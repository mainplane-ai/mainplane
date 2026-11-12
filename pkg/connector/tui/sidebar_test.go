package tui

import (
	"slices"
	"testing"
	"time"

	"github.com/mainplane-ai/mainplane/pkg/harness"
)

// TestAgentSessionsHidden: the list leaves out the sessions of agents, but
// not the open one, until agents is set. A session with no via is plain.
// One hidden from the first read is not unread when it shows.
func TestAgentSessionsHidden(t *testing.T) {
	a := &App{Status: &Text{}}
	l := &sessionList{cur: &session{id: "open"}, seen: map[string]time.Time{}}
	theirs := harness.Info{ID: "theirs", Via: harness.AgentVia + "a1", Updated: time.Now()}
	read := func() []string {
		l.update(a, []harness.Info{{ID: "plain", Via: "tui"}, theirs, {ID: "open", Via: harness.AgentVia + "a2"}, {ID: "old"}}, nil)
		var ids []string
		for _, in := range l.infos {
			ids = append(ids, in.ID)
		}
		return ids
	}
	if got, want := read(), []string{"plain", "open", "old"}; !slices.Equal(got, want) {
		t.Fatalf("hidden: got %v, want %v", got, want)
	}
	l.agents = true
	if got, want := read(), []string{"plain", "theirs", "open", "old"}; !slices.Equal(got, want) {
		t.Fatalf("shown: got %v, want %v", got, want)
	}
	if l.unread(theirs) {
		t.Fatal("a session hidden since the start shows as unread")
	}
}
