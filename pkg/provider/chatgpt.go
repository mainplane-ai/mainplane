package provider

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	neturl "net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// ChatGPT plan usage through Sign in with ChatGPT, OpenAI's flow for
// open-source clients: https://developers.openai.com/siwc/token-sharing-open-source.
// The first login registers a client of the user's own, named agentName, and
// later logins reuse its client id. Requests are the openai envelope's, with
// the login's access token as the key.
const (
	issuer      = "https://auth.openai.com"
	tokenURL    = issuer + "/api/accounts/oauth/token"
	resource    = "https://api.openai.com/v1" // the audience of the token, and where requests go
	planScope   = "chatgpt.tokens.use.direct" // the grant to use the plan; a login without it can sign in but not infer
	scopes      = "openid profile email offline_access resource.invoke " + planScope
	agentName   = "Mainplane"
	register    = "dynamic_agent_client" // the client id of a first login, never saved
	credentials = "chatgpt.json"
	// The browser lands on the loopback callback, which the harness, maybe
	// remote, does not serve: the user pastes the address it shows. Only the
	// port may differ between logins; 1455 is the docs' and Codex's.
	callback = "http://127.0.0.1:1455/auth/callback"
	// Access tokens last an hour. The token response's earliest_refresh_at
	// fell about 6 minutes before expiry, so a refresh this early is allowed
	// and leaves a step time to send.
	refreshEarly = 5 * time.Minute
	// tokenTime bounds a token call, which holds refreshMu.
	tokenTime = 30 * time.Second
)

// refreshMu serializes refreshes: a refresh rotates the refresh token, and the
// harness rebuilds providers on every config change, so the lock is not one
// provider's.
var refreshMu sync.Mutex

// chatgptCreds is the login's record, as the docs lay it out.
type chatgptCreds struct {
	Email        string    `json:"email"`
	Subject      string    `json:"subject"`
	ClientID     string    `json:"client_id"`
	HostID       string    `json:"ext_agent_host_id"`
	IDToken      string    `json:"id_token"`
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	Scope        string    `json:"scope"`
	Expires      time.Time `json:"expires"`
}

// tokenResponse is the token endpoint's answer to a code or a refresh.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	Scope        string `json:"scope"`
	ExpiresIn    int    `json:"expires_in"`
}

func ChatGPT(url, dir string) Provider {
	return Provider{
		URL:      base(url, resource) + "/responses",
		Envelope: openai{},
		Headers:  bearer,
		Token:    func(ctx context.Context) (string, error) { return chatgptToken(ctx, dir) },
		// The prompt cache hit every time with it and about half the time
		// with prompt_cache_key alone; Codex and opencode send it.
		Session: "session-id",
	}
}

// chatgptToken is the access token, refreshed first when it expires soon.
func chatgptToken(ctx context.Context, dir string) (string, error) {
	refreshMu.Lock()
	defer refreshMu.Unlock()
	c, err := loadCreds(dir)
	if errors.Is(err, fs.ErrNotExist) || err == nil && c.RefreshToken == "" {
		return "", configError{errors.New("chatgpt: not signed in: mainplane signin <provider>")}
	}
	if err != nil {
		return "", configError{err}
	}
	if time.Until(c.Expires) > refreshEarly {
		return c.AccessToken, nil
	}
	t, err := postToken(ctx, neturl.Values{"grant_type": {"refresh_token"}, "client_id": {c.ClientID}, "refresh_token": {c.RefreshToken}, "resource": {resource}})
	if err != nil {
		return "", err
	}
	c.AccessToken, c.RefreshToken, c.Scope, c.Expires = t.AccessToken, t.RefreshToken, t.Scope, time.Now().Add(time.Duration(t.ExpiresIn)*time.Second)
	if t.IDToken != "" {
		c.IDToken = t.IDToken
	}
	return c.AccessToken, saveCreds(dir, c)
}

// postToken calls the token endpoint. A refusal is an httpError, so a 400
// such as invalid_grant is not retried and a 5xx is.
func postToken(ctx context.Context, form neturl.Values) (tokenResponse, error) {
	var t tokenResponse
	ctx, cancel := context.WithTimeout(ctx, tokenTime)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return t, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return t, err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return t, err
	}
	if resp.StatusCode != http.StatusOK {
		return t, &httpError{name: "chatgpt token", status: resp.Status, code: resp.StatusCode, body: b, after: retryAfter(resp.Header)}
	}
	if err := json.Unmarshal(b, &t); err != nil {
		return t, err
	}
	if t.AccessToken == "" || t.RefreshToken == "" {
		return t, fmt.Errorf("chatgpt token: no access or refresh token in the response")
	}
	return t, nil
}

