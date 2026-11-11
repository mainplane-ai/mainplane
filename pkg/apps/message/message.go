// Package message is the message app: one agent posts to another by its ID.
// The ID is the capability: there is no list, and the recipient needs no app.
package message

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/mainplane-ai/mainplane/pkg/apps"
	"github.com/mainplane-ai/mainplane/pkg/harness"
)

//go:embed skill.md
var Skill []byte

// New serves POST / {"to","text"}, posting as agent:<caller>.
func New(ctx context.Context, h *harness.Harness, _ string) (http.Handler, error) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /{$}", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ To, Text string }
		err := json.NewDecoder(http.MaxBytesReader(w, r.Body, harness.MaxPost)).Decode(&in)
		if err == nil && (in.To == "" || in.Text == "") {
			err = errors.New(`a message is {"to": "<agent id>", "text": "..."}`)
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if _, err := h.GetAgent(in.To); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		sid, n, err := h.PostAgent(ctx, in.To, "agent:"+r.Header.Get(apps.AgentHeader), r.Header.Get("Idempotency-Key"), []harness.Part{{Type: "text/plain", Body: []byte(in.Text)}})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"session": sid, "n": n})
	})
	return mux, nil
}
