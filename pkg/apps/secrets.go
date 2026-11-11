package apps

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"
)

// Secrets is the write-only 0600 JSON key-value store at <harness-dir>/.secrets.
// Values never leave through an HTTP read route; each app receives only the
// secret names declared in its own app.json.
type Secrets struct {
	path     string
	onChange func()
	mu       sync.RWMutex
}

func newSecrets(path string, onChange func()) *Secrets {
	return &Secrets{path: path, onChange: onChange}
}

func validSecretName(name string) error {
	if name == "" {
		return errors.New("secret name is empty")
	}
	for i := range len(name) {
		c := name[i]
		ok := 'A' <= c && c <= 'Z' || 'a' <= c && c <= 'z' || c == '_' || i > 0 && '0' <= c && c <= '9'
		if !ok {
			return fmt.Errorf("secret %q: names use letters, digits, and underscores", name)
		}
	}
	return nil
}

func (s *Secrets) loadLocked() (map[string]string, error) {
	m := map[string]string{}
	b, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	return m, json.Unmarshal(b, &m)
}

func (s *Secrets) saveLocked(m map[string]string) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(b)
	if err := errors.Join(werr, f.Sync(), f.Close()); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, s.path)
}

// List returns the sorted secret names without their values.
func (s *Secrets) List() ([]string, error) {
	s.mu.RLock()
	m, err := s.loadLocked()
	s.mu.RUnlock()
	if err != nil {
		return nil, err
	}
	names := slices.Sorted(maps.Keys(m))
	if names == nil {
		return []string{}, nil
	}
	return names, nil
}

// Get reads one secret value for internal injection into an app that declared it.
func (s *Secrets) Get(name string) (string, bool) {
	s.mu.RLock()
	m, err := s.loadLocked()
	s.mu.RUnlock()
	if err != nil {
		return "", false
	}
	v, ok := m[name]
	return v, ok
}

// Set stores a secret value and notifies the manager so waiting apps can start.
func (s *Secrets) Set(name, value string) error {
	if err := validSecretName(name); err != nil {
		return err
	}
	s.mu.Lock()
	m, err := s.loadLocked()
	if err == nil {
		m[name] = value
		err = s.saveLocked(m)
	}
	s.mu.Unlock()
	if err == nil && s.onChange != nil {
		s.onChange()
	}
	return err
}

// Delete removes a secret and notifies the manager.
func (s *Secrets) Delete(name string) error {
	s.mu.Lock()
	m, err := s.loadLocked()
	if err == nil {
		delete(m, name)
		err = s.saveLocked(m)
	}
	s.mu.Unlock()
	if err == nil && s.onChange != nil {
		s.onChange()
	}
	return err
}
