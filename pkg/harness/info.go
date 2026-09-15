package harness

import (
	"context"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/mainplane-ai/mainplane/pkg/statefile"
)

// Info is what GET /sessions/{id} and each entry of GET /sessions say about a
// session: everything the tip states, no cost and no title. Prompt is the
// last step's prompt in tokens, against Config.Context.
type Info struct {
	ID      string           `json:"id"`
	Status  statefile.Status `json:"status"`
	Config  statefile.Conf   `json:"config"`
	Created time.Time        `json:"created"`
	Updated time.Time        `json:"updated"`
	N       int              `json:"n"`
	Prompt  int              `json:"prompt"`
	Usage   statefile.Usage  `json:"usage"`
}

// Info reads one session's file.
func (h *Harness) Info(id string) (Info, error) {
	chain, err := h.Sessions.Load(id)
	if err != nil {
		return Info{}, err
	}
	conf, err := config(chain)
	if err != nil {
		return Info{}, err
	}
	tip := chain[len(chain)-1]
	info := Info{ID: id, Status: h.status(id, chain), Config: conf, Created: chain[0].Time, Updated: tip.Time, N: tip.N, Prompt: statefile.Prompt(chain)}
	for _, r := range chain {
		if r.Kind == statefile.Step && r.Usage != nil {
			info.Usage.Input += r.Usage.Input
			info.Usage.Output += r.Usage.Output
			info.Usage.CacheRead += r.Usage.CacheRead
			info.Usage.CacheWrite += r.Usage.CacheWrite
		}
	}
	h.lockIndex()
	h.index[id] = info
	h.imu.Unlock()
	return info, nil
}

// lockIndex takes imu with the maps ready.
func (h *Harness) lockIndex() {
	h.imu.Lock()
	if h.index == nil {
		h.index, h.dirty = map[string]Info{}, map[string]bool{}
	}
}

// touch marks a session's index entry stale. Memory is a cache of the file:
// the next list reads the file again.
func (h *Harness) touch(id string) {
	h.lockIndex()
	h.dirty[id] = true
	h.imu.Unlock()
}

// List is every session, newest updated first, from the index. Entries a
// write touched since the last list are read again; the rest are served from
// memory. The mark is cleared before the read, so a write that lands during
// the read marks it again. A file that does not read is logged and left out
// until its next write. Resume fills the index on start, and touch adds a
// created session.
func (h *Harness) List() ([]Info, error) {
	h.lockIndex()
	ids := make([]string, 0, len(h.index)+len(h.dirty))
	for id := range h.index {
		ids = append(ids, id)
	}
	for id := range h.dirty {
		if _, ok := h.index[id]; !ok {
			ids = append(ids, id)
		}
	}
	h.imu.Unlock()
	infos := make([]Info, 0, len(ids))
	for _, id := range ids {
		h.imu.Lock()
		info, stale := h.index[id], h.dirty[id]
		delete(h.dirty, id)
		h.imu.Unlock()
		if stale {
			var err error
			if info, err = h.Info(id); err != nil {
				log.Printf("session %s: %v", id, err)
				continue
			}
		}
		infos = append(infos, info)
	}
	slices.SortFunc(infos, func(a, b Info) int { return b.Updated.Compare(a.Updated) })
	return infos, nil
}

// copyLogs writes the session's file to every worker its config lists, at
// <scratch>/logs/<id>.state, image bodies dropped so rg works on it. A
// worker not connected is skipped; the session's next stop writes again.
func (h *Harness) copyLogs(ctx context.Context, id string) {
	chain, err := h.Sessions.Load(id)
	if err != nil {
		log.Printf("session %s: log copy: %v", id, err)
		return
	}
	conf, err := config(chain)
	if err != nil {
		log.Printf("session %s: log copy: %v", id, err)
		return
	}
	var b []byte
	for _, r := range chain {
		if strings.HasPrefix(r.Type, "image/") {
			r.Body = nil
		}
		enc, err := statefile.Encode(r)
		if err != nil {
			log.Printf("session %s: log copy: %v", id, err)
			return
		}
		b = append(b, enc...)
	}
	for _, w := range conf.Workers {
		r, ok := h.Workers.Get(w.Name)
		if !ok {
			continue
		}
		if err := r.Write(ctx, r.Scratch+"/logs/"+id+".state", b); err != nil {
			log.Printf("session %s: log copy to %s: %v", id, w.Name, err)
		}
	}
}
