package pointer

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
)

// LinuxDir is the harness's directory on Linux, where admin serves the
// sessions drive from, and Config the config in the harness's directory.
const (
	LinuxDir = "/var/lib/mainplane-server"
	Config   = "config.json"
)

// Dir is the directory of the harness installed on this machine. Only root
// and the operator may read it.
var Dir = map[string]string{
	"linux":   LinuxDir,
	"darwin":  "/Library/Application Support/mainplane-server",
	"windows": filepath.Join(os.Getenv("ProgramData"), "mainplane-server"),
}[runtime.GOOS]

// Local is the loopback URL of the harness installed on this machine, when
// this process may read its config and the harness proves key there; else
// none. There it is reached with neither the tunnel nor the pointer.
func Local(ctx context.Context, key string) string {
	u := local()
	if u == "" || Prove(ctx, key, u) != nil {
		return ""
	}
	return u
}

// Self is the URL the harness installed on this machine is reached at, as it
// proves on loopback: none before its tunnel is up.
func Self(ctx context.Context, key string) (string, error) {
	u := local()
	if u == "" {
		return "", errors.New("no harness installed on this machine, or this user may not read its config")
	}
	r, err := prove(ctx, key, u)
	return r.URL, err
}

// local is the loopback URL in the config of the harness installed on this
// machine, when this process may read it.
func local() string {
	var c struct {
		HTTP string `json:"http"` // server.Config's
	}
	b, err := os.ReadFile(filepath.Join(Dir, Config))
	if err != nil || json.Unmarshal(b, &c) != nil || c.HTTP == "" {
		return ""
	}
	return "http://" + c.HTTP
}

// loopback is whether u is on this machine.
func loopback(u string) bool {
	p, err := url.Parse(u)
	return err == nil && net.ParseIP(p.Hostname()).IsLoopback()
}
