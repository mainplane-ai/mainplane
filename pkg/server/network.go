package server

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"

	petname "github.com/dustinkirkland/golang-petname"

	"github.com/mainplane-ai/mainplane/pkg/auth"
	"github.com/mainplane-ai/mainplane/pkg/pointer"
)

// Network is what a person types to make a machine a worker of the harness:
// its name on the pointer, then its device code.
type Network struct {
	Name string `json:"name"`
	Code string `json:"code"`
}

func (n Network) String() string { return n.Name + " " + n.Code }

func networkPath(dir string) string { return filepath.Join(dir, "network.json") }

// LoadNetwork is the network of the harness in dir.
func LoadNetwork(dir string) (Network, error) {
	var n Network
	b, err := os.ReadFile(networkPath(dir))
	if err != nil {
		return n, err
	}
	return n, json.Unmarshal(b, &n)
}

func (n Network) save(dir string) error {
	b, err := json.MarshalIndent(n, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(networkPath(dir), b, 0o600)
}

// network gives the harness with k in dir a random name, claimed on the
// pointer, and a device code, unless it has them. A name another harness
// holds is passed over.
func network(dir string, k ed25519.PrivateKey) error {
	_, err := LoadNetwork(dir)
	if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	n := Network{Code: auth.NewCode()}
	for {
		n.Name = fmt.Sprintf("%s-%03d", petname.Generate(2, "-"), rand.IntN(1000))
		if err := pointer.Claim(context.Background(), k, n.Name); !errors.Is(err, pointer.ErrTaken) {
			if err != nil {
				return err
			}
			return n.save(dir)
		}
	}
}

// Rename makes name the installed harness's network name. Its workers stay:
// they know the harness by its key.
func Rename(name string) (Network, error) {
	n, err := LoadNetwork(Dir)
	if err != nil {
		return n, err
	}
	k, err := Key(Dir)
	if err != nil {
		return n, err
	}
	if err := pointer.Claim(context.Background(), k, name); err != nil {
		return n, fmt.Errorf("%s: %w", name, err)
	}
	n.Name = name
	return n, n.save(Dir)
}

// Cycle gives the installed harness a new device code. Its workers stay; a
// machine joins with the new code only.
func Cycle() (Network, error) {
	n, err := LoadNetwork(Dir)
	if err != nil {
		return n, err
	}
	n.Code = auth.NewCode()
	return n, n.save(Dir)
}
