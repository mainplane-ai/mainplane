package statefile

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Sessions is the directory that holds one directory per session, each holding
// numbered state files. The filesystem is the registry.
type Sessions struct {
	Dir string
}

func (s Sessions) path(id string, file int) string {
	return filepath.Join(s.Dir, id, fmt.Sprintf("%04d.state", file))
}

// Live returns the number of the highest state file in a session, or 0 if none.
func (s Sessions) Live(id string) (int, error) {
	names, err := filepath.Glob(filepath.Join(s.Dir, id, "*.state"))
	if err != nil {
		return 0, err
	}
	live := 0
	for _, name := range names {
		n, err := strconv.Atoi(strings.TrimSuffix(filepath.Base(name), ".state"))
		if err == nil && n > live {
			live = n
		}
	}
	return live, nil
}

// Start creates a session with one file: start, then config.
func (s Sessions) Start(id string, config []byte) (*File, error) {
	if err := os.MkdirAll(filepath.Join(s.Dir, id), 0o755); err != nil {
		return nil, err
	}
	return Create(s.path(id, 1), Record{Header: Header{Kind: Start}}, config)
}

// Link adds the next file to a session, or the first file to a new one, whose
// first record points at a position in another file. Fork, revert, compaction,
// and config change are all this call.
func (s Sessions) Link(id string, from Position, mode Mode, config []byte) (*File, error) {
	if err := os.MkdirAll(filepath.Join(s.Dir, id), 0o755); err != nil {
		return nil, err
	}
	live, err := s.Live(id)
	if err != nil {
		return nil, err
	}
	first := Record{Header: Header{Kind: Link, From: &from, Mode: mode}}
	return Create(s.path(id, live+1), first, config)
}

// Open opens a session's live file for appending.
func (s Sessions) Open(id string) (*File, error) {
	live, err := s.Live(id)
	if err != nil {
		return nil, err
	}
	if live == 0 {
		return nil, fmt.Errorf("session %s: no state file", id)
	}
	return Open(s.path(id, live))
}

// Load returns the records the live file stands for: its own, preceded by
// everything a continue link points at, recursively. Each record's Seq says
// which file of the chain it came from, oldest first.
func (s Sessions) Load(id string) ([]Record, error) {
	live, err := s.Live(id)
	if err != nil {
		return nil, err
	}
	if live == 0 {
		return nil, fmt.Errorf("session %s: no state file", id)
	}
	return s.chain(Position{Session: id, File: live, N: 0})
}

// chain loads a file up to position n (0 means all), after its own ancestry.
func (s Sessions) chain(p Position) ([]Record, error) {
	recs, err := Read(s.path(p.Session, p.File))
	if err != nil {
		return nil, err
	}
	if p.N > 0 && p.N <= len(recs) {
		recs = recs[:p.N-1]
	}
	var out []Record
	if len(recs) > 0 && recs[0].Kind == Link && recs[0].Mode == Continue {
		if out, err = s.chain(*recs[0].From); err != nil {
			return nil, err
		}
	}
	seq := 0
	if len(out) > 0 {
		seq = out[len(out)-1].Seq + 1
	}
	for _, r := range recs {
		r.Seq = seq
		out = append(out, r)
	}
	return out, nil
}
