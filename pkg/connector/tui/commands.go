package tui

import (
	"context"
	"strings"

	"github.com/mainplane-ai/mainplane/pkg/connector"
	"github.com/mainplane-ai/mainplane/pkg/connector/tui/term"
	"github.com/mainplane-ai/mainplane/pkg/statefile"
)

// command is a slash command: typed alone as /name or /alias, picked from
// the list a / opens above the input, or from the menu Ctrl+P opens.
type command struct {
	name, alias, label, key string
	run                     func(s *session, a *App)
}

// commands is a function, not a variable: the commands make sessions, whose
// keys open the commands.
func commands() []command {
	return []command{
		{"new", "clear", "New session", "ctrl+n", (*session).fresh},
		{"sessions", "resume", "Sessions", "ctrl+o", (*session).sessions},
		{"continue", "compact", "Continue in a new session", "ctrl+k", (*session).carryOn},
		{"workers", "", "Workers", "ctrl+e", (*session).workers},
		{"help", "", "Commands and keys", "f1", (*session).palette},
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

// slashRows is the most rows the list above the input shows.
const slashRows = 10

// slash is the list of commands above the input while it holds a / and the
// start of a command's name or alias. Up and Down move, Enter, Tab or a
// click runs, Esc closes it until the input changes.
type slash struct {
	s     *session
	query string // the input that made the list
	cmds  []command
	sel   int
	top   int
}

// update makes the list for the input: none unless it is a / and the start
// of a command word. Names that match come before aliases that do.
func (l *slash) update() {
	t := l.s.input.Text()
	if t == l.query {
		return
	}
	l.query, l.cmds, l.sel, l.top = t, nil, 0, 0
	for _, e := range l.s.input.els {
		if e.paste != "" {
			return
		}
	}
	if !strings.HasPrefix(t, "/") || strings.ContainsAny(t, " \t\n") {
		return
	}
	var byAlias []command
	for _, c := range commands() {
		switch {
		case strings.HasPrefix(c.name, t[1:]):
			l.cmds = append(l.cmds, c)
		case c.alias != "" && strings.HasPrefix(c.alias, t[1:]):
			byAlias = append(byAlias, c)
		}
	}
	l.cmds = append(l.cmds, byAlias...)
}

func (l *slash) Height(int) int { return min(len(l.cmds), slashRows) }

func (l *slash) run(a *App, i int) {
	if i < 0 || i >= len(l.cmds) {
		return
	}
	c := l.cmds[i]
	l.s.input.Clear()
	c.run(l.s, a)
}

// Key is the input's Before: it takes the keys of the list while it shows.
func (l *slash) Key(a *App, k term.Key) bool {
	if l.update(); len(l.cmds) == 0 {
		return false
	}
	switch k.String() {
	case "up":
		l.sel = (l.sel + len(l.cmds) - 1) % len(l.cmds)
	case "down":
		l.sel = (l.sel + 1) % len(l.cmds)
	case "enter", "tab":
		l.run(a, l.sel)
	case "esc":
		l.cmds = nil // until the input changes
	default:
		return false
	}
	return true
}

func (l *slash) Mouse(a *App, m term.Mouse, r Rect) bool {
	i := l.top + m.Y - r.Y
	switch {
	case m.Button == term.WheelUp:
		l.sel = max(0, l.sel-1)
	case m.Button == term.WheelDown:
		l.sel = min(len(l.cmds)-1, l.sel+1)
	case m.Action == term.Motion && m.Button == term.NoButton:
		if i < len(l.cmds) {
			l.sel = i
		}
	case m.Action == term.Press && m.Button == term.Left:
		l.run(a, i)
	default:
		return false
	}
	return true
}

func (l *slash) Draw(a *App, c *Canvas, r Rect) {
	nameW := 0
	for _, cmd := range commands() {
		nameW = max(nameW, term.Width(cmd.name)+1)
	}
	l.top = min(max(l.top, l.sel-r.H+1), l.sel)
	for y := 0; y < r.H && l.top+y < len(l.cmds); y++ {
		cmd, st, desc := l.cmds[l.top+y], a.Theme.text(), a.Theme.muted()
		if l.top+y == l.sel {
			c.Fill(Rect{r.X, r.Y + y, r.W, 1}, a.Theme.Accent)
			st = term.Style{FG: a.Theme.BG, BG: a.Theme.Accent}
			desc = st
		}
		c.Text(r.X+1, r.Y+y, "/"+cmd.name, st)
		c.Text(r.X+1+nameW+3, r.Y+y, cmd.label, desc)
	}
}

// palette is the menu of every key: the commands and the actions run from
// it, the rest only list their keys.
func (s *session) palette(a *App) {
	m := &Menu{}
	add := func(section, label, key string, do func(a *App)) {
		it := MenuItem{Label: label, Hint: key, Section: section}
		if do != nil {
			it.Pick = func(a *App, _ *MenuItem) { do(a) }
		}
		m.Items = append(m.Items, it)
	}
	for _, c := range commands() {
		if c.name != "help" {
			run := c.run
			add("Session", c.label, c.key, func(a *App) { run(s, a) })
		}
	}
	add("Session", "Previous session", "ctrl+up", func(a *App) { s.list.step(a, -1) })
	add("Session", "Next session", "ctrl+down", func(a *App) { s.list.step(a, 1) })
	add("Session", "Stop the session", "esc esc", func(a *App) {
		if s.stepping() {
			s.stop(a)
		}
	})
	add("Input", "Send", "enter", nil)
	add("Input", "New line", "shift+enter, ctrl+j", nil)
	add("Input", "Slash commands", "/", func(a *App) {
		s.input.Clear()
		s.input.insert(textElems("/")...)
	})
	add("Input", "Select", "shift+arrows", nil)
	add("Input", "Cut the selection", "ctrl+x", nil)
	add("Input", "Delete the word before", "ctrl+backspace", nil)
	add("Input", "Delete the next word", "ctrl+delete", nil)
	add("Input", "Start or end of the line", "home, end", nil)
	add("Transcript", "Page up", "pgup", func(a *App) { s.transcript.Scroll(-s.transcript.Page()) })
	add("Transcript", "Page down", "pgdn", func(a *App) { s.transcript.Scroll(s.transcript.Page()) })
	add("Transcript", "Top", "ctrl+home", func(a *App) { s.transcript.Scroll(-1 << 30) })
	add("Transcript", "Bottom", "ctrl+end", func(a *App) { s.transcript.Scroll(1 << 30) })
	add("Transcript", "Select and copy", "drag", nil)
	add("App", "Commands and keys", "ctrl+p, f1", nil)
	add("App", "Copy the selection, else exit", "ctrl+c", nil)
	add("App", "Exit", "ctrl+c ctrl+c", func(a *App) { a.Quit() })
	a.OpenMenu("Commands", m)
}

// switchTo leaves this session for to: its polls and posts end.
func (s *session) switchTo(a *App, to *session) {
	s.cancel()
	s.list.leave()
	s.list.cur = to
	a.T.Progress(false)
	a.SetScreen(to)
	s.list.tick(a)
}

// fresh is the empty screen; the first message makes a session.
func (s *session) fresh(a *App) { s.switchTo(a, newSession(a, s.list, "")) }

// carryOn opens a blank session screen with this session's config, its
// input holding a message to continue from this one. Sending it makes the
// session.
func (s *session) carryOn(a *App) {
	if s.id == "" {
		a.Say("No session to continue yet")
		return
	}
	if s.info.Config.Model == "" {
		a.Say("The session is still loading")
		return
	}
	to := newSession(a, s.list, "")
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
			if s.ctx.Err() != nil {
				return
			}
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
					s.list.open(a, id)
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
			if s.ctx.Err() != nil {
				return
			}
			if err != nil {
				a.SayError(err)
				return
			}
			s.workerMenu(a, listed)
		})
	})
}

