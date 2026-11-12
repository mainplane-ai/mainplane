package tui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mainplane-ai/mainplane/pkg/connector"
	"github.com/mainplane-ai/mainplane/pkg/harness"
)

// TestAgentSessionsHidden: the list is what the harness gave for the choice
// in /settings as it is now, and a read from before a change is dropped. An
// agent's session that is the open one is in the list though the read left
// it out, at its place by time. One shown for the first time is not unread,
// and is once it changes.
func TestAgentSessionsHidden(t *testing.T) {
	a := &App{Status: &Text{}}
	at := func(min int) time.Time { return time.Date(2026, 1, 1, 0, min, 0, 0, time.UTC) }
	open := harness.Info{ID: "open", Via: harness.AgentVia + "a2", Updated: at(5)}
	l := &sessionList{cur: &session{id: "open", info: open}, seen: map[string]time.Time{}}
	plain := []harness.Info{{ID: "plain", Via: "tui", Updated: at(9)}, {ID: "old", Updated: at(1)}}
	theirs := harness.Info{ID: "theirs", Via: harness.AgentVia + "a1", Updated: at(7)}
	all := []harness.Info{plain[0], theirs, open, plain[1]}
	ids := func() []string {
		var ids []string
		for _, in := range l.infos {
			ids = append(ids, in.ID)
		}
		return ids
	}
	l.update(a, slices.Clone(plain), false, nil)
	if got, want := ids(), []string{"plain", "open", "old"}; !slices.Equal(got, want) {
		t.Fatalf("hidden: got %v, want %v", got, want)
	}
	l.update(a, slices.Clone(all), true, nil)
	if got, want := ids(), []string{"plain", "open", "old"}; !slices.Equal(got, want) {
		t.Fatalf("a read that asked for what is not shown: got %v, want %v", got, want)
	}
	l.agents.Store(true)
	l.update(a, slices.Clone(plain), false, nil)
	l.update(a, slices.Clone(all), true, nil)
	if got, want := ids(), []string{"plain", "theirs", "open", "old"}; !slices.Equal(got, want) {
		t.Fatalf("shown: got %v, want %v", got, want)
	}
	if l.unread(theirs) {
		t.Fatal("a session hidden until now shows as unread")
	}
	theirs.Updated = at(8)
	if !l.unread(theirs) {
		t.Fatal("a session of an agent that changed since it showed is not unread")
	}
}

// TestSessionsMenu: the /sessions menu holds what the list shows. The open
// session is in it though it is an agent's and those are hidden, and a
// reply from before /settings changed is asked for again, not shown.
func TestSessionsMenu(t *testing.T) {
	open := harness.Info{ID: "open", Via: harness.AgentVia + "a2", Updated: time.Date(2026, 1, 1, 0, 5, 0, 0, time.UTC)}
	plain := harness.Info{ID: "plain", Via: "tui", Updated: open.Updated.Add(time.Minute)}
	theirs := harness.Info{ID: "theirs", Via: harness.AgentVia + "a1", Updated: open.Updated.Add(-time.Minute)}
	a := &App{Status: &Text{}, post: make(chan func(), 1)}
	l := &sessionList{}
	var flip atomic.Bool // the choice in /settings changes while the next reply is on its way
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		infos := []harness.Info{plain, open, theirs}
		if r.URL.Query().Get("plain") == "1" {
			infos = infos[:1]
		}
		if flip.Swap(false) {
			l.agents.Store(true)
		}
		_ = json.NewEncoder(w).Encode(infos)
	}))
	defer srv.Close()
	s := &session{id: "open", info: open, ctx: context.Background(), c: &connector.Client{Local: srv.URL}, list: l}
	l.cur = s
	menu := func() []string {
		t.Helper()
		a.layers = nil
		s.sessions(a)
		for len(a.layers) == 0 {
			select {
			case f := <-a.post:
				f()
			case <-time.After(10 * time.Second):
				t.Fatal("no menu")
			}
		}
		var ids []string
		for _, it := range a.layers[0].Root.Kids[2].View.(*Menu).Items {
			id, _, _ := strings.Cut(it.Label, " ")
			ids = append(ids, id)
		}
		return ids
	}
	if got, want := menu(), []string{"plain", "open"}; !slices.Equal(got, want) {
		t.Fatalf("hidden: got %v, want %v", got, want)
	}
	flip.Store(true)
	if got, want := menu(), []string{"plain", "open", "theirs"}; !slices.Equal(got, want) {
		t.Fatalf("shown while the reply was on its way: got %v, want %v", got, want)
	}
}
