// Package release is how a binary becomes another release's. Every file comes
// from dl.mainplane.ai and counts only when its hash is in a SHA256SUMS the
// release key signed. A caller names a version, never a place or bytes, so
// whoever picks the version, a harness for its workers or a person for the
// harness, cannot make root run a binary we did not release. The private half
// of the key is the RELEASE_SIGNING_KEY secret that release.yml signs
// SHA256SUMS with. A download gets minutes: a binary is megabytes on any link.
package release

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	DL      = "https://dl.mainplane.ai/" // every release's files, install scripts included, under its version
	key     = "Epmoycu6ik6l3iJiEtfOAdK+DLFDVXhgdc+uNv6ua/U="
	timeout = 5 * time.Minute
	// script is the latest release's install script for Linux and macOS; with
	// .ps1, for Windows.
	script = "https://mainplane.ai/install"
)

// InstallLines run the latest install script with args: on Linux and macOS,
// then on Windows.
func InstallLines(args string) (unix, windows string) {
	return "curl -fsSL " + script + " | sh -s -- " + args, "& ([scriptblock]::Create((irm " + script + ".ps1))) " + args
}

// Latest is the newest stable release, which release.yml writes on stable
// tags only.
func Latest() (string, error) {
	b, err := fetch("latest")
	return string(bytes.TrimSpace(b)), err
}

// Get is file of release v, checked against v's signed SHA256SUMS.
func Get(v, file string) ([]byte, error) {
	sums, err := fetch(v + "/SHA256SUMS")
	if err != nil {
		return nil, err
	}
	sig, err := fetch(v + "/SHA256SUMS.sig")
	if err != nil {
		return nil, err
	}
	k, _ := base64.StdEncoding.DecodeString(key)
	if !ed25519.Verify(k, sums, sig) {
		return nil, fmt.Errorf("release %s: SHA256SUMS is not signed by the release key", v)
	}
	want := ""
	for _, line := range strings.Split(string(sums), "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[1] == file {
			want = f[0]
		}
	}
	if want == "" {
		return nil, fmt.Errorf("release %s has no %s", v, file)
	}
	b, err := fetch(v + "/" + file)
	if err != nil {
		return nil, err
	}
	if got := sha256.Sum256(b); hex.EncodeToString(got[:]) != want {
		return nil, fmt.Errorf("release %s: %s does not match SHA256SUMS", v, file)
	}
	return b, nil
}

// Install puts release v's build of cmd for this machine at path. The new
// binary must say it is v. The old one moves aside: see Aside.
func Install(path, cmd, v string) error {
	name := cmd + "-" + runtime.GOOS + "-" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	b, err := Get(v, name)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path+".new", b, 0o755); err != nil {
		return err
	}
	if err := os.Chmod(path+".new", 0o755); err != nil {
		return err
	}
	if err := says(path+".new", v, name); err != nil {
		return err
	}
	old, err := Aside(path)
	if err != nil {
		return err
	}
	if err := os.Rename(path+".new", path); err != nil {
		return errors.Join(err, os.Rename(old, path))
	}
	return nil
}

// InstallApp puts release v's macOS app bundle for this machine at app: the
// zip of its Contents unpacked beside it, then renamed over the old bundle,
// which goes. A binary still running from the old one keeps running.
func InstallApp(app, v string) error {
	name := "mainplane-darwin-" + runtime.GOARCH + ".app.zip"
	b, err := Get(v, name)
	if err != nil {
		return err
	}
	z, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return fmt.Errorf("release %s: %s: %w", v, name, err)
	}
	if err := errors.Join(os.RemoveAll(app+".new"), os.RemoveAll(app+".old")); err != nil {
		return err
	}
	for _, f := range z.File {
		if err := unzip(f, app+".new"); err != nil {
			return fmt.Errorf("release %s: %s: %w", v, name, err)
		}
	}
	if err := says(filepath.Join(app+".new", "Contents", "MacOS", "mainplane"), v, name); err != nil {
		return err
	}
	if err := os.Rename(app, app+".old"); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.Rename(app+".new", app); err != nil {
		return errors.Join(err, os.Rename(app+".old", app))
	}
	return os.RemoveAll(app + ".old")
}

// unzip writes f under dir with its mode, set apart from the write, which
// root's umask would narrow.
func unzip(f *zip.File, dir string) error {
	if !filepath.IsLocal(f.Name) {
		return fmt.Errorf("%s is outside the bundle", f.Name)
	}
	p := filepath.Join(dir, f.Name)
	if f.FileInfo().IsDir() {
		return os.MkdirAll(p, 0o755)
	}
	r, err := f.Open()
	if err != nil {
		return err
	}
	b, err := io.ReadAll(r)
	if err := errors.Join(err, r.Close(), os.MkdirAll(filepath.Dir(p), 0o755)); err != nil {
		return err
	}
	if err := os.WriteFile(p, b, f.Mode().Perm()); err != nil {
		return err
	}
	return os.Chmod(p, f.Mode().Perm())
}

// says checks that the binary at path runs and says it is v.
func says(path, v, name string) error {
	out, err := exec.Command(path, "version").Output()
	if err != nil {
		return fmt.Errorf("release %s: %s does not run: %w", v, name, err)
	}
	if got := string(bytes.TrimSpace(out)); got != v {
		return fmt.Errorf("release %s: %s says it is %s", v, name, got)
	}
	return nil
}

// Aside renames path to a name of its own beside it and returns that name, so
// a new binary can take path. Windows renames a running binary but will not
// replace or delete it, and a TUI may run any old binary for days, so each
// gets a new name and the asides from before go only once nothing runs them.
// Elsewhere a rename replaces a running binary, so path stays where it is: on
// macOS each aside would be one more path client row in Privacy settings.
func Aside(path string) (string, error) {
	for _, f := range Asides(path) {
		_ = os.Remove(f) // one still running stays for a later Aside
	}
	if runtime.GOOS != "windows" {
		return path, nil
	}
	old := fmt.Sprintf("%s.old-%d", path, time.Now().UnixNano())
	return old, os.Rename(path, old)
}

// Asides are the old binaries Aside left beside path, and the .old that
// releases before it left. A directory that cannot be read has none: Aside's
// rename and uninstall's removals then fail on it by name.
func Asides(path string) []string {
	dir, base := filepath.Dir(path), filepath.Base(path)
	es, _ := os.ReadDir(dir)
	var m []string
	for _, e := range es {
		if n := e.Name(); n == base+".old" || strings.HasPrefix(n, base+".old-") {
			m = append(m, filepath.Join(dir, n))
		}
	}
	return m
}

func fetch(path string) ([]byte, error) {
	c := http.Client{Timeout: timeout}
	resp, err := c.Get(DL + path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s%s: %s", DL, path, resp.Status)
	}
	return io.ReadAll(resp.Body)
}
