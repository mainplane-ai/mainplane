package worker

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// The js interpreter is Bun running run.js, with cdp.js, a CDP client, and
// cdp.md, what it does, beside it in <scratch>/js. Every worker has it.
const (
	// BunVersion is pinned, so a snippet behaves the same on every worker. A bump is
	// a PR that changes the version and every sum below, from the release's
	// SHASUMS256.txt.
	BunVersion = "1.4.2"
	bunURL     = "https://github.com/oven-sh/bun/releases/download/bun-v" + BunVersion + "/"
	// The zip is under 40 MB; a download that stalls fails instead of holding
	// the first js run.
	bunWait = 5 * time.Minute
	// done ends a snippet: run.js reads lines up to one that starts with NUL,
	// which no snippet has, and answers with the mark and the exit code.
	jsDone = "\x00%s"
)

// bunBuilds is the zip for each platform and its sha256. x64 takes the
// baseline build, which needs no AVX2, so an old CPU runs it too.
var bunBuilds = map[string][2]string{
	"linux/amd64":   {"bun-linux-x64-baseline.zip", "c678040f14fe0440eb839d37cbd0ce4c051a32da72806ac97de6a6aab6bf728f"},
	"linux/arm64":   {"bun-linux-aarch64.zip", "54328bbc2d9c8e0c9f892c544d66c57a83b84139e34909e5ee81758f1ac8fda7"},
	"darwin/amd64":  {"bun-darwin-x64-baseline.zip", "bad5bbd6cf14d0980d115f5954c9ff904df619d5e994d2da1ffccd3f316300b0"},
	"darwin/arm64":  {"bun-darwin-aarch64.zip", "90987a3a16d7db556d886ac3d551e7b6d3edf0a1cf43acaed622e8676be1d12f"},
	"windows/amd64": {"bun-windows-x64-baseline.zip", "78c221c2376f79731ccf4e4af0b3bb46d81fefa3296c5abee09ad8a1b21e68c6"},
	"windows/arm64": {"bun-windows-aarch64.zip", "a7a16b876a305fd1029c66dbd27007b4f6112ae896532f675878731a21e50cfd"},
}

//go:embed js
var jsFiles embed.FS

// jsReady is whether this process readied the js folder. One at start and a
// first js run's wait for each other.
var jsReady struct {
	sync.Mutex
	done bool
}

func jsDir(scratch string) string { return filepath.Join(scratch, "js") }

func bunPath(dir string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(dir, "bun.exe")
	}
	return filepath.Join(dir, "bun")
}

// interp is how to start the interpreter name. js is readied first unless
// this process did so, as it does at start unless the operator was not
// logged in.
func (s *server) interp(name string) (interp, error) {
	if name != "js" {
		return interps[name], nil
	}
	if err := readyJS(s.Local); err != nil {
		return interp{}, err
	}
	dir := jsDir(s.Scratch)
	return interp{[]string{bunPath(dir), filepath.Join(dir, "run.js")}, jsDone}, nil
}

// startJS readies the js folder when the worker starts, so a new release's
// files and Bun are there before the first run asks.
func startJS(l Local) {
	if err := readyJS(l); err != nil {
		log.Printf("js: %v", err)
	}
}

// readyJS runs JS as the operator: scratch is theirs, and a root write into a
// folder they control could be sent anywhere by a link. A root worker runs
// its own binary as them, `mainplane js <dir>`.
func readyJS(l Local) error {
	jsReady.Lock()
	defer jsReady.Unlock()
	if jsReady.done {
		return nil
	}
	dir := jsDir(l.Scratch)
	if l.Operator == nil {
		err := JS(dir)
		jsReady.done = err == nil
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, "js", dir)
	if err := prepare(cmd, l.Operator); err != nil {
		return err
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("js: %w: %s", err, bytes.TrimSpace(out))
	}
	jsReady.done = true
	return nil
}

// JS makes dir the js interpreter's: the embedded files written over, and
// Bun fetched when the one there is not BunVersion.
func JS(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	files, err := fs.ReadDir(jsFiles, "js")
	if err != nil {
		return err
	}
	for _, f := range files {
		b, err := jsFiles.ReadFile("js/" + f.Name())
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, f.Name()), b, 0o644); err != nil {
			return err
		}
	}
	bun := bunPath(dir)
	if out, err := exec.Command(bun, "--version").Output(); err == nil && strings.TrimSpace(string(out)) == BunVersion {
		return nil
	}
	b, err := fetchBun()
	if err != nil {
		return err
	}
	if err := os.WriteFile(bun+".new", b, 0o755); err != nil {
		return err
	}
	if err := os.Chmod(bun+".new", 0o755); err != nil {
		return err
	}
	// Windows renames a running binary but will not replace it, and an
	// environment may be running the old one.
	_ = os.Remove(bun + ".old")
	if err := os.Rename(bun, bun+".old"); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return os.Rename(bun+".new", bun)
}

// fetchBun is this platform's Bun binary from its pinned zip.
func fetchBun() ([]byte, error) {
	build, ok := bunBuilds[runtime.GOOS+"/"+runtime.GOARCH]
	if !ok {
		return nil, fmt.Errorf("no Bun build for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	url := bunURL + build[0]
	resp, err := (&http.Client{Timeout: bunWait}).Get(url)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if fmt.Sprintf("%x", sha256.Sum256(b)) != build[1] {
		return nil, fmt.Errorf("%s does not match its sha256", url)
	}
	z, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return nil, err
	}
	for _, f := range z.File {
		if name := f.Name[strings.LastIndex(f.Name, "/")+1:]; name == "bun" || name == "bun.exe" {
			r, err := f.Open()
			if err != nil {
				return nil, err
			}
			defer func() { _ = r.Close() }()
			return io.ReadAll(r)
		}
	}
	return nil, fmt.Errorf("%s has no bun binary", url)
}
