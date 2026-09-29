// Package tunnel is a self-hosted harness's front door: a cloudflared child
// that carries one public HTTPS URL to the harness's loopback port, so the
// harness opens no inbound port and needs no domain.
package tunnel

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	// version is the cloudflared release every harness runs. A bump is a PR
	// that changes it and every sum in assets.
	version  = "2026.9.3"
	releases = "https://github.com/cloudflare/cloudflared/releases/download/"
	quickAPI = "https://api.trycloudflare.com/tunnel"
	dohAPI   = "https://cloudflare-dns.com/dns-query?type=A&name="
	// cloudflared rides out network loss on its own and exits only when it
	// cannot go on. Starting again at once would spin.
	restartWait = 5 * time.Second
	// A new quick tunnel's name resolves seconds after Cloudflare makes it,
	// but a resolver asked before then keeps the miss for up to 60s, the
	// trycloudflare.com negative TTL.
	nameWait = 90 * time.Second
)

// QuickWarning is what the harness install and the CLI say of a quick
// tunnel's URL.
const QuickWarning = "warning: this quick tunnel URL is not meant for production use; see https://github.com/mainplane-ai/mainplane/blob/main/docs/self-hosting.md"

// QuickURL says whether url is a quick tunnel's: Cloudflare names every one
// under trycloudflare.com.
func QuickURL(url string) bool { return strings.HasSuffix(url, ".trycloudflare.com") }

// assets are the release file for each platform and its sha256, as GitHub
// states it for that file. Cloudflare ships no Windows arm64 build; the amd64
// one runs there under emulation.
var assets = map[string][2]string{
	"linux/amd64":   {"cloudflared-linux-amd64", "77e26d8d900e0b8469f416239d14b5f296525fdf79fee6f511ef55609e3fbac2"},
	"linux/arm64":   {"cloudflared-linux-arm64", "aaeb2d7d0da3614634c7e03ab13487a1522c2e79165ed2929cfe23d5e95b326d"},
	"darwin/amd64":  {"cloudflared-darwin-amd64.tgz", "d1155d0837487f261183b15c1eab6c4ebcad9dc49b94675f1524c3564cea3977"},
	"darwin/arm64":  {"cloudflared-darwin-arm64.tgz", "587c2cfb1c230fe36c7fa7727da78be459dae028cabe8c001291999350f07095"},
	"windows/amd64": {"cloudflared-windows-amd64.exe", "f096265ec2fcbe9bb6e2d64268db167ced3fcbb83d894bdb9e2fcdb26f2ea7e2"},
	"windows/arm64": {"cloudflared-windows-amd64.exe", "f096265ec2fcbe9bb6e2d64268db167ced3fcbb83d894bdb9e2fcdb26f2ea7e2"},
}

// credentials is a quick tunnel as cloudflared reads it from a credentials
// file, with the hostname Cloudflare gave it, which cloudflared ignores.
type credentials struct {
	AccountTag   string
	TunnelSecret []byte
	TunnelID     string
	Hostname     string
}

// Quick runs a quick tunnel to origin until ctx ends, and calls up with its
// URL each time cloudflared starts. dir keeps cloudflared and the tunnel's
// credentials, so a restart comes back at the same URL while Cloudflare keeps
// the tunnel, about 10 minutes after its last connection.
func Quick(ctx context.Context, dir, origin string, up func(url string)) error {
	bin, conf, err := prepare(dir)
	if err != nil {
		return err
	}
	file := filepath.Join(dir, "tunnel.json")
	for {
		c, fresh, err := quick(file)
		if err != nil {
			return err
		}
		ok, err := named(ctx, c.Hostname, fresh)
		if err != nil {
			return err
		}
		if ok {
			up("https://" + c.Hostname)
			err = run(ctx, bin, conf, origin, nil, "--credentials-file", file, c.TunnelID)
		} else {
			err = errors.Join(fmt.Errorf("%s does not resolve: Cloudflare dropped the tunnel", c.Hostname), os.Remove(file))
		}
		if err := pause(ctx, err); err != nil {
			return err
		}
	}
}

// Own runs the user's own tunnel, by the token Cloudflare gave for it, until
// ctx ends, and calls up with url each time cloudflared starts. The user
// routes url to the tunnel and the tunnel to origin in Cloudflare.
func Own(ctx context.Context, dir, url, token, origin string, up func(url string)) error {
	bin, conf, err := prepare(dir)
	if err != nil {
		return err
	}
	for {
		up(url)
		// in the environment, since any local user can read a command line
		if err := pause(ctx, run(ctx, bin, conf, origin, []string{"TUNNEL_TOKEN=" + token})); err != nil {
			return err
		}
	}
}

// prepare fetches cloudflared into dir and writes its config there: bin and
// conf. cloudflared also reads a config at its default paths, where a user's
// own tunnel may route every host elsewhere. Its own file keeps that out.
func prepare(dir string) (bin, conf string, err error) {
	if bin, err = fetch(dir); err != nil {
		return "", "", err
	}
	conf = filepath.Join(dir, "cloudflared.yml")
	return bin, conf, os.WriteFile(conf, []byte("no-autoupdate: true\n"), 0o644)
}

