package statefile

import (
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
	names, err := filepath.Glob(filepath.Join(s.Dir, "*.state"))
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(names))
	for i, name := range names {
		ids[i] = strings.TrimSuffix(filepath.Base(name), ".state")
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

// Load returns a session's records.
func (s Sessions) Load(id string) ([]Record, error) {
	return Read(s.Path(id))
}
