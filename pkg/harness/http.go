package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/mainplane-ai/mainplane/pkg/statefile"
)

// maxMessage bounds a posted message: it is typed or pasted by a person. A
// file goes to a worker, not into the state file.
const maxMessage = 1 << 20

// Handler is the harness's HTTP surface: the three operations and status.
// Records go out in the state file's own bytes, one after another, with the
// file number in each header so a client can name a position. A session's
// records are its own files; a link record's from names where the rest is.
//
//	POST /sessions                    Link body; without from, a fresh session    -> {"id"}
//	POST /sessions/{id}/link          Link body                                   -> {"file"}
//	POST /sessions/{id}/messages      the message; ?via= names the connector      -> Position
//	GET  /sessions/{id}/records       ?after=FILE.N  ?wait=30s to long poll       -> records
//	GET  /sessions/{id}               {"status", "live"}
//
// ctx outlives every request: it is what steps run under.
func Handler(ctx context.Context, h *Harness) http.Handler {
	mux := http.NewServeMux()
	fail := func(w http.ResponseWriter, err error) { http.Error(w, err.Error(), http.StatusBadRequest) }
	reply := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	// handle refuses an {id} that is not one of ours before it becomes a path.
	handle := func(pattern string, fn http.HandlerFunc) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			if id := r.PathValue("id"); !statefile.ValidID(id) {
				fail(w, fmt.Errorf("bad session id %q", id))
				return
			}
			fn(w, r)
		})
	}
	mux.HandleFunc("POST /sessions", func(w http.ResponseWriter, r *http.Request) {
		var l Link
		if err := json.NewDecoder(r.Body).Decode(&l); err != nil {
			fail(w, err)
			return
		}
		id, err := h.Create(ctx, l)
		if err != nil {
			fail(w, err)
			return
		}
		reply(w, map[string]string{"id": id})
	})
	handle("POST /sessions/{id}/link", func(w http.ResponseWriter, r *http.Request) {
		var l Link
		if err := json.NewDecoder(r.Body).Decode(&l); err != nil {
			fail(w, err)
			return
		}
		file, err := h.Link(ctx, r.PathValue("id"), l)
		if err != nil {
			fail(w, err)
			return
		}
		reply(w, map[string]int{"file": file})
	})
	handle("POST /sessions/{id}/messages", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxMessage))
		if err != nil {
			fail(w, err)
			return
		}
		via := r.URL.Query().Get("via")
		if via == "" {
			via = "http"
		}
		p, err := h.Post(ctx, r.PathValue("id"), via, r.Header.Get("Content-Type"), body)
		if err != nil {
			fail(w, err)
			return
		}
		reply(w, p)
	})
	handle("GET /sessions/{id}/records", h.serveRecords)
	handle("GET /sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		st, live, err := h.Status(r.PathValue("id"))
		if err != nil {
			fail(w, err)
			return
		}
		reply(w, map[string]any{"status": st, "live": live})
	})
	return mux
}

// serveRecords writes the records after ?after, long polling for ?wait when
// there are none. The change channel is taken before the read, so an append
// between read and wait is not missed.
func (h *Harness) serveRecords(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var after statefile.Position
	_, _ = fmt.Sscanf(r.URL.Query().Get("after"), "%d.%d", &after.File, &after.N)
	changed := h.Changed(id)
	recs, err := h.records(id, after)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if wait, _ := time.ParseDuration(r.URL.Query().Get("wait")); len(recs) == 0 && wait > 0 {
		select {
		case <-changed:
		case <-time.After(wait):
		case <-r.Context().Done():
		}
		if recs, err = h.records(id, after); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	w.Header().Set("Content-Type", "application/x-mainplane-records")
	for _, rec := range recs {
		b, err := statefile.Encode(rec)
		if err != nil {
			return
		}
		_, _ = w.Write(b)
	}
}

// records is the live chain after position p, restricted to the session's own
// files: what a fork inherited is read from its parent, where the link record
// points. Positions are unique within a session and nowhere else. A position
// not in the chain, which is what a revert leaves a client holding, gives the
// whole chain.
func (h *Harness) records(id string, p statefile.Position) ([]statefile.Record, error) {
	chain, err := h.Sessions.Load(id)
	if err != nil {
		return nil, err
	}
	var own []statefile.Record
	for _, r := range chain {
		if r.Session == id {
			own = append(own, r)
		}
	}
	for i, r := range own {
		if r.File == p.File && r.N == p.N {
			return own[i+1:], nil
		}
	}
	return own, nil
}
