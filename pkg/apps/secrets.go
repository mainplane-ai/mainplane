package apps

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sync"
)

// secretName is an environment variable's name, since a run app gets each
// secret as one.
var secretName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

// smu holds each read and rewrite of .secrets whole.
var smu sync.Mutex

func (r *Registry) secretsPath() string { return filepath.Join(r.Dir, ".secrets") }

// secrets is .secrets: a JSON object, so a PEM's lines or an = in a value
// need no quoting. A file that does not read is logged and holds nothing.
func (r *Registry) secrets() map[string]string {
	m := map[string]string{}
	b, err := os.ReadFile(r.secretsPath())
	if err == nil {
		err = json.Unmarshal(b, &m)
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Printf("apps: %s: %v", r.secretsPath(), err)
	}
	return m
}

// SecretNames is the name of every secret; no route reads a value.
func (r *Registry) SecretNames() []string {
	return append([]string{}, slices.Sorted(maps.Keys(r.secrets()))...) // [] when none, never null
}

// SetSecret writes one secret, then starts each run app it completes.
func (r *Registry) SetSecret(name, value string) error {
	if !secretName.MatchString(name) {
		return fmt.Errorf("a secret's name is an environment variable's: [A-Za-z_][A-Za-z0-9_]*")
	}
	if err := r.edit(func(m map[string]string) { m[name] = value }); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, a := range r.apps {
		r.launch(a)
	}
	return nil
}

// DeleteSecret removes one secret. A running app keeps the value it started
// with; its next launch waits for the secret.
func (r *Registry) DeleteSecret(name string) error {
	found := false
	err := r.edit(func(m map[string]string) {
		_, found = m[name]
		delete(m, name)
	})
	if err == nil && !found {
		err = fmt.Errorf("no secret %s", name)
	}
	return err
}

// edit rewrites .secrets through a file beside it, 0600 from its creation.
func (r *Registry) edit(change func(map[string]string)) error {
	smu.Lock()
	defer smu.Unlock()
	m := r.secrets()
	change(m)
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := r.secretsPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, r.secretsPath())
}
