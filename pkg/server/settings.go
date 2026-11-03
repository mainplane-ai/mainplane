package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/mainplane-ai/mainplane/pkg/release"
	"github.com/mainplane-ai/mainplane/pkg/version"
	"github.com/mainplane-ai/mainplane/pkg/worker"
)

// DataSharing is the answer to data sharing permission: which, when, through
// which connector, and to which words.
type DataSharing struct {
	Share bool      `json:"share"`
	At    time.Time `json:"at,omitzero"`
	Via   string    `json:"via,omitempty"`
	Text  string    `json:"text,omitempty"`
}

type settings struct {
	Version     *versions    `json:"version,omitempty"`
	DataSharing *DataSharing `json:"data_sharing,omitempty"`
	Prompt      *Prompt      `json:"prompt,omitempty"`
}

// Prompt is the lines new sessions get after the system prompt's mechanics,
// and whether they are the release's default. Set, default drops the
// config's own lines for the default.
type Prompt struct {
	Lines   []string `json:"lines"`
	Default bool     `json:"default,omitempty"`
}

type versions struct {
	Current string `json:"current"`
	Latest  string `json:"latest,omitempty"`
}

// An update downloads one binary and restarts the harness.
const updateTime = 5 * time.Minute

// The default prompt is about 3 KiB; a user's may be a few times that.
const maxSettings = 64 << 10

// doNotTrack is DO_NOT_TRACK set to anything but 0 or false.
func doNotTrack() bool {
	v := os.Getenv("DO_NOT_TRACK")
	return v != "" && v != "0" && v != "false"
}

func (a *api) settings(w http.ResponseWriter, r *http.Request) {
	c, err := loadConfig(a.path)
	if err != nil {
		fail(w, err)
		return
	}
	// latest is optional: wait for it no longer than a connector waits
	latest, l := "", make(chan string, 1)
	go func() { v, _ := release.Latest(); l <- v }()
	select {
	case latest = <-l:
	case <-time.After(5 * time.Second):
	}
	u := c.DataSharing
	if doNotTrack() {
		u = &DataSharing{Via: "DO_NOT_TRACK"}
	}
	reply(w, settings{&versions{version.V, latest}, u, &Prompt{c.prompt(), c.Prompt == nil}})
}

func (a *api) setSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		settings
		Version string `json:"version"`
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, maxSettings))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		fail(w, fmt.Errorf(`the body is {"data_sharing":{"share","via","text"}}, {"prompt":{"lines"}}, {"prompt":{"default":true}} or {"version"}: %w`, err))
		return
	}
	if u := in.DataSharing; u != nil {
		if u.Share && doNotTrack() {
			http.Error(w, "DO_NOT_TRACK is set", http.StatusConflict)
			return
		}
		u.At = time.Now().UTC()
		if err := a.save(func(c *Config) { c.DataSharing = u }); err != nil {
			fail(w, err)
			return
		}
	}
	if p := in.Prompt; p != nil {
		if err := checkPrompt(*p); err != nil {
			fail(w, err)
			return
		}
		if err := a.save(func(c *Config) { c.Prompt = p.Lines }); err != nil {
			fail(w, err)
			return
		}
	}
	if in.Version != "" {
		// not 502, which a connector reads as the proxy losing the harness
		// to its restart
		if err := a.update(r.Context(), in.Version); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// checkPrompt takes lines, one line each, or the default with none.
func checkPrompt(p Prompt) error {
	if p.Default != (p.Lines == nil) {
		return errors.New(`a prompt is {"lines":[...]} or {"default":true}`)
	}
	for _, l := range p.Lines {
		if strings.ContainsAny(l, "\r\n") {
			return fmt.Errorf("prompt line %q has a line break: give each line its own string", l)
		}
	}
	return nil
}

// update has the admin worker, which runs as root on this machine, make the
// harness release v.
func (a *api) update(ctx context.Context, v string) error {
	r, ok := a.pool.Get(worker.Admin)
	if !ok {
		return errors.New("the admin worker is not connected: update on the harness's machine with mainplane-server update")
	}
	ctx, cancel := context.WithTimeout(ctx, updateTime)
	defer cancel()
	return r.Update(ctx, "mainplane-server", v)
}

// save changes the config at path with fn.
func (a *api) save(fn func(*Config)) error {
	keyMu.Lock()
	defer keyMu.Unlock()
	c, err := Load(a.path)
	if err != nil {
		return err
	}
	fn(&c)
	return saveConfig(a.path, c)
}
