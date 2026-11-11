package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base32"
	"strings"
)

// Two credentials the harness signs and keeps no table of: the HMAC of the
// subject under the harness key's seed is the whole check. A session key
// lets a session's environments reach its agent's apps, and stops working
// when the session is no longer its agent's live one; an app key lets a
// daemon app call the routes its entitlements name.
//
//	mp_skey_<harness key>.<session id>:<hmac>
//	mp_akey_<harness key>.<app name>:<hmac>
const (
	SessionKey = "skey"
	AppKey     = "akey"
)

// Signer makes and checks signed tokens of one harness.
type Signer struct {
	Seed    []byte // the harness key's seed
	Harness string // the harness key as a token carries it
}

func (s Signer) Sign(kind, subject string) string {
	return Token(kind, s.Harness, subject+":"+s.mac(kind, subject))
}

// Verify is the subject of token, when it is a token of kind this harness signed.
func (s Signer) Verify(kind, token string) (string, bool) {
	key, secret, err := Parse(kind, token)
	i := strings.LastIndexByte(secret, ':')
	if err != nil || i <= 0 || key != s.Harness || len(s.Seed) == 0 {
		return "", false
	}
	subject := secret[:i]
	return subject, hmac.Equal([]byte(secret[i+1:]), []byte(s.mac(kind, subject)))
}

func (s Signer) mac(kind, subject string) string {
	m := hmac.New(sha256.New, s.Seed)
	m.Write([]byte(kind + " " + subject))
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(m.Sum(nil))
}

// Caller is who a request's credential names: Key with no subject for an
// api key, else the kind of signed token and its subject.
type Caller struct {
	Kind    string
	Subject string
}

type callerKey struct{}

func WithCaller(ctx context.Context, c Caller) context.Context {
	return context.WithValue(ctx, callerKey{}, c)
}

// CallerOf is the caller Bearer set; a context it never saw is an api key's,
// as a handler mounted behind no Bearer is the harness's own.
func CallerOf(ctx context.Context) Caller {
	if c, ok := ctx.Value(callerKey{}).(Caller); ok {
		return c
	}
	return Caller{Kind: Key}
}
