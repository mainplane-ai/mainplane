// Package connector is the Go client of the harness HTTP API, for every
// connector that runs in Go: the CLI and the TUI ship in the device binary
// and call the harness only through it.
package connector

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mainplane-ai/mainplane/pkg/auth"
	"github.com/mainplane-ai/mainplane/pkg/harness"
	"github.com/mainplane-ai/mainplane/pkg/mesh"
	"github.com/mainplane-ai/mainplane/pkg/pointer"
	"github.com/mainplane-ai/mainplane/pkg/provider"
	"github.com/mainplane-ai/mainplane/pkg/statefile"
	"github.com/mainplane-ai/mainplane/pkg/worker"
)

// Client is what login wrote: the harness's key, the URL it was last found
// at, none when login found it on this machine, and the api key's secret. On
// the harness's own machine, calls go to it on loopback, at Local; on a
// worker of that harness, to its node on the mesh, at Mesh. A call that
// fails at URL finds the harness again, so one Client is shared, not copied.
type Client struct {
	URL     string     `json:"url"`
	Key     string     `json:"key"`
	Harness string     `json:"harness"`
	Local   string     `json:"-"`
	Mesh    string     `json:"-"`
	mu      sync.Mutex // guards URL, which a failed call may find again
}

// Path is the login file, ~/.mainplane/login.json.
func Path() (string, error) {
	h, err := os.UserHomeDir()
	return filepath.Join(h, ".mainplane", "login.json"), err
}

// Login finds the harness of an api key, on this machine or through the
// pointer, checks that it takes the key, and saves both. It returns the client, the login it replaced (empty for none),
// and the release the harness runs.
func Login(ctx context.Context, token string) (c *Client, old *Client, version string, err error) {
	h, key, err := auth.Parse(auth.Key, token)
	if err != nil {
		return c, old, "", err
	}
	c, old = &Client{Key: key, Harness: h, Local: pointer.Local(ctx, h)}, &Client{}
	if c.Local == "" {
		if c.URL, err = pointer.Find(ctx, h, ""); err != nil {
			return c, old, "", unreachable(err)
		}
	}
	if version, err = c.Version(ctx); err != nil {
		return c, old, "", err
	}
	// a login file that does not read is written anew
	if p, err := Path(); err == nil {
		if b, err := os.ReadFile(p); err == nil {
			_ = json.Unmarshal(b, old)
		}
	}
	return c, old, version, c.Save()
}

// Load is what login wrote, with the harness on loopback when it is the one
// installed on this machine, on the mesh when the worker on this machine
// follows the same harness key, else proved at its URL, or found again
// through the pointer and saved.
func Load(ctx context.Context) (*Client, error) {
	c := &Client{}
	p, err := Path()
	if err != nil {
		return c, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, c); err != nil {
		return c, err
	}
	if c.Local = pointer.Local(ctx, c.Harness); c.Local != "" {
		return c, nil
	}
	if k, a, err := mesh.Harness(worker.Harness); err == nil && k == c.Harness {
		c.Mesh = "http://" + net.JoinHostPort(a.String(), strconv.Itoa(worker.APIPort))
		return c, nil
	}
	return c, c.Find(ctx)
}

// Find proves the harness at its URL, or finds it again through the pointer
// and saves it.
func (c *Client) Find(ctx context.Context) error {
	if moved, err := c.find(ctx); !moved {
		return err
	}
	return c.Save()
}

