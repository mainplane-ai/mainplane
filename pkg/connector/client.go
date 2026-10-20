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

	"github.com/mainplane-ai/mainplane/pkg/auth"
	"github.com/mainplane-ai/mainplane/pkg/harness"
	"github.com/mainplane-ai/mainplane/pkg/mesh"
	"github.com/mainplane-ai/mainplane/pkg/pointer"
	"github.com/mainplane-ai/mainplane/pkg/statefile"
	"github.com/mainplane-ai/mainplane/pkg/worker"
)

// Client is what login wrote: the harness's key, the URL it was last found
// at, and the api key's secret. On a worker of that harness, calls go to its
// node on the mesh, at Mesh.
type Client struct {
	URL     string `json:"url"`
	Key     string `json:"key"`
	Harness string `json:"harness"`
	Mesh    string `json:"-"`
}

// Path is the login file, ~/.mainplane/login.json.
func Path() (string, error) {
	h, err := os.UserHomeDir()
	return filepath.Join(h, ".mainplane", "login.json"), err
}

// Login finds the harness of an api key, checks that it takes the key, and
// saves both. It returns the client, the login it replaced (empty for none),
// and the release the harness runs.
func Login(ctx context.Context, token string) (c Client, old Client, version string, err error) {
	h, key, err := auth.Parse(auth.Key, token)
	if err != nil {
		return c, old, "", err
	}
	u, err := pointer.Find(ctx, h, "")
	if err != nil {
		return c, old, "", unreachable(err)
	}
	c = Client{URL: u, Key: key, Harness: h}
	if version, err = c.Version(ctx); err != nil {
		return c, old, "", err
	}
	// a login file that does not read is written anew
	if p, err := Path(); err == nil {
		if b, err := os.ReadFile(p); err == nil {
			_ = json.Unmarshal(b, &old)
		}
	}
	return c, old, version, c.Save()
}

// Load is what login wrote, with the harness on the mesh when the worker on
// this machine follows the same harness key, else proved at its URL, or found
// again through the pointer and saved.
func Load(ctx context.Context) (Client, error) {
	var c Client
	p, err := Path()
	if err != nil {
		return c, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, err
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
	u, err := pointer.Find(ctx, c.Harness, c.URL)
	if err != nil || u == c.URL {
		return unreachable(err)
	}
	c.URL = u
	return c.Save()
}

func (c Client) Save() error {
	b, err := json.Marshal(c)
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
// host: most often this machine is offline, and the bare dial error reads
// as a fault of Mainplane.
func unreachable(err error) error {
	var u *url.Error
	var op *net.OpError
	if !errors.As(err, &u) || !errors.As(err, &op) {
		return err
	}
	host := u.URL
	if p, perr := url.Parse(u.URL); perr == nil {
		host = p.Host
	}
	return fmt.Errorf("cannot reach %s: is this machine offline? (%w)", host, op)
}

// Do does one request; a status outside 2xx is an error in the harness's own
// words.
func (c Client) Do(ctx context.Context, method, path, ctype string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, cmp.Or(c.Mesh, c.URL)+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Key)
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, unreachable(err)
	}
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, fmt.Errorf("%s %s: %d %s", method, path, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return resp, nil
}

// Call does one request, and decodes a JSON reply into out when out is given.
func (c Client) Call(ctx context.Context, method, path, ctype string, body io.Reader, out any) error {
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
func (c Client) Version(ctx context.Context) (string, error) {
	var out struct{ Version string }
	err := c.Call(ctx, "GET", "/", "", nil, &out)
	return out.Version, err
}

// Create makes a session and returns its id.
func (c Client) Create(ctx context.Context, cr harness.Create, via string) (string, error) {
	b, err := json.Marshal(cr)
	if err != nil {
		return "", err
	}
	var out struct{ ID string }
	err = c.Call(ctx, "POST", "/sessions?via="+url.QueryEscape(via), "application/json", bytes.NewReader(b), &out)
	return out.ID, err
}

// Sessions lists sessions, newest updated first.
func (c Client) Sessions(ctx context.Context, status string, limit int) ([]harness.Info, error) {
	var infos []harness.Info
	err := c.Call(ctx, "GET", "/sessions?status="+url.QueryEscape(status)+"&limit="+strconv.Itoa(limit), "", nil, &infos)
	return infos, err
}

func (c Client) Info(ctx context.Context, id string) (harness.Info, error) {
	var i harness.Info
	err := c.Call(ctx, "GET", "/sessions/"+id, "", nil, &i)
	return i, err
}

func (c Client) Stop(ctx context.Context, id, via string) error {
	return c.Call(ctx, "POST", "/sessions/"+id+"/stop?via="+url.QueryEscape(via), "", nil, nil)
}

// Post appends text and files as one turn: a single text/plain body, or one
// multipart part per piece, each typed by its extension. A file with no type
// is refused, not guessed. Returns the last record's n.
func (c Client) Post(ctx context.Context, id, via, text string, files []string) (int, error) {
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
func (c Client) Records(ctx context.Context, id string, after int, wait string) ([]statefile.Record, error) {
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

// Worker is a worker as GET /workers lists it: connected, unless refused.
type Worker struct {
	Name    string `json:"name"`
	Refused string `json:"refused,omitempty"`
}

// Workers lists the workers the harness knows.
func (c Client) Workers(ctx context.Context) ([]Worker, error) {
	var ws []Worker
	err := c.Call(ctx, "GET", "/workers", "", nil, &ws)
	return ws, err
}

// SetName names the session; an empty name removes it.
func (c Client) SetName(ctx context.Context, id, via, name string) error {
	return c.Call(ctx, "PUT", "/sessions/"+id+"/name?via="+url.QueryEscape(via), "text/plain; charset=utf-8", strings.NewReader(name), nil)
}

// Delete removes the session for good.
func (c Client) Delete(ctx context.Context, id, via string) error {
	return c.Call(ctx, "DELETE", "/sessions/"+id+"?via="+url.QueryEscape(via), "", nil, nil)
}

// SetWorkers makes ws the session's workers from its next step on.
func (c Client) SetWorkers(ctx context.Context, id, via string, ws []statefile.Worker) error {
	b, err := json.Marshal(ws)
	if err != nil {
		return err
	}
	return c.Call(ctx, "PUT", "/sessions/"+id+"/workers?via="+url.QueryEscape(via), "application/json", bytes.NewReader(b), nil)
}

// Providers lists the providers the harness serves, by the name a model
// starts with.
func (c Client) Providers(ctx context.Context) ([]harness.ProviderInfo, error) {
	var ps []harness.ProviderInfo
	err := c.Call(ctx, "GET", "/providers", "", nil, &ps)
	return ps, err
}

// SetProvider makes the harness serve provider name with this shape, url,
// region and key, at once.
func (c Client) SetProvider(ctx context.Context, name, shape, u, region, key string) error {
	b, err := json.Marshal(map[string]string{"shape": shape, "url": u, "region": region, "key": key})
	if err != nil {
		return err
	}
	return c.Call(ctx, "PUT", "/providers/"+url.PathEscape(name), "application/json", bytes.NewReader(b), nil)
}
