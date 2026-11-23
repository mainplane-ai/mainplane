// Package continueapp is the built-in app continue: an agent hands off to
// its next session at a point it picks, before its context limit.
package continueapp

import (
	_ "embed"
	"io"
	"net/http"
	"strings"

	"github.com/mainplane-ai/mainplane/pkg/harness"
)

//go:embed skill.md
var Skill string

// maxText bounds a handoff: the next session reads it whole as its first
// message, and the log of this one holds the rest.
const maxText = 16 << 10

// New is the app's handler: POST / with the handoff text as the body. The
// agent's session hands off when its turn ends.
func New(h *harness.Harness) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /{$}", func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxText))
		if err != nil || strings.TrimSpace(string(b)) == "" {
			http.Error(w, "the body is the handoff text, at most 16 KiB", http.StatusBadRequest)
			return
		}
		if err := h.ContinueAgent(r.Header.Get("X-Mainplane-Session"), string(b)); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	})
	return mux
}
