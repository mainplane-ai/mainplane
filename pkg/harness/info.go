package harness

import (
	"context"
	"encoding/json"
	"log"
	"slices"
	"time"

	"github.com/mainplane-ai/mainplane/pkg/api"

	"github.com/mainplane-ai/mainplane/pkg/provider"
	"github.com/mainplane-ai/mainplane/pkg/statefile"
)

// cacheOf is the cache a step kept, from its header: its TTL counted from
// the anchor, the request's start where the anchor is not measured.
func cacheOf(r statefile.Record) (api.Cache, bool) {
	var c provider.Cache
	if r.Kind != statefile.Step || len(r.Cache) == 0 || json.Unmarshal(r.Cache, &c) != nil || c.TTL == 0 {
		return api.Cache{}, false
	}
	at := r.Sent
	if c.Anchor == "end" || at.IsZero() {
		at = r.Time
	}
	return api.Cache{Until: at.Add(time.Duration(c.TTL) * time.Second), Likely: c.Likely}, true
}

// Info reads one session's file.
func (h *Harness) Info(id string) (api.Info, error) {
	chain, err := h.Sessions.Load(id)
	if err != nil {
		return api.Info{}, err
	}
	conf, err := config(chain)
	if err != nil {
		return api.Info{}, err
	}
	tip := chain[len(chain)-1]
	info := api.Info{ID: id, Via: chain[0].Via, Status: h.status(id, chain), Config: conf, Created: chain[0].Time, N: tip.N, ContextUsed: statefile.ContextUsed(chain)}
	for _, r := range chain {
		if r.Kind == statefile.Name {
			info.Name = string(r.Body)
		} else {
			info.Updated = r.Time
		}
		if r.Kind == statefile.Step && r.Usage != nil {
			info.Usage.Input += r.Usage.Input
			info.Usage.Output += r.Usage.Output
			info.Usage.CacheRead += r.Usage.CacheRead
			info.Usage.CacheWrite += r.Usage.CacheWrite
		}
		if c, ok := cacheOf(r); ok {
			info.Cache = c
		}
	}
	h.lockIndex()
	if !h.gone[id] { // a read that began before a delete does not bring it back
		h.index[id] = info
	}
	h.imu.Unlock()
	return info, nil
}

// cached is a session's Info from the index. The file is read only when a
// write touched it since the index last read it, or the index has none. A
// touched file that does not read keeps its mark, so the next call reads it
// again, and what the index has of it is the answer until then.
func (h *Harness) cached(id string) (api.Info, error) {
	h.lockIndex()
	info, ok := h.index[id]
	stale := h.dirty[id]
	delete(h.dirty, id) // before the read, as List does
	h.imu.Unlock()
	if ok && !stale {
		return info, nil
	}
	fresh, err := h.Info(id)
	if err == nil || !ok {
		return fresh, err
	}
	log.Printf("session %s: %v", id, err)
	h.touch(id)
	return info, nil
}

// lockIndex takes imu with the maps ready.
func (h *Harness) lockIndex() {
	h.imu.Lock()
	if h.index == nil {
		h.index, h.dirty, h.gone = map[string]api.Info{}, map[string]bool{}, map[string]bool{}
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
func (h *Harness) List() ([]api.Info, error) {
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
	infos := make([]api.Info, 0, len(ids))
	for _, id := range ids {
		h.imu.Lock()
		info, ok := h.index[id]
		stale := h.dirty[id]
		delete(h.dirty, id)
		if !ok && !stale || h.gone[id] {
			h.imu.Unlock()
			continue // deleted since the ids were taken
		}
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
	slices.SortFunc(infos, func(a, b api.Info) int { return b.Updated.Compare(a.Updated) })
	return infos, nil
}

// copyLogs writes the session's file to every worker its config lists, at
// <scratch>/logs/<id>.log, the bodies the envelope takes as media dropped so
// rg works on it. A
// worker not connected is skipped; the session's next stop writes again.
// A copy a worker already has whole is not written to it again: a write
// empties the file first, under whoever reads it, and after a handoff that
// is the next session's first run.
func (h *Harness) copyLogs(ctx context.Context, s *session) {
	id, n := s.id, s.exports.Add(1)
	s.logs.Lock()
	defer s.logs.Unlock()
	if s.exports.Load() != n {
		return // a newer call waits and writes a newer snapshot
	}
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
	p, _, err := h.provider(conf.Model)
	if err != nil {
		log.Printf("session %s: log copy: %v", id, err)
		return
	}
	var b []byte
	for _, r := range chain {
		if p.Envelope.Accepts(r.Type) {
			r.Body = nil
		}
		enc, err := statefile.Encode(r)
		if err != nil {
			log.Printf("session %s: log copy: %v", id, err)
			return
		}
		b = append(b, enc...)
	}
	tip := chain[len(chain)-1].N
	for _, w := range conf.Workers {
		r, ok := h.Workers.Get(w.Name)
		if !ok || s.copied[w.Name] == tip {
			continue
		}
		if err := r.Write(ctx, r.Scratch+"/logs/"+id+".log", b); err != nil {
			log.Printf("session %s: log copy to %s: %v", id, w.Name, err)
			continue
		}
		if s.copied == nil {
			s.copied = map[string]int{}
		}
		s.copied[w.Name] = tip
	}
}
