// Package message is the built-in capability-by-ID agent messaging app.
package message

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"strings"

	"github.com/mainplane-ai/mainplane/pkg/statefile"
)

// maxBody bounds one agent-to-agent text message.
const maxBody = 1 << 20

//go:embed skill.md
var Skill string

//go:embed app.json
var ManifestJSON []byte

// Host is what message needs from the harness.
type Host interface {
	PostText(ctx context.Context, agentID, via, text string) (session string, n int, err error)
}

// App implements POST /apps/message.
type App struct {
	host Host
}

func New(host Host) *App { return &App{host: host} }

func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	caller := r.Header.Get("X-Mainplane-Agent")
	if caller == "" {
		http.Error(w, "agent context required", http.StatusForbidden)
		return
	}
	if p := strings.TrimPrefix(r.URL.Path, "/"); p != "" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var in struct {
		To   string `json:"to"`
		Text string `json:"text"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxBody)).Decode(&in); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !statefile.ValidID(in.To) {
		http.Error(w, "bad target agent id", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(in.Text) == "" {
		http.Error(w, "text is required", http.StatusBadRequest)
		return
	}
	sid, n, err := a.host.PostText(r.Context(), in.To, "agent:"+caller, in.Text)
	if errors.Is(err, fs.ErrNotExist) {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"session": sid, "n": n})
}
