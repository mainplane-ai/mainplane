package server

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/mainplane-ai/mainplane/pkg/statefile"
	"github.com/mainplane-ai/mainplane/pkg/version"
)

// The harness sends session records here only while the user has opted in to data sharing.
const dataURL = "https://data.mainplane.ai/v1/records"

const (
	shipEvery = 10 * time.Second
	postTime  = time.Minute
	maxBatch  = 8 << 20 // bytes of records in one post; a body larger than this goes as its header alone
)

var (
	credential = regexp.MustCompile(`sk-[\w-]{20,}|AKIA[0-9A-Z]{16}|gh[pousr]_\w{36}|github_pat_\w{22,}|xox[abpr]-[\w-]{10,}|AIza[\w-]{35}|mp_(key|join)_[\w.-]+|eyJ[\w-]+\.eyJ[\w-]+\.[\w-]+|-----BEGIN[A-Z ]*PRIVATE KEY-----[\s\S]*?-----END[A-Z ]*PRIVATE KEY-----`)
	scrubbed   = []byte("<scrubbed credential>")
)

// sent is sharing.json in the harness's directory: the install id, and how
// many bytes of each session's file are sent.
type sent struct {
	Install  string           `json:"install"`
	Sessions map[string]int64 `json:"sessions"`
}

type shipper struct {
	install string
	since   time.Time
	known   [][]byte
	config  string // the config's path, read again before each post
}

// ship sends the records appended to sessions in dir after the config at
// path said to share data, while it says so.
func ship(ctx context.Context, dir, path string) {
	if version.V == "dev" {
		return
	}
	last := ""
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(shipEvery):
		}
		c, ok := sharing(path)
		if !ok {
			continue
		}
		err := shipAll(ctx, dir, path, c)
		if err != nil && err.Error() != last {
			log.Printf("harness: data sharing: %v", err)
		}
		last = fmt.Sprint(err)
	}
}

// sharing is the config at path, and whether it allows data sharing.
func sharing(path string) (Config, bool) {
	c, err := Load(path)
	return c, err == nil && c.DataSharing != nil && c.DataSharing.Share && !doNotTrack()
}

func shipAll(ctx context.Context, dir, path string, c Config) error {
	file := filepath.Join(dir, "sharing.json")
	var s sent
	if b, err := os.ReadFile(file); err == nil {
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
	}
	fresh := s.Install == ""
	if fresh {
		s.Install = rand.Text()
	}
	sh := shipper{install: s.Install, since: c.DataSharing.At, config: path}
	for _, p := range c.Providers {
		if k := os.ExpandEnv(p.Key); len(k) >= 8 {
			sh.known = append(sh.known, []byte(k))
		}
	}
	if c.Tunnel != nil {
		sh.known = append(sh.known, []byte(c.Tunnel.Token))
	}
	paths, err := filepath.Glob(filepath.Join(dir, "sessions", "*.state"))
	if err != nil {
		return err
	}
	was := s.Sessions
	s.Sessions = map[string]int64{}
	for _, p := range paths {
		id := strings.TrimSuffix(filepath.Base(p), ".state")
		s.Sessions[id], err = sh.file(ctx, p, id, was[id])
		if err != nil {
			break
		}
	}
	if !fresh && maps.Equal(s.Sessions, was) {
		return err
	}
	b, _ := json.Marshal(s)
	if werr := os.WriteFile(file+".tmp", b, 0o600); werr != nil {
		return errors.Join(err, werr)
	}
	return errors.Join(err, os.Rename(file+".tmp", file))
}

// file sends the whole records of session id's file at path past byte from,
// and returns the byte it sent up to.
func (sh shipper) file(ctx context.Context, path, id string, from int64) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return from, err
	}
	defer func() { _ = f.Close() }()
	if fi, err := f.Stat(); err != nil || fi.Size() <= from {
		return from, err
	}
	if _, err := f.Seek(from, io.SeekStart); err != nil {
		return from, err
	}
	cr := &counter{r: f}
	br := bufio.NewReader(cr)
	var batch bytes.Buffer
	first, last, end, done := 0, 0, from, from
	for {
		r, err := statefile.Decode(br)
		if errors.Is(err, io.EOF) || errors.Is(err, statefile.ErrTorn) {
			break
		}
		if err != nil {
			return done, err
		}
		at := from + cr.n - int64(br.Buffered())
		if r.Time.Before(sh.since) {
			if batch.Len() == 0 {
				done = at
			}
			continue
		}
		switch {
		case len(r.Body) > maxBatch:
			r.Body = nil
		case !strings.HasPrefix(r.Type, "image/"):
			r.Body = scrub(r.Body, sh.known)
		}
		b, err := statefile.Encode(r)
		if err != nil {
			return done, err
		}
		if batch.Len() > 0 && batch.Len()+len(b) > maxBatch {
			if err := sh.post(ctx, id, first, last, batch.Bytes()); err != nil {
				return done, err
			}
			done = end
			batch.Reset()
		}
		if batch.Len() == 0 {
			first = r.N
		}
		batch.Write(b)
		last, end = r.N, at
	}
	if batch.Len() > 0 {
		if err := sh.post(ctx, id, first, last, batch.Bytes()); err != nil {
			return done, err
		}
		done = end
	}
	return done, nil
}

func (sh shipper) post(ctx context.Context, id string, first, last int, records []byte) error {
	if _, ok := sharing(sh.config); !ok {
		return errors.New("data sharing was turned off")
	}
	var z bytes.Buffer
	gz := gzip.NewWriter(&z)
	_, _ = gz.Write(records)
	_ = gz.Close()
	ctx, cancel := context.WithTimeout(ctx, postTime)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, dataURL, &z)
	if err != nil {
		return err
	}
	for k, v := range map[string]string{
		"Content-Type":      "application/gzip",
		"Mainplane-Install": sh.install,
		"Mainplane-Session": id,
		"Mainplane-Records": fmt.Sprintf("%d-%d", first, last),
		"Mainplane-Version": version.V,
		"Mainplane-OS":      runtime.GOOS + "/" + runtime.GOARCH,
	} {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%s: %s", dataURL, resp.Status)
	}
	return nil
}

func scrub(b []byte, known [][]byte) []byte {
	for _, k := range known {
		b = bytes.ReplaceAll(b, k, scrubbed)
	}
	return credential.ReplaceAll(b, scrubbed)
}

// counter counts the bytes read through it.
type counter struct {
	r io.Reader
	n int64
}

func (c *counter) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}
