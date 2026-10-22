// Package tui is the Mainplane terminal app: a retained tree of components
// on the term engine, and its screens: login, and the session.
package tui

import (
	"github.com/mainplane-ai/mainplane/pkg/connector"
	"github.com/mainplane-ai/mainplane/pkg/connector/tui/term"
)

// Run takes the terminal until the user exits. With no client it opens on
// login; with quick, on the quickstart; with a session id, on that session;
// else on the empty session. It returns a release when the app ended to
// update to it.
func Run(c *connector.Client, id string, quick bool) (update string, err error) {
	t, err := term.Open()
	if err != nil {
		return "", err
	}
	defer t.Close()
	defer t.Recover()
	a := NewApp(t)
	switch {
	case c == nil:
		a.SetScreen(newLogin(a))
	case quick:
		a.SetScreen(newQuickstart(a, *c))
	default:
		a.SetScreen(newSession(a, newSessionList(a, *c), id))
	}
	a.Run()
	return a.Update, nil
}
