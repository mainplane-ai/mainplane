package worker

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// A worker updates only from here and only to what the release key signed:
// the harness names a version, never a place or bytes, so a harness cannot
// make a root worker run a binary we did not release. The private half of the
// key is the RELEASE_SIGNING_KEY secret that release.yml signs SHA256SUMS
// with. A download gets minutes: a binary is megabytes on any link.
const (
	dl         = "https://dl.mainplane.ai/"
	releaseKey = "Epmoycu6ik6l3iJiEtfOAdK+DLFDVXhgdc+uNv6ua/U="
	dlTimeout  = 5 * time.Minute
)

// update replaces the running binary with release v's: SHA256SUMS must carry
// the release key's signature, the binary must match its line in it, and the
// new binary must say it is v. The old one is kept beside it as .old, since
// Windows renames a running binary but will not replace it. It returns the
// path the service runs.
func update(v string) (string, error) {
	sums, err := fetch(v + "/SHA256SUMS")
	if err != nil {
		return "", err
	}
	sig, err := fetch(v + "/SHA256SUMS.sig")
	if err != nil {
		return "", err
	}
	key, _ := base64.StdEncoding.DecodeString(releaseKey)
	if !ed25519.Verify(key, sums, sig) {
		return "", fmt.Errorf("release %s: SHA256SUMS is not signed by the release key", v)
	}
	name := "mainplane-" + runtime.GOOS + "-" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	want := ""
	for _, line := range strings.Split(string(sums), "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[1] == name {
			want = f[0]
		}
	}
	if want == "" {
		return "", fmt.Errorf("release %s has no %s", v, name)
	}
	b, err := fetch(v + "/" + name)
	if err != nil {
		return "", err
	}
	if got := sha256.Sum256(b); hex.EncodeToString(got[:]) != want {
		return "", fmt.Errorf("release %s: %s does not match SHA256SUMS", v, name)
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(exe+".new", b, 0o755); err != nil {
		return "", err
	}
	if err := os.Chmod(exe+".new", 0o755); err != nil {
		return "", err
	}
	out, err := exec.Command(exe+".new", "version").Output()
	if err != nil {
		return "", fmt.Errorf("release %s: %s does not run: %w", v, name, err)
	}
	if got := string(bytes.TrimSpace(out)); got != v {
		return "", fmt.Errorf("release %s: %s says it is %s", v, name, got)
	}
	if err := os.Rename(exe, exe+".old"); err != nil {
		return "", err
	}
	if err := os.Rename(exe+".new", exe); err != nil {
		return "", errors.Join(err, os.Rename(exe+".old", exe))
	}
	return exe, nil
}

func fetch(path string) ([]byte, error) {
	c := http.Client{Timeout: dlTimeout}
	resp, err := c.Get(dl + path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s%s: %s", dl, path, resp.Status)
	}
	return io.ReadAll(resp.Body)
}
