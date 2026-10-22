package pointer

import (
	"crypto/ed25519"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
)

// Dir is the directory of the harness installed on this machine, and KeyFile
// and Config its harness key and config there. Only root and the operator
// may read it.
var Dir = map[string]string{
	"linux":   "/var/lib/mainplane-server",
	"darwin":  "/Library/Application Support/mainplane-server",
	"windows": filepath.Join(os.Getenv("ProgramData"), "mainplane-server"),
}[runtime.GOOS]

const (
	KeyFile = "harness.key"
	Config  = "config.json"
)

// Local is the loopback URL of the harness with key when it is the one
// installed on this machine and this process may read its directory, else
// none. There the harness is reached with neither the tunnel nor a proof.
func Local(key string) string {
	b, err := os.ReadFile(filepath.Join(Dir, KeyFile))
	if err != nil || len(b) != ed25519.SeedSize || Encode(ed25519.NewKeyFromSeed(b)) != key {
		return ""
	}
	var c struct {
		HTTP string `json:"http"` // server.Config's
	}
	if b, err = os.ReadFile(filepath.Join(Dir, Config)); err != nil || json.Unmarshal(b, &c) != nil || c.HTTP == "" {
		return ""
	}
	return "http://" + c.HTTP
}
