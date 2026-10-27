package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/mainplane-ai/mainplane/pkg/provider"
)

// A person signs in within minutes; a login left open past this is
// abandoned. The start call waits for the first prompt this long.
const (
	loginTime  = 15 * time.Minute
	promptTime = 30 * time.Second
)

// logins runs provider logins, one at a time per provider name: a new start
// ends the one before. On success the provider joins the config at path,
// with its shape and no key, and is served at once.
type logins struct {
	ctx   context.Context
	path  string
	serve func(map[string]provider.Provider)
	mu    sync.Mutex
	m     map[string]*login
}

type login struct {
	mu     sync.Mutex
	st     provider.LoginState
	shown  chan struct{} // closed at the first prompt or the end
	done   chan struct{} // closed at the end
	paste  chan string
	cancel context.CancelFunc
}

func (l *login) view() provider.LoginState {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.st
}

// start is POST /providers/{name}/login with {"shape","url"}: a provider the
// config has keeps its own unless the body names them. It replies with the
// first prompt.
func (ls *logins) start(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var in Provider
	if err := json.NewDecoder(io.LimitReader(r.Body, maxKey)).Decode(&in); err != nil && !errors.Is(err, io.EOF) {
		fail(w, fmt.Errorf("the body is {\"shape\",\"url\"}: %w", err))
		return
	}
	if name != filepath.Base(name) || !filepath.IsLocal(name) {
		fail(w, fmt.Errorf("provider %q: a name is one path element", name))
		return
	}
	c, err := loadConfig(ls.path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if in.Shape == "" {
		in.Shape = c.Providers[name].Shape
	}
	run, ok := provider.Logins[in.Shape]
	if !ok {
		fail(w, fmt.Errorf("provider %s: shape %q has no login; it takes a key: mainplane key", name, in.Shape))
		return
	}
	dir := providerDir(filepath.Dir(ls.path), name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		fail(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(ls.ctx, loginTime)
	l := &login{st: provider.LoginState{State: "pending"}, shown: make(chan struct{}), done: make(chan struct{}), paste: make(chan string), cancel: cancel}
	ls.mu.Lock()
	if old := ls.m[name]; old != nil {
		old.cancel()
	}
	ls.m[name] = l
	ls.mu.Unlock()
	var once sync.Once
	shown := func() { once.Do(func() { close(l.shown) }) }
	go func() {
		defer cancel()
		err := run(ctx, dir, func(p provider.Prompt) {
			l.mu.Lock()
			l.st.Prompt = p
			l.mu.Unlock()
			shown()
		}, l.paste)
		if err == nil {
			err = saveProvider(ls.path, name, Provider{Shape: in.Shape, URL: in.URL}, ls.serve)
		}
		l.mu.Lock()
		l.st.State = "done"
		if err != nil {
			l.st.State, l.st.Error = "failed", err.Error()
		}
		l.mu.Unlock()
		close(l.done)
		shown()
	}()
	select {
	case <-l.shown:
	case <-time.After(promptTime):
	}
	reply(w, l.view())
}

// paste is POST /providers/{name}/login/code with {"code"}: what the prompt
// asked the user to paste.
func (ls *logins) paste(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxKey)).Decode(&in); err != nil || in.Code == "" {
		fail(w, errors.New(`the body is {"code"}`))
		return
	}
	l := ls.find(w, r)
	if l == nil {
		return
	}
	select {
	case l.paste <- in.Code:
		w.WriteHeader(http.StatusNoContent)
	case <-l.done:
		fail(w, fmt.Errorf("the login has ended: %+v", l.view()))
	case <-time.After(promptTime):
		fail(w, fmt.Errorf("the login takes no paste now: %+v", l.view()))
	}
}

// get is GET /providers/{name}/login: the login's state and prompt.
func (ls *logins) get(w http.ResponseWriter, r *http.Request) {
	if l := ls.find(w, r); l != nil {
		reply(w, l.view())
	}
}

func (ls *logins) find(w http.ResponseWriter, r *http.Request) *login {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	l := ls.m[r.PathValue("name")]
	if l == nil {
		http.Error(w, "no login started for this provider", http.StatusNotFound)
	}
	return l
}

func fail(w http.ResponseWriter, err error) { http.Error(w, err.Error(), http.StatusBadRequest) }

func reply(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
