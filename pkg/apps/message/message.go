// Package message is the built-in app that carries a message from one agent
// to another. Knowing an agent's id is the right to message it: there is no
// route that lists agents, and the one who receives needs no app.
package message

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/mainplane-ai/mainplane/pkg/harness"
)

//go:embed skill.md
var Skill string

// New is the app's handler: POST / with {"to","text"} posts text to agent
// to, from the agent the harness says is calling.
func New(ctx context.Context, h *harness.Harness) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /{$}", func(w http.ResponseWriter, r *http.Request) {
		var m struct{ To, Text string }
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, harness.MaxPost)).Decode(&m); err != nil || m.To == "" || m.Text == "" {
			http.Error(w, `the body is {"to":"<agent id>","text":"..."}`, http.StatusBadRequest)
			return
		}
		from, key := "agent:"+r.Header.Get("X-Mainplane-Agent"), r.Header.Get("Idempotency-Key")
		if key != "" {
			key = from + ":" + key // a key is its sender's: two agents that pick the same one do not collide
		}
		_, _, err := h.PostAgent(ctx, m.To, from, key, []harness.Part{{Type: "text/plain", Body: []byte(m.Text)}})
		switch {
		case errors.Is(err, harness.ErrNoAgent):
			http.Error(w, err.Error(), http.StatusNotFound)
		case err != nil:
			http.Error(w, err.Error(), http.StatusBadRequest)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	})
	return mux
}
