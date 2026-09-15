package statefile

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Sessions is the directory that holds one state file per session, named by
// its id. The filesystem is the registry.
type Sessions struct {
	Dir string
}

func (s Sessions) Path(id string) string {
	return filepath.Join(s.Dir, id+".state")
}

// List returns every session id: the file names.
func (s Sessions) List() ([]string, error) {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		if id, ok := strings.CutSuffix(e.Name(), ".state"); ok {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// Start creates a session: start, then config.
func (s Sessions) Start(id string, config []byte) (*File, error) {
	return Create(s.Path(id), config)
}

// Copy creates session id from records 1 through n-1 of session from. Fork
// and revert are both this call.
func (s Sessions) Copy(from, id string, n int) error {
	return Copy(s.Path(from), s.Path(id), n)
}

// Open opens a session's file for appending.
func (s Sessions) Open(id string) (*File, error) {
	return Open(s.Path(id))
}

// Load returns a session's records. A file with none is a create that died
// before its first write, not a session.
func (s Sessions) Load(id string) ([]Record, error) {
	recs, err := Read(s.Path(id))
	if err == nil && len(recs) == 0 {
		return nil, fmt.Errorf("session %s has no records", id)
	}
	return recs, err
}
