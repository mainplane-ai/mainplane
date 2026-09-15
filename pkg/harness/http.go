package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mainplane-ai/mainplane/pkg/statefile"
)

// maxPost bounds one post: a paragraph and a screenshot, typed or pasted by a
// person. A file goes to a worker, not into the state file.
const maxPost = 8 << 20

// Handler is the harness's HTTP surface: seven verbs on a session and three
// reads of the environment. Records go out in the state file's own bytes,
// one after another.
//
//	GET  /sessions                    ?status= ?limit= ?after=<id>                 -> [Info], newest updated first
//	POST /sessions                    Create body                                  -> {"id"}
//	GET  /sessions/{id}               -> Info
//	GET  /sessions/{id}/records       ?after=N  ?wait=30s to long poll             -> records
//	POST /sessions/{id}/records       one body, or multipart; ?via= names the      -> {"n"}
//	                                  connector. one message record per part,
//	                                  each with its Content-Type
//	POST /sessions/{id}/run           ?via=  step from the tip, whatever it is     -> {"status"}
//	POST /sessions/{id}/stop          ?via=  cut the step, write it down           -> {"status"}
//	GET  /workers                     -> connected workers' hellos
//	GET  /providers                   -> names this harness can serve as provider/model
//	GET  /                            -> {"version"}
//
// ctx outlives every request: it is what steps run under.
func Handler(ctx context.Context, h *Harness) http.Handler {
	mux := http.NewServeMux()
	fail := func(w http.ResponseWriter, err error) { http.Error(w, err.Error(), http.StatusBadRequest) }
	reply := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	via := func(r *http.Request) string {
		if v := r.URL.Query().Get("via"); v != "" {
			return v
		}
		return "http"
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
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		reply(w, map[string]string{"version": Version})
	})
	mux.HandleFunc("GET /workers", func(w http.ResponseWriter, r *http.Request) {
		reply(w, h.Workers.List())
	})
	mux.HandleFunc("GET /providers", func(w http.ResponseWriter, r *http.Request) {
		names := make([]string, 0, len(h.Providers))
		for name := range h.Providers {
			names = append(names, name)
		}
		slices.Sort(names)
		reply(w, names)
	})
	mux.HandleFunc("GET /sessions", func(w http.ResponseWriter, r *http.Request) {
		infos, err := h.List()
		if err != nil {
			fail(w, err)
			return
		}
		reply(w, page(infos, r.URL.Query().Get("status"), r.URL.Query().Get("after"), r.URL.Query().Get("limit")))
	})
	mux.HandleFunc("POST /sessions", func(w http.ResponseWriter, r *http.Request) {
		var c Create
		if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
			fail(w, err)
			return
		}
		id, err := h.Create(ctx, c)
		if err != nil {
			fail(w, err)
			return
		}
		reply(w, map[string]string{"id": id})
	})
	handle("GET /sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		info, err := h.Info(r.PathValue("id"))
		if err != nil {
			fail(w, err)
			return
		}
		reply(w, info)
	})
	handle("GET /sessions/{id}/records", h.serveRecords)
	handle("POST /sessions/{id}/records", func(w http.ResponseWriter, r *http.Request) {
		parts, err := parts(r.Header.Get("Content-Type"), http.MaxBytesReader(w, r.Body, maxPost))
		if err != nil {
			fail(w, err)
			return
		}
		n, err := h.Post(ctx, r.PathValue("id"), via(r), parts)
		if err != nil {
			fail(w, err)
			return
		}
		reply(w, map[string]int{"n": n})
	})
	handle("POST /sessions/{id}/run", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if _, err := h.Info(id); err != nil {
			fail(w, err)
			return
		}
		h.Retry(ctx, id)
		reply(w, map[string]statefile.Status{"status": statefile.StatusStepping})
	})
	handle("POST /sessions/{id}/stop", func(w http.ResponseWriter, r *http.Request) {
		if err := h.Stop(ctx, r.PathValue("id"), via(r)); err != nil {
			fail(w, err)
			return
		}
		reply(w, map[string]statefile.Status{"status": statefile.StatusFailed})
	})
	return mux
}

// page filters a list by status, skips through the entry named by after, and
// cuts it to limit. Empty means no filter, no skip, no cut.
func page(infos []Info, status, after, limit string) []Info {
	if status != "" {
		infos = slices.DeleteFunc(infos, func(i Info) bool { return string(i.Status) != status })
	}
	if after != "" {
		if i := slices.IndexFunc(infos, func(i Info) bool { return i.ID == after }); i >= 0 {
			infos = infos[i+1:]
		}
	}
	if n, err := strconv.Atoi(limit); err == nil && n >= 0 && n < len(infos) {
		infos = infos[:n]
	}
	return infos
}

// parts reads a post body as one part, or as each part of a multipart body.
// Every part states its content type: the record's Type is the client's word,
// never a guess.
func parts(ctype string, body io.Reader) ([]Part, error) {
	mt, params, err := mime.ParseMediaType(ctype)
	if err != nil {
		return nil, fmt.Errorf("content type: %w", err)
	}
	if !strings.HasPrefix(mt, "multipart/") {
		b, err := io.ReadAll(body)
		return []Part{{Type: ctype, Body: b}}, err
	}
	var out []Part
	mr := multipart.NewReader(body, params["boundary"])
	for {
		p, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			if len(out) == 0 {
				return nil, fmt.Errorf("multipart body has no parts")
			}
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		if p.Header.Get("Content-Type") == "" {
			return nil, fmt.Errorf("part %d has no content type", len(out)+1)
		}
		b, err := io.ReadAll(p)
		if err != nil {
			return nil, err
		}
		out = append(out, Part{Type: p.Header.Get("Content-Type"), Body: b})
	}
}

// serveRecords writes the records after ?after, long polling for ?wait when
// there are none. The change channel is taken before the read, so an append
// between read and wait is not missed.
func (h *Harness) serveRecords(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	after, _ := strconv.Atoi(r.URL.Query().Get("after"))
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

// records is the file after record n. Records are numbered from 1 with no
// gaps, so n is an offset; past the tip it is a cursor from some other file.
func (h *Harness) records(id string, after int) ([]statefile.Record, error) {
	chain, err := h.Sessions.Load(id)
	if err != nil {
		return nil, err
	}
	if after > len(chain) {
		return nil, fmt.Errorf("after %d, session has %d records", after, len(chain))
	}
	return chain[max(after, 0):], nil
}
