package tui

import (
	"slices"
	"testing"
	"time"

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
