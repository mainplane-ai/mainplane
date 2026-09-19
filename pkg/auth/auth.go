// Package auth is the two credentials a harness checks. An api key lets a
// connector call every HTTP route. A join secret lets a machine dial in as a
// worker; it can add a worker and nothing else. Both kinds live hashed in one
// table on the admin drive. What a person pastes is a token: the kind, the
// address its holder needs, and the secret itself.
//
//	mp_<kind>_<base64url addr>.<secret>
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"slices"
	"strings"
)

const (
	Key  = "key"  // a connector's credential: every HTTP route
	Join = "join" // a worker's credential: the dial in
)

type Entry struct {
	Name string `json:"name"`
	Hash string `json:"hash"`
}

// Table is the live entries by kind. A revoked entry is gone, not marked.
type Table map[string][]Entry

// Store is the table as a file, read on every check so a new or revoked
// entry counts at once.
type Store struct{ Path string }

func (s Store) Load() (Table, error) {
	t := Table{}
	b, err := os.ReadFile(s.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return t, nil
	}
	if err != nil {
		return nil, err
	}
	return t, json.Unmarshal(b, &t)
}

func (s Store) save(t Table) error {
	b, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.Path, b, 0o600)
}

// Issue adds an entry and returns its secret: the one time it is in the clear.
func (s Store) Issue(kind, name string) (string, error) {
	t, err := s.Load()
	if err != nil {
		return "", err
	}
	if slices.ContainsFunc(t[kind], func(e Entry) bool { return e.Name == name }) {
		return "", fmt.Errorf("%s %q exists", kind, name)
	}
	secret := rand.Text()
	t[kind] = append(t[kind], Entry{Name: name, Hash: Hash(secret)})
	return secret, s.save(t)
}

func (s Store) Revoke(kind, name string) error {
	t, err := s.Load()
	if err != nil {
		return err
	}
	n := len(t[kind])
	t[kind] = slices.DeleteFunc(t[kind], func(e Entry) bool { return e.Name == name })
	if len(t[kind]) == n {
		return fmt.Errorf("no %s %q", kind, name)
	}
	return s.save(t)
}

// Check is whether secret is a live entry of kind.
func (s Store) Check(kind, secret string) bool {
	t, err := s.Load()
	if err != nil {
		log.Printf("auth: %v", err)
		return false
	}
	h, ok := []byte(Hash(secret)), false
	for _, e := range t[kind] {
		ok = ok || subtle.ConstantTimeCompare(h, []byte(e.Hash)) == 1
	}
	return ok
}

// Bearer refuses a request whose Authorization: Bearer is not a live api key.
func (s Store) Bearer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secret, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || !s.Check(Key, secret) {
			http.Error(w, "api key refused", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func Hash(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func Token(kind, addr, secret string) string {
	return "mp_" + kind + "_" + base64.RawURLEncoding.EncodeToString([]byte(addr)) + "." + secret
}

// Parse is a token's address and secret, refused when it is not of kind.
func Parse(kind, token string) (addr, secret string, err error) {
	rest, ok := strings.CutPrefix(token, "mp_"+kind+"_")
	enc, secret, dot := strings.Cut(rest, ".")
	b, derr := base64.RawURLEncoding.DecodeString(enc)
	if !ok || !dot || derr != nil {
		return "", "", fmt.Errorf("not a %s token", kind)
	}
	return string(b), secret, nil
}
