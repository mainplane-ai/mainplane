package tui

import (
	"testing"

	"github.com/mainplane-ai/mainplane/pkg/harness"
)

func TestSessionListFiltersAgentSessionsByDefault(t *testing.T) {
	l := &sessionList{
		cur: &session{id: ""},
		all: []harness.Info{
			{ID: "plain111", Via: "tui"},
			{ID: "agent111", Via: "agent:a1b2c3d4"},
			{ID: "plain222", Via: "cli"},
			{ID: "agent222", Via: "agent:deadbeef"},
		},
	}

	l.applyFilter()
	if len(l.infos) != 2 || l.infos[0].ID != "plain111" || l.infos[1].ID != "plain222" {
		t.Fatalf("default filter got %+v, want only plain111 and plain222", l.infos)
	}

	// Opening an agent session directly keeps it visible while open.
	l.cur.id = "agent111"
	l.applyFilter()
	if len(l.infos) != 3 || l.infos[1].ID != "agent111" {
		t.Fatalf("open agent session should stay visible, got %+v", l.infos)
	}

	// Toggling showAgents shows all sessions.
	l.cur.id = ""
	l.showAgents = true
	l.applyFilter()
	if len(l.infos) != 4 {
		t.Fatalf("showAgents=true got %d sessions, want 4", len(l.infos))
	}
}