// find proves the harness at its URL, or finds it again through the pointer,
// and says whether it moved. When the pointer answers, this machine is
// online and the harness is not where it last said: a saved login can
// outlive its harness, as a quick tunnel's does.
func (c *Client) find(ctx context.Context) (bool, error) {
	at := c.At()
	u, err := pointer.Find(ctx, c.Harness, at)
	if err != nil && u != "" {
		return false, fmt.Errorf("the harness of this login is not at %s, where it last was: it is down, or gone; to log in to another: mainplane login <api key> (%w)", u, err)
	}
	if err != nil || u == at {
		return false, unreachable(err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.URL = u
	return true, nil
}

func (c *Client) Save() error {
	c.mu.Lock()
	b, err := json.Marshal(c)
	c.mu.Unlock()
	if err != nil {
		return err
	}
	p, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o600)
}

// unreachable says plainly that a request found no network path to its
// host: most often this machine is offline, or on loopback the harness is
// stopped, and the bare dial error reads as a fault of Mainplane.
func unreachable(err error) error {
	var u *url.Error
	var op *net.OpError
	if !errors.As(err, &u) || !errors.As(err, &op) {
		return err
	}
	host := u.URL
	if p, perr := url.Parse(u.URL); perr == nil {
		host = p.Host
		if net.ParseIP(p.Hostname()).IsLoopback() {
			return fmt.Errorf("the harness on this machine does not answer at %s: is mainplane-server running? (%w)", host, op)
		}
	}
	return fmt.Errorf("cannot reach %s: is this machine offline? (%w)", host, op)
}

// Do does one request; a status outside 2xx is an error in the harness's own
// words.
func (c *Client) Do(ctx context.Context, method, path, ctype string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, cmp.Or(c.Local, c.Mesh, c.At())+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Key)
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	resp, err := http.DefaultClient.Do(req)
	// Cloudflare answers 502, then 530, for a quick tunnel whose harness is
	// down, and a harness down for long comes back at a new URL: find it, so
	// the next call goes there. The call's own failure is what it returns.
	// Unsaved: a login saves only once its key works, and Load finds it again.
	if c.Local == "" && c.Mesh == "" && (err != nil || resp.StatusCode >= 500) {
		_, _ = c.find(ctx)
	}
	if err != nil {
		return nil, unreachable(err)
	}
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, fmt.Errorf("%s %s: %w", method, path, &StatusError{resp.StatusCode, strings.TrimSpace(string(b))})
	}
	return resp, nil
}

// At is the URL the harness was last found at.
func (c *Client) At() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.URL
}

// StatusError is a reply outside 2xx, in the harness's own words.
type StatusError struct {
	Code int
	Text string
}

func (e *StatusError) Error() string { return fmt.Sprintf("%d %s", e.Code, e.Text) }