// workerStates is every worker the harness lists, then those of the
// session it does not, each connected, refused or offline.
func workerStates(listed []connector.Worker, own []statefile.Worker) ([]string, map[string]string) {
	state := map[string]string{}
	var names []string
	for _, w := range listed {
		state[w.Name] = "connected"
		if w.Refused != "" {
			state[w.Name] = "refused"
		}
		names = append(names, w.Name)
	}
	for _, w := range own {
		if _, ok := state[w.Name]; !ok {
			state[w.Name] = "offline"
			names = append(names, w.Name)
		}
	}
	return names, state
}

func (s *session) workerMenu(a *App, listed []connector.Worker) {
	m := &Menu{Checks: true}
	have := map[string]statefile.Worker{}
	for _, w := range s.info.Config.Workers {
		have[w.Name] = w
	}
	names, state := workerStates(listed, s.info.Config.Workers)
	set := func(a *App, _ *MenuItem) {
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
		id, n := s.id, s.workerSets.Add(1)
		a.Go(func() {
			s.workerSet.Lock()
			defer s.workerSet.Unlock()
			if s.workerSets.Load() != n {
				return // a newer toggle sends its own list
			}
			ctx, cancel := context.WithTimeout(context.Background(), callTime)
			defer cancel()
			err := s.c.SetWorkers(ctx, id, via, ws)
			a.Post(func() {
				if err != nil {
					for i := range m.Items {
						_, m.Items[i].On = have[m.Items[i].Label]
					}
					a.SayError(err)
					return
				}
				have = map[string]statefile.Worker{}
				for _, w := range ws {
					have[w.Name] = w
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