// chatgptLogin signs in: a first login registers the client, a later one
// reuses its client id and the host id. The user opens the URL, approves, and
// pastes the address of the page the browser lands on.
func chatgptLogin(ctx context.Context, dir string, show func(Prompt), paste <-chan string) error {
	c, err := hostCreds(dir)
	if err != nil {
		return err
	}
	verifier, state, nonce := random(), random(), random()
	sum := sha256.Sum256([]byte(verifier))
	q := neturl.Values{
		"client_id":             {register},
		"ext_agent_host_id":     {c.HostID},
		"response_type":         {"code"},
		"redirect_uri":          {callback},
		"scope":                 {scopes},
		"resource":              {resource},
		"state":                 {state},
		"nonce":                 {nonce},
		"code_challenge_method": {"S256"},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(sum[:])},
	}
	if c.ClientID == "" {
		q.Set("agent_name_hint", agentName)
	} else {
		q.Set("client_id", c.ClientID)
		q.Set("login_hint", c.Email)
	}
	show(Prompt{
		Text:  "Open the URL and sign in with ChatGPT. The browser then shows a page on 127.0.0.1 that does not load: paste its full address.",
		URL:   issuer + "/api/accounts/authorize?" + q.Encode(),
		Paste: true,
	})
	var pasted string
	select {
	case pasted = <-paste:
	case <-ctx.Done():
		return ctx.Err()
	}
	u, err := neturl.Parse(strings.TrimSpace(pasted))
	if err != nil {
		return fmt.Errorf("chatgpt: the pasted text is not the address: %w", err)
	}
	r := u.Query()
	if e := r.Get("error"); e != "" {
		return fmt.Errorf("chatgpt: %s: %s", e, r.Get("error_description"))
	}
	if r.Get("state") != state || r.Get("code") == "" {
		return errors.New("chatgpt: the pasted address is not this login's callback: start again")
	}
	id := r.Get("client_id")
	switch {
	case c.ClientID != "" && id != "" && id != c.ClientID:
		return fmt.Errorf("chatgpt: the login returned client %s, not this provider's %s", id, c.ClientID)
	case c.ClientID != "":
		id = c.ClientID
	case id == "":
		return errors.New("chatgpt: the callback has no client_id: the registration did not complete")
	}
	t, err := postToken(ctx, neturl.Values{"grant_type": {"authorization_code"}, "client_id": {id}, "code": {r.Get("code")}, "code_verifier": {verifier}, "redirect_uri": {callback}, "resource": {resource}})
	if err != nil {
		return err
	}
	if !slices.Contains(strings.Fields(t.Scope), planScope) {
		return errors.New("chatgpt: signed in, but the plan usage permission was not granted: log in again and allow it")
	}
	claims, err := idClaims(t.IDToken, id, nonce)
	if err != nil {
		return err
	}
	if c.Subject != "" && claims.Sub != c.Subject {
		return errors.New("chatgpt: signed in as another account than this provider's")
	}
	c.Email, c.Subject, c.ClientID, c.IDToken = claims.Email, claims.Sub, id, t.IDToken
	c.AccessToken, c.RefreshToken, c.Scope, c.Expires = t.AccessToken, t.RefreshToken, t.Scope, time.Now().Add(time.Duration(t.ExpiresIn)*time.Second)
	refreshMu.Lock()
	defer refreshMu.Unlock()
	return saveCreds(dir, c)
}

// hostCreds is the login's record so far, with a host id. The docs: persist
// the host id before its first sign-in.
func hostCreds(dir string) (chatgptCreds, error) {
	refreshMu.Lock()
	defer refreshMu.Unlock()
	c, err := loadCreds(dir)
	if errors.Is(err, fs.ErrNotExist) {
		c, err = chatgptCreds{HostID: "urn:uuid:" + uuid()}, nil
	}
	if err != nil {
		return c, err
	}
	return c, saveCreds(dir, c)
}

type claims struct {
	Iss   string          `json:"iss"`
	Aud   json.RawMessage `json:"aud"` // a string or a list
	Exp   int64           `json:"exp"`
	Nonce string          `json:"nonce"`
	Sub   string          `json:"sub"`
	Email string          `json:"email"`
}

// idClaims checks the ID token's issuer, audience, expiry and nonce. Its
// signature goes unchecked: the token came straight from the token endpoint
// over TLS, which OpenID Connect Core 3.1.3.7 accepts in place of it.
func idClaims(token, client, nonce string) (claims, error) {
	var c claims
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return c, errors.New("chatgpt: the ID token is not a JWT")
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return c, fmt.Errorf("chatgpt: ID token: %w", err)
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("chatgpt: ID token: %w", err)
	}
	var aud []string
	if json.Unmarshal(c.Aud, &aud) != nil {
		aud = []string{strings.Trim(string(c.Aud), `"`)}
	}
	switch {
	case c.Iss != issuer, !slices.Contains(aud, client), time.Now().Unix() > c.Exp, c.Nonce != nonce, c.Sub == "":
		return c, fmt.Errorf("chatgpt: the ID token does not match this login: iss %s, aud %s, nonce match %t", c.Iss, c.Aud, c.Nonce == nonce)
	}
	return c, nil
}

func loadCreds(dir string) (chatgptCreds, error) {
	var c chatgptCreds
	b, err := os.ReadFile(filepath.Join(dir, credentials))
	if err != nil {
		return c, err
	}
	err = json.Unmarshal(b, &c)
	return c, err
}

// saveCreds replaces the file whole, so a crash leaves the old tokens or the
// new, never half.
func saveCreds(dir string, c chatgptCreds) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, credentials+".tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, credentials))
}

// random is a fresh state, nonce or PKCE verifier: 32 bytes, 43 characters.
func random() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b) // never fails, as crypto/rand documents
	return base64.RawURLEncoding.EncodeToString(b)
}

// uuid is a random UUID (version 4), the host id's form the docs name.
func uuid() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6], b[8] = b[6]&0x0f|0x40, b[8]&0x3f|0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}