// Call does one request, and decodes a JSON reply into out when out is given.
func (c *Client) Call(ctx context.Context, method, path, ctype string, body io.Reader, out any) error {
	resp, err := c.Do(ctx, method, path, ctype, body)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// Version is the release the harness runs.
func (c *Client) Version(ctx context.Context) (string, error) {
	var out struct{ Version string }
	err := c.Call(ctx, "GET", "/", "", nil, &out)
	return out.Version, err
}

// Create makes a session and returns its id.
func (c *Client) Create(ctx context.Context, cr harness.Create, via string) (string, error) {
	b, err := json.Marshal(cr)
	if err != nil {
		return "", err
	}
	var out struct{ ID string }
	err = c.Call(ctx, "POST", "/sessions?via="+url.QueryEscape(via), "application/json", bytes.NewReader(b), &out)
	return out.ID, err
}

// Sessions lists sessions, newest updated first.
func (c *Client) Sessions(ctx context.Context, status string, limit int) ([]harness.Info, error) {
	var infos []harness.Info
	err := c.Call(ctx, "GET", "/sessions?status="+url.QueryEscape(status)+"&limit="+strconv.Itoa(limit), "", nil, &infos)
	return infos, err
}

func (c *Client) Info(ctx context.Context, id string) (harness.Info, error) {
	var i harness.Info
	err := c.Call(ctx, "GET", "/sessions/"+id, "", nil, &i)
	return i, err
}

func (c *Client) Stop(ctx context.Context, id, via string) error {
	return c.Call(ctx, "POST", "/sessions/"+id+"/stop?via="+url.QueryEscape(via), "", nil, nil)
}

// Post appends text and files as one turn: a single text/plain body, or one
// multipart part per piece, each typed by its extension. A file with no type
// is refused, not guessed. Returns the last record's n.
func (c *Client) Post(ctx context.Context, id, via, text string, files []string) (int, error) {
	var out struct{ N int }
	path := "/sessions/" + id + "/records?via=" + url.QueryEscape(via)
	if len(files) == 0 {
		err := c.Call(ctx, "POST", path, "text/plain", strings.NewReader(text), &out)
		return out.N, err
	}
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	w, err := mw.CreatePart(textproto.MIMEHeader{"Content-Type": {"text/plain"}})
	if err != nil {
		return 0, err
	}
	_, _ = w.Write([]byte(text))
	for _, f := range files {
		t := mime.TypeByExtension(filepath.Ext(f))
		if t == "" {
			return 0, fmt.Errorf("%s: no content type for %q", f, filepath.Ext(f))
		}
		data, err := os.ReadFile(f)
		if err != nil {
			return 0, err
		}
		w, err := mw.CreatePart(textproto.MIMEHeader{"Content-Type": {t}})
		if err != nil {
			return 0, err
		}
		_, _ = w.Write(data)
	}
	_ = mw.Close()
	err = c.Call(ctx, "POST", path, mw.FormDataContentType(), &b, &out)
	return out.N, err
}

// Records is the file after record n; with wait, a long poll.
func (c *Client) Records(ctx context.Context, id string, after int, wait string) ([]statefile.Record, error) {
	resp, err := c.Do(ctx, "GET", "/sessions/"+id+"/records?after="+strconv.Itoa(after)+"&wait="+wait, "", nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	br := bufio.NewReader(resp.Body)
	var recs []statefile.Record
	for {
		r, err := statefile.Decode(br)
		if errors.Is(err, io.EOF) {
			return recs, nil
		}
		if err != nil {
			return recs, err
		}
		recs = append(recs, r)
	}
}

// Worker is a worker as GET /workers lists it: connected, unless refused,
// and whether it can serve drives.
type Worker struct {
	Name        string `json:"name"`
	Refused     string `json:"refused,omitempty"`
	DriveServer bool   `json:"drive_server,omitempty"`
}

// Workers lists the workers the harness knows.
func (c *Client) Workers(ctx context.Context) ([]Worker, error) {
	var ws []Worker
	err := c.Call(ctx, "GET", "/workers", "", nil, &ws)
	return ws, err
}

// SetName names the session; an empty name removes it.
func (c *Client) SetName(ctx context.Context, id, via, name string) error {
	return c.Call(ctx, "PUT", "/sessions/"+id+"/name?via="+url.QueryEscape(via), "text/plain; charset=utf-8", strings.NewReader(name), nil)
}

// Delete removes the session for good.
func (c *Client) Delete(ctx context.Context, id, via string) error {
	return c.Call(ctx, "DELETE", "/sessions/"+id+"?via="+url.QueryEscape(via), "", nil, nil)
}

// SetWorkers makes ws the session's workers from its next step on.
func (c *Client) SetWorkers(ctx context.Context, id, via string, ws []statefile.Worker) error {
	b, err := json.Marshal(ws)
	if err != nil {
		return err
	}
	return c.Call(ctx, "PUT", "/sessions/"+id+"/workers?via="+url.QueryEscape(via), "application/json", bytes.NewReader(b), nil)
}

// Providers lists the providers the harness serves, by the name a model
// starts with.
func (c *Client) Providers(ctx context.Context) ([]harness.ProviderInfo, error) {
	var ps []harness.ProviderInfo
	err := c.Call(ctx, "GET", "/providers", "", nil, &ps)
	return ps, err
}

// loginPath is the route of provider name's login.
func loginPath(name string) string { return "/providers/" + url.PathEscape(name) + "/login" }

// signinPoll is how often a login is asked how it stands.
const signinPoll = 2 * time.Second

// SignIn starts provider name's login on the harness, with the shape and url
// for a provider it does not have, and returns its first prompt, or its end.
// The prompt may wait on a download, such as a provider's binary.
func (c *Client) SignIn(ctx context.Context, name, shape, u string) (provider.LoginState, error) {
	var st provider.LoginState
	b, err := json.Marshal(map[string]string{"shape": shape, "url": u})
	if err != nil {
		return st, err
	}
	if err := c.Call(ctx, "POST", loginPath(name), "application/json", bytes.NewReader(b), &st); err != nil {
		return st, err
	}
	st, err = c.signinPoll(ctx, name, st, func(st provider.LoginState) bool { return st.URL != "" })
	if err == nil && st.State == "failed" {
		err = errors.New(st.Error)
	}
	return st, err
}

// SignInPaste hands the login what its prompt asked the user to paste.
func (c *Client) SignInPaste(ctx context.Context, name, text string) error {
	b, err := json.Marshal(map[string]string{"code": text})
	if err != nil {
		return err
	}
	return c.Call(ctx, "POST", loginPath(name)+"/code", "application/json", bytes.NewReader(b), nil)
}

// SignInWait waits for the login to end, and is an error unless it signed in.
func (c *Client) SignInWait(ctx context.Context, name string) error {
	st, err := c.signinPoll(ctx, name, provider.LoginState{State: "pending"}, func(provider.LoginState) bool { return false })
	if err == nil && st.State != "done" {
		err = errors.New(st.Error)
	}
	return err
}

// signinPoll asks for the login's state until it is no longer pending or
// stop holds.
func (c *Client) signinPoll(ctx context.Context, name string, st provider.LoginState, stop func(provider.LoginState) bool) (provider.LoginState, error) {
	for st.State == "pending" && !stop(st) {
		select {
		case <-time.After(signinPoll):
		case <-ctx.Done():
			return st, ctx.Err()
		}
		st = provider.LoginState{}
		if err := c.Call(ctx, "GET", loginPath(name), "", nil, &st); err != nil {
			return st, err
		}
	}
	return st, nil
}

// SetProvider makes the harness serve provider name with this shape, url,
// region, session header and key, at once.
func (c *Client) SetProvider(ctx context.Context, name, shape, u, region, session, key string) error {
	b, err := json.Marshal(map[string]string{"shape": shape, "url": u, "region": region, "session": session, "key": key})
	if err != nil {
		return err
	}
	return c.Call(ctx, "PUT", "/providers/"+url.PathEscape(name), "application/json", bytes.NewReader(b), nil)
}

// Network is the state of the harness's tunnel: starting, ready, or its error.
func (c *Client) Network(ctx context.Context) (string, error) {
	var out struct{ Network string }
	err := c.Call(ctx, "GET", "/", "", nil, &out)
	return out.Network, err
}

// Join is the network name and device code that make a machine a worker.
func (c *Client) Join(ctx context.Context) (string, error) {
	var out struct{ Join string }
	err := c.Call(ctx, "GET", "/join", "", nil, &out)
	return out.Join, err
}

// NewKey mints an api key named name and returns its token. A name in use is
// a StatusError of 409.
func (c *Client) NewKey(ctx context.Context, name string) (string, error) {
	b, err := json.Marshal(map[string]string{"name": name})
	if err != nil {
		return "", err
	}
	var out struct{ Key string }
	err = c.Call(ctx, "POST", "/keys", "application/json", bytes.NewReader(b), &out)
	return out.Key, err
}

// SetDrive makes or changes the drive name, served by server to workers, "*"
// for all.
func (c *Client) SetDrive(ctx context.Context, name, server string, workers []string) error {
	b, err := json.Marshal(map[string]any{"server": server, "workers": workers})
	if err != nil {
		return err
	}
	return c.Call(ctx, "PUT", "/drives/"+url.PathEscape(name), "application/json", bytes.NewReader(b), nil)
}
