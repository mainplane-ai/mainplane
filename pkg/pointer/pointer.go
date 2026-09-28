// Package pointer is how a worker or connector finds its harness from the
// key its token carries. pointer.mainplane.ai maps the key to the harness's
// current URL, signed by the key. Neither the pointer nor the URL is trusted:
// the harness at the URL proves the key before any secret goes there.
//
//	PUT pointer.mainplane.ai/<key>  {url, seq, sig}  sig over "<key> <url> <seq>"
//	GET <url>/id?nonce=<n>          {url, sig,       sig over "id <url> <n>"
//	                                 noise}          noise over "noise <coordinator's Noise key>"
package pointer

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"time"
)

const api = "https://pointer.mainplane.ai/"

// The pointer and a harness answer in well under this; one that has not is
// down, and a worker should try again rather than hang.
var client = http.Client{Timeout: 10 * time.Second}

type record struct {
	URL   string `json:"url"`
	Seq   int64  `json:"seq,omitempty"` // a proof has none
	Sig   string `json:"sig"`
	Noise string `json:"noise,omitempty"` // only a proof has one
}

// Key is the harness key kept in file, made there the first time. Only its
// owner may read it: whoever has it can move the harness's workers.
func Key(file string) (ed25519.PrivateKey, error) {
	b, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		_, k, err := ed25519.GenerateKey(nil)
		if err != nil {
			return nil, err
		}
		return k, os.WriteFile(file, k.Seed(), 0o600)
	}
	if err != nil {
		return nil, err
	}
	if len(b) != ed25519.SeedSize {
		return nil, fmt.Errorf("%s is not a harness key", file)
	}
	return ed25519.NewKeyFromSeed(b), nil
}

// Encode is k's public half as tokens and the pointer carry it.
func Encode(k ed25519.PrivateKey) string {
	return base64.RawURLEncoding.EncodeToString(k.Public().(ed25519.PublicKey))
}

func sign(k ed25519.PrivateKey, msg string) string {
	return base64.RawURLEncoding.EncodeToString(ed25519.Sign(k, []byte(msg)))
}

func decode(key string) (ed25519.PublicKey, error) {
	pub, err := base64.RawURLEncoding.DecodeString(key)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("%q is not a harness key", key)
	}
	return pub, nil
}

func verify(key, msg, sig string) error {
	pub, err := decode(key)
	if err != nil {
		return err
	}
	s, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || !ed25519.Verify(pub, []byte(msg), s) {
		return errors.New("the signature does not verify against the harness key")
	}
	return nil
}

func recordMsg(key, url string, seq int64) string { return fmt.Sprintf("%s %s %d", key, url, seq) }

// idMsg is what a harness signs to prove its key. The prefix keeps a proof
// from ever reading as a record, which starts with the key.
func idMsg(url, nonce string) string { return "id " + url + " " + nonce }

// noiseMsg is what a harness signs to vouch for its coordinator's Noise key.
// It needs no nonce or URL: only the holder of the Noise key's private half
// can finish a Noise handshake, so a replayed signature gains nothing.
func noiseMsg(noise string) string { return "noise " + noise }

// Publish puts url on the pointer as the URL of k's harness. seq is the time,
// so the harness keeps no counter.
func Publish(ctx context.Context, k ed25519.PrivateKey, url string) error {
	key, seq := Encode(k), time.Now().UnixMilli()
	b, err := json.Marshal(record{URL: url, Seq: seq, Sig: sign(k, recordMsg(key, url, seq))})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, api+key, bytes.NewReader(b))
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNoContent {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("publish to %s: %s: %s", api, resp.Status, b)
	}
	return nil
}

// Find is where the harness with key is: cached, when it proves the key
// there, else the URL on the pointer, once it proves the key there.
func Find(ctx context.Context, key, cached string) (string, error) {
	if _, err := decode(key); err != nil {
		return "", err
	}
	if cached != "" && Prove(ctx, key, cached) == nil {
		return cached, nil
	}
	var r record
	if err := get(ctx, api+key, &r); err != nil {
		return "", err
	}
	if err := verify(key, recordMsg(key, r.URL, r.Seq), r.Sig); err != nil {
		return "", fmt.Errorf("the pointer's record: %w", err)
	}
	return r.URL, Prove(ctx, key, r.URL)
}

// Prove is nil when the harness at url holds key. It signs a fresh nonce with
// the URL it knows itself by, which must be url: a host that relays another
// harness's answer fails.
func Prove(ctx context.Context, key, url string) error {
	_, err := prove(ctx, key, url)
	return err
}

// ProveNoise is nil when the harness at url holds key and vouches for noise
// as its coordinator's Noise key.
func ProveNoise(ctx context.Context, key, url, noise string) error {
	r, err := prove(ctx, key, url)
	if err != nil {
		return err
	}
	if err := verify(key, noiseMsg(noise), r.Noise); err != nil {
		return fmt.Errorf("%s: Noise key %s: %w", url, noise, err)
	}
	return nil
}

func prove(ctx context.Context, key, url string) (record, error) {
	nonce := rand.Text()
	var r record
	if err := get(ctx, url+"/id?nonce="+nonce, &r); err != nil {
		return r, err
	}
	if r.URL != url {
		return r, fmt.Errorf("%s says it is %s", url, r.URL)
	}
	if err := verify(key, idMsg(url, nonce), r.Sig); err != nil {
		return r, fmt.Errorf("%s: %w", url, err)
	}
	return r, nil
}

// ID answers a proof for the harness with k, now reached at url(), whose
// coordinator has the Noise key noise.
func ID(k ed25519.PrivateKey, url func() string, noise string) http.Handler {
	ns := sign(k, noiseMsg(noise))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := url()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(record{URL: u, Sig: sign(k, idMsg(u, r.URL.Query().Get("nonce"))), Noise: ns})
	})
}

func get(ctx context.Context, url string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}
