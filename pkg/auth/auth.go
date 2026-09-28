// Package auth is the two credentials a harness checks. An api key lets a
// connector call every HTTP route. A join secret lets a machine dial in as a
// worker; it can add a worker and nothing else. Both kinds live hashed in one
// table on the admin drive. What a person pastes is a token: the kind, the
// key of the harness its holder finds through the pointer, and the secret
// itself.
//
//	mp_<kind>_<harness key>.<secret>
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	Key  = "key"  // a connector's credential: every HTTP route
	Join = "join" // a worker's credential: the dial in
)

// An address has this many refusals a window. That is more than a worker
// with a stale token makes, redialing every 5s, so it never locks out the
// other workers behind the same NAT. It is far too few to guess a
// 26-character secret.
const (
	refusals = 30
	window   = time.Minute
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
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o755); err != nil {
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

// Set makes secret the entry of kind named name, in place of any before it.
func (s Store) Set(kind, name, secret string) error {
	t, err := s.Load()
	if err != nil {
		return err
	}
	t[kind] = append(slices.DeleteFunc(t[kind], func(e Entry) bool { return e.Name == name }), Entry{Name: name, Hash: Hash(secret)})
	return s.save(t)
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

// Bearer refuses a request whose Authorization: Bearer is not a live api key,
// and one from an address l turns away. A request with no key is refused but
// not counted: it guesses nothing.
func (s Store) Bearer(l *Limit, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a := Addr(r)
		if err := l.Wait(a); err != nil {
			http.Error(w, err.Error(), http.StatusTooManyRequests)
			return
		}
		secret, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || !s.Check(Key, secret) {
			if ok {
				l.Refused(a)
			}
			http.Error(w, "api key refused", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Limit counts refused credentials of both kinds by address, and turns an
// address away for the rest of a window once it has too many: a secret is 26
// random characters, but nothing should get to guess at it freely.
type Limit struct {
	mu sync.Mutex
	m  map[string]strikes
}

// strikes are an address's refusals in the window that began at start.
type strikes struct {
	n     int
	start time.Time
}

// Wait is why addr is turned away now, or nil.
func (l *Limit) Wait(addr string) error {
	l.mu.Lock()
	s := l.m[addr]
	l.mu.Unlock()
	if w := window - time.Since(s.start); s.n >= refusals && w > 0 {
		return fmt.Errorf("too many refused credentials from %s: try again in %s", addr, (w + time.Second - 1).Truncate(time.Second))
	}
	return nil
}

// Refused counts a refused credential from addr, and forgets every address
// whose window has passed.
func (l *Limit) Refused(addr string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.m == nil {
		l.m = map[string]strikes{}
	}
	for a, s := range l.m {
		if time.Since(s.start) > window {
			delete(l.m, a)
		}
	}
	s, ok := l.m[addr]
	if !ok {
		s.start = time.Now()
	}
	s.n++
	l.m[addr] = s
}

// Addr is who sent r: the client address cloudflared puts in
// CF-Connecting-IP, else the peer. The harness listens on loopback only, so
// every peer is cloudflared or this machine, and the header can be trusted.
func Addr(r *http.Request) string {
	if a := r.Header.Get("CF-Connecting-IP"); a != "" {
		return a
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	return host
}

func Hash(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func Token(kind, key, secret string) string {
	return "mp_" + kind + "_" + key + "." + secret
}

// Parse is a token's harness key and secret, refused when it is not of kind.
func Parse(kind, token string) (key, secret string, err error) {
	rest, ok := strings.CutPrefix(token, "mp_"+kind+"_")
	key, secret, dot := strings.Cut(rest, ".")
	if !ok || !dot || key == "" || secret == "" {
		return "", "", fmt.Errorf("not a %s token", kind)
	}
	return key, secret, nil
}