// run is cloudflared at bin carrying a tunnel to origin until it exits or
// ctx ends, with env added and args after run.
func run(ctx context.Context, bin, conf, origin string, env []string, args ...string) error {
	cmd := exec.CommandContext(ctx, bin, append([]string{"tunnel", "--config", conf, "--loglevel", "warn", "--url", origin, "run"}, args...)...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	return fmt.Errorf("cloudflared: %w", cmd.Run())
}

// pause logs why cloudflared stopped and waits restartWait, unless ctx ends
// first; then it is ctx's error.
func pause(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	log.Printf("tunnel: %v; again in %s", err, restartWait)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(restartWait):
		return nil
	}
}

// named is whether host resolves: at once for a saved tunnel's name, which
// goes when Cloudflare drops the tunnel, and within nameWait for a fresh one.
// It asks Cloudflare's resolver over HTTPS, so this machine's resolver never
// caches a miss for a name about to exist. A lookup that fails, as SERVFAIL
// does, is an error, not a missing name.
func named(ctx context.Context, host string, fresh bool) (bool, error) {
	client := http.Client{Timeout: 10 * time.Second}
	for end := time.Now().Add(nameWait); ; {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, dohAPI+host, nil)
		if err != nil {
			return false, err
		}
		req.Header.Set("Accept", "application/dns-json")
		resp, err := client.Do(req)
		if err != nil {
			return false, err
		}
		var r struct {
			Status int // an RCODE: 0 NOERROR, 3 NXDOMAIN
			Answer []json.RawMessage
		}
		err = json.NewDecoder(resp.Body).Decode(&r)
		_ = resp.Body.Close()
		if err != nil {
			return false, fmt.Errorf("%s%s: %s: %w", dohAPI, host, resp.Status, err)
		}
		if r.Status != 0 && r.Status != 3 {
			return false, fmt.Errorf("%s%s: rcode %d", dohAPI, host, r.Status)
		}
		if len(r.Answer) > 0 || !fresh || time.Now().After(end) {
			return len(r.Answer) > 0, nil
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// quick is the tunnel saved in file, or a new one from Cloudflare, saved, and
// whether it is new.
func quick(file string) (credentials, bool, error) {
	var c credentials
	b, err := os.ReadFile(file)
	if err == nil {
		return c, false, json.Unmarshal(b, &c)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return c, false, err
	}
	resp, err := http.Post(quickAPI, "application/json", nil)
	if err != nil {
		return c, false, err
	}
	defer func() { _ = resp.Body.Close() }()
	var r struct {
		Result struct {
			ID         string `json:"id"`
			Hostname   string `json:"hostname"`
			AccountTag string `json:"account_tag"`
			Secret     []byte `json:"secret"`
		} `json:"result"`
	}
	b, _ = io.ReadAll(resp.Body)
	if err := json.Unmarshal(b, &r); err != nil || r.Result.ID == "" || r.Result.Hostname == "" {
		return c, false, fmt.Errorf("%s answered %s: %s", quickAPI, resp.Status, b)
	}
	c = credentials{AccountTag: r.Result.AccountTag, TunnelSecret: r.Result.Secret, TunnelID: r.Result.ID, Hostname: r.Result.Hostname}
	if b, err = json.Marshal(c); err != nil {
		return c, false, err
	}
	return c, true, os.WriteFile(file, b, 0o600)
}

// fetch is the path of this platform's cloudflared in dir, downloaded and
// checked against its sum the first time.
func fetch(dir string) (string, error) {
	a, ok := assets[runtime.GOOS+"/"+runtime.GOARCH]
	if !ok {
		return "", fmt.Errorf("no cloudflared for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	bin := filepath.Join(dir, "cloudflared-"+version)
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	if _, err := os.Stat(bin); err == nil {
		return bin, nil
	}
	url := releases + version + "/" + a[0]
	log.Printf("tunnel: downloading %s", url)
	resp, err := http.Get(url)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if sum := sha256.Sum256(b); hex.EncodeToString(sum[:]) != a[1] {
		return "", fmt.Errorf("%s: %s, sha256 %x, not %s", url, resp.Status, sum, a[1])
	}
	if strings.HasSuffix(a[0], ".tgz") {
		if b, err = untar(b); err != nil {
			return "", err
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(bin+".new", b, 0o755); err != nil {
		return "", err
	}
	return bin, os.Rename(bin+".new", bin)
}

// untar is the cloudflared file in a gzipped tar.
func untar(b []byte) ([]byte, error) {
	z, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	t := tar.NewReader(z)
	for {
		h, err := t.Next()
		if err != nil {
			return nil, fmt.Errorf("no cloudflared in the archive: %w", err)
		}
		if filepath.Base(h.Name) == "cloudflared" {
			return io.ReadAll(t)
		}
	}
}
