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
	"github.com/mainplane-ai/mainplane/pkg/worker"
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
	if err := json.Unmarshal(b, &n); err != nil {
		return n, err
	}
	// an empty code would let anyone in who runs the exchange with one; a
	// harness with no pointer has no name
	if n.Code == "" {
		return n, fmt.Errorf("%s has no code", networkPath(dir))
	}
	return n, nil
}

// save writes the network in dir. join cycle and rename write it as root, in
// the operator's dir, so a link there leads nowhere outside it.
func (n Network) save(dir string) error {
	b, err := json.MarshalIndent(n, "", "  ")
	if err != nil {
		return err
	}
	r, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()
	return r.WriteFile(filepath.Base(networkPath(dir)), b, 0o600)
}

// adminSecret is admin's join secret, which no device code is: it changes
// with the code, as admin's own secret did with each install.
func adminSecret(k ed25519.PrivateKey, code string) string {
	return auth.Secret(k, worker.Admin+" "+code)
}

// network gives the harness with k in dir a device code and, with claim, a
// random name claimed on the pointer, unless it has them. A name another
// harness holds is passed over.
func network(dir string, k ed25519.PrivateKey, claim bool) error {
	_, err := LoadNetwork(dir)
	if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	n := Network{Code: auth.NewCode()}
	if !claim {
		return n.save(dir)
	}
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

// Join is what a device's install line takes to join the harness with k:
// the network name and device code, or with no pointer, a join token pinned
// to url, the harness URL.
func (n Network) Join(k ed25519.PrivateKey, noPointer bool, url string) string {
	if !noPointer {
		return n.String()
	}
	return auth.Pin(auth.Token(auth.Join, pointer.Encode(k), auth.Secret(k, n.Code)), url)
}

// Rename makes name the installed harness's network name. Its workers stay:
// they know the harness by its key.
func Rename(name string) (Network, error) {
	c, err := Load(Conf)
	if err != nil {
		return Network{}, err
	}
	if c.NoPointer {
		return Network{}, errors.New("this harness has no pointer, so it has no network name: its join lines carry its URL")
	}
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
