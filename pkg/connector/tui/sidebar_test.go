package tui

import (
	"context"
	"testing"
	"time"

	"github.com/mainplane-ai/mainplane/pkg/connector/tui/term"
	"github.com/mainplane-ai/mainplane/pkg/harness"
)

func TestSessionListFiltersAgentSessionsByDefault(t *testing.T) {
	a := &App{T: &term.Terminal{}, Status: &Text{}}

	l := &sessionList{
		seen:   map[string]time.Time{},
		drafts: map[string][]elem{},
		all: []harness.Info{
			{ID: "plain111", Via: "tui"},
			{ID: "agent111", Via: "agent:a1b2c3d4"},
			{ID: "plain222", Via: "cli"},
			{ID: "agent222", Via: "agent:deadbeef"},
		},
	}
	sAgent := &session{id: "agent111", list: l, input: NewInput(), cancel: func() {}, ctx: context.Background()}
	sPlain := &session{id: "plain111", list: l, input: NewInput(), cancel: func() {}, ctx: context.Background()}
	l.cur = sPlain

	l.applyFilter()
	if len(l.infos) != 2 || l.infos[0].ID != "plain111" || l.infos[1].ID != "plain222" {
		t.Fatalf("default filter got %+v, want only plain111 and plain222", l.infos)
	}

	// Navigating to an agent session keeps it visible while open.
	sPlain.switchTo(a, sAgent)
	if len(l.infos) != 3 || l.infos[1].ID != "agent111" {
		t.Fatalf("open agent session should stay visible, got %+v", l.infos)
	}

	// Navigating away via switchTo immediately hides the agent session again.
	sAgent.switchTo(a, sPlain)
	if len(l.infos) != 2 || l.infos[0].ID != "plain111" || l.infos[1].ID != "plain222" {
		t.Fatalf("leaving agent session should immediately hide it, got %+v", l.infos)
	}

	// Toggling showAgents shows all sessions.
	l.showAgents = true
	l.applyFilter()
	if len(l.infos) != 4 {
		t.Fatalf("showAgents=true got %d sessions, want 4", len(l.infos))
	}
}
