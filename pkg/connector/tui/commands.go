package tui

import (
	"context"
	"strings"

	"github.com/mainplane-ai/mainplane/pkg/connector"
	"github.com/mainplane-ai/mainplane/pkg/statefile"
)

// command is a slash command: typed alone as /name or /alias, or picked
// from the menu Ctrl+P or a lone / opens.
type command struct {
	name, alias, label string
	run                func(s *session, a *App)
}

// commands is a function, not a variable: the commands make sessions, whose
// keys open the commands.
func commands() []command {
	return []command{
		{"new", "clear", "New session", (*session).fresh},
		{"sessions", "resume", "Sessions", (*session).sessions},
		{"continue", "compact", "Continue in a new session", (*session).carryOn},
		{"workers", "", "Workers", (*session).workers},
		{"help", "", "Keys", (*session).keys},
	}
}

// command is the command text names, when it is one word that is a
// command's name or alias.
func commandFor(text string) *command {
	t := strings.TrimSpace(text)
	if !strings.HasPrefix(t, "/") || strings.ContainsAny(t, " \n") {
		return nil
	}
	for _, c := range commands() {
		if t[1:] == c.name || c.alias != "" && t[1:] == c.alias {
			return &c
		}
	}
	return nil
}

// palette is the menu of every command.
func (s *session) palette(a *App) {
	m := &Menu{}
	for _, c := range commands() {
		run := c.run
		m.Items = append(m.Items, MenuItem{Label: c.label, Hint: "/" + c.name, Pick: func(a *App, _ *MenuItem) { run(s, a) }})
	}
	a.OpenMenu("Commands", m)
}

// switchTo leaves this session for to: its polls and posts end.
func (s *session) switchTo(a *App, to *session) {
	s.cancel()
	a.Ticking(false)
	a.T.Progress(false)
	a.SetScreen(to)
}

// fresh is the empty screen; the first message makes a session.
func (s *session) fresh(a *App) { s.switchTo(a, newSession(a, s.c, "")) }

// carryOn opens a blank session screen with this session's config, its
// input holding a message to continue from this one. Sending it makes the
// session.
func (s *session) carryOn(a *App) {
	if s.id == "" {
		a.Say("No session to continue yet")
		return
	}
	to := newSession(a, s.c, "")
	cf := s.info.Config
	to.cfg, to.started = &cf, true
	to.input.Attach("[continue session "+s.id+"]", "You are continuing session "+s.id+". Retrieve key context from it and continue.")
	s.switchTo(a, to)
}

// sessions is the menu of sessions, newest updated first.
func (s *session) sessions(a *App) {
	a.Go(func() {
		ctx, cancel := context.WithTimeout(context.Background(), callTime)
		defer cancel()
		infos, err := s.c.Sessions(ctx, "", 200)
		a.Post(func() {
			if err != nil {
				a.SayError(err)
				return
			}
			m := &Menu{}
			for _, in := range infos {
				id := in.ID
				label := id
				if in.Name != "" {
					label += "  " + in.Name
				}
				if id == s.id {
					label += "  (this one)"
				}
				m.Items = append(m.Items, MenuItem{Label: label, Hint: string(in.Status) + " · " + stamp(in.Updated), Pick: func(a *App, _ *MenuItem) {
					if id != s.id {
						s.switchTo(a, newSession(a, s.c, id))
					}
				}})
			}
			a.OpenMenu("Sessions", m)
		})
	})
}

// workers is the menu of workers, checked for those of this session. Each
// toggle sets the session's workers at once, from its next step on.
func (s *session) workers(a *App) {
	if s.id == "" {
		a.Say("No session yet: workers are set once it is made")
		return
	}
	a.Go(func() {
		ctx, cancel := context.WithTimeout(context.Background(), callTime)
		defer cancel()
		listed, err := s.c.Workers(ctx)
		a.Post(func() {
			if err != nil {
				a.SayError(err)
				return
			}
			s.workerMenu(a, listed)
		})
	})
}

func (s *session) workerMenu(a *App, listed []connector.Worker) {
	m := &Menu{Checks: true}
	have := map[string]statefile.Worker{}
	for _, w := range s.info.Config.Workers {
		have[w.Name] = w
	}
	state := map[string]string{}
	var names []string
	for _, w := range listed {
		state[w.Name] = "connected"
		if w.Refused != "" {
			state[w.Name] = "refused"
		}
		names = append(names, w.Name)
	}
	for _, w := range s.info.Config.Workers {
		if _, ok := state[w.Name]; !ok {
			state[w.Name] = "offline"
			names = append(names, w.Name)
		}
	}
	set := func(a *App, it *MenuItem) {
		var ws []statefile.Worker
		for _, x := range m.Items {
			if x.On {
				w, ok := have[x.Label]
				if !ok {
					w = statefile.Worker{Name: x.Label}
				}
				ws = append(ws, w)
			}
		}
		id := s.id
		a.Go(func() {
			ctx, cancel := context.WithTimeout(context.Background(), callTime)
			defer cancel()
			err := s.c.SetWorkers(ctx, id, via, ws)
			a.Post(func() {
				if err != nil {
					it.On = !it.On
					a.SayError(err)
				}
			})
		})
	}
	for _, n := range names {
		_, on := have[n]
		m.Items = append(m.Items, MenuItem{Label: n, Hint: state[n], On: on, Pick: set})
	}
	a.OpenMenu("Workers", m)
}
