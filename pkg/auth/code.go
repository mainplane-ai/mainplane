package auth

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"filippo.io/cpace"
)

// A device code is what a person types beside a network name to make a
// machine a worker: 8 Crockford base32 characters, shown as XXXX-XXXX, one
// per harness. It is short because it never travels. The machine and the
// harness run CPace with it, which gives a guess one live try and nothing to
// test offline, so neither the pointer, the URL nor the tunnel learns it. The
// harness answers with the join secret sealed under the key they share, which
// only a machine that knew the code opens.
//
//	POST <url>/join  {"a": CPace message A}  ->  {"b": CPace message B, "box": the join secret, sealed}
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// ErrCode is a device code the harness does not hold.
var ErrCode = errors.New("wrong device code")

// NewCode is a random device code.
func NewCode() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = crockford[b[i]&31]
	}
	return string(b[:4]) + "-" + string(b[4:])
}

// Code is s as a device code. Case and the hyphen do not matter, nor the
// letters people read for digits: O is 0, I and L are 1.
func Code(s string) (string, error) {
	c := strings.NewReplacer("-", "", "O", "0", "I", "1", "L", "1").Replace(strings.ToUpper(s))
	if len(c) != 8 || strings.Trim(c, crockford) != "" {
		return "", fmt.Errorf("%q is not a device code: it is 8 letters and digits, such as K7QM-4ZTR", s)
	}
	return c[:4] + "-" + c[4:], nil
}

// Secret is the join secret a worker that knew code joins the mesh of k's
// harness with. It derives from the code, so a new code refuses the old
// secret and no table holds it.
func Secret(k ed25519.PrivateKey, code string) string {
	m := hmac.New(sha256.New, k.Seed())
	m.Write([]byte("join " + code))
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(m.Sum(nil))
}

// pake binds the exchange to the harness key both sides expect, so a harness
// with another key derives another key even if it knew the code.
func pake(key string) *cpace.ContextInfo {
	return cpace.NewContextInfo("worker", "harness", []byte("mainplane join "+key))
}

func aead(isk []byte) (cipher.AEAD, error) {
	k, err := hkdf.Key(sha256.New, isk, nil, "mainplane join secret", 32)
	if err != nil {
		return nil, err
	}
	b, err := aes.NewCipher(k)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCMWithRandomNonce(b)
}

type joinMsg struct {
	A   []byte `json:"a,omitempty"`
	B   []byte `json:"b,omitempty"`
	Box []byte `json:"box,omitempty"`
}

// A CPace message is 32 bytes and some JSON; nothing longer is one.
const maxJoin = 1 << 10

// JoinHandler answers the device code exchange of the harness with k, whose
// code is code() now. Every exchange counts as a refusal in l, since the
// harness cannot tell a right code from a wrong one: so an address gets few
// guesses a window.
func JoinHandler(k ed25519.PrivateKey, key string, code func() (string, error), l *Limit) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a := Addr(r)
		if err := l.Wait(a); err != nil {
			http.Error(w, err.Error(), http.StatusTooManyRequests)
			return
		}
		l.Refused(a)
		var m joinMsg
		if err := json.NewDecoder(io.LimitReader(r.Body, maxJoin)).Decode(&m); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		c, err := code()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		b, isk, err := cpace.Exchange(c, pake(key), m.A)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		g, err := aead(isk)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(joinMsg{B: b, Box: g.Seal(nil, nil, []byte(Secret(k, c)), nil)})
	})
}

// Redeem trades code for the join token of the harness with key at url, or
// ErrCode when that harness holds another code or another key.
func Redeem(ctx context.Context, url, key, code string) (string, error) {
	a, s, err := cpace.Start(code, pake(key))
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(joinMsg{A: a})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url+"/join", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, maxJoin))
		return "", fmt.Errorf("%s/join: %s: %s", url, resp.Status, bytes.TrimSpace(b))
	}
	var m joinMsg
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxJoin)).Decode(&m); err != nil {
		return "", err
	}
	isk, err := s.Finish(m.B)
	if err != nil {
		return "", err
	}
	g, err := aead(isk)
	if err != nil {
		return "", err
	}
	secret, err := g.Open(nil, nil, m.Box, nil)
	if err != nil {
		return "", ErrCode
	}
	return Token(Join, key, string(secret)), nil
}
