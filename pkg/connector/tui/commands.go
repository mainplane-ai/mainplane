package tui

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/mainplane-ai/mainplane/pkg/connector"
	"github.com/mainplane-ai/mainplane/pkg/connector/tui/term"
	"github.com/mainplane-ai/mainplane/pkg/harness"
	"github.com/mainplane-ai/mainplane/pkg/statefile"
	"github.com/mainplane-ai/mainplane/pkg/version"
)

// command is a slash command: typed as /name or an alias, picked from the
// list a / opens above the input, or from the menu Ctrl+P opens. Each one
// costs a place in that list, so a command earns it or is not one.
type command struct {
	name    string
	aliases []string
	label   string
	key     string // shown in Ctrl+P; a key of one press also runs it
	run     func(s *session, a *App)
	withArg func(s *session, a *App, arg string) // when set, /name text runs it
}

// commands is a function, not a variable: the commands make sessions, whose
// keys open the commands.
func commands() []command {
	return []command{
		{"new", []string{"clear"}, "New session", "ctrl+n", (*session).fresh, nil},
		{"sessions", []string{"resume"}, "Sessions", "ctrl+o", (*session).sessions, nil},
		{"continue", []string{"compact"}, "Continue in a new session", "ctrl+k", (*session).carryOn, nil},
		{"fork", nil, "Fork into a new session", "", (*session).fork, nil},
		{"rewind", []string{"revert"}, "Rewind to before a message", "", (*session).rewind, nil},
		{"rename", nil, "Rename the session", "", (*session).renameDialog, (*session).rename},
		{"model", nil, "Model for a new session", "", (*session).models, (*session).model},
		{"workers", nil, "Workers", "ctrl+e", (*session).workers, nil},
		{"stop", nil, "Stop the session", "esc esc", (*session).halt, nil},
		{"info", []string{"debug", "id"}, "Session info", "", (*session).infoMenu, nil},
		{"help", nil, "Commands and keys", "f1", (*session).palette, nil},
		{"exit", []string{"quit"}, "Exit", "ctrl+c ctrl+c", func(_ *session, a *App) { a.Quit() }, nil},
	}
}

func (c command) is(word string) bool { return word == c.name || slices.Contains(c.aliases, word) }

// commandFor is the command text names, and the text after it: one line,
// /word and, for a command that takes it, more.
func commandFor(text string) (*command, string) {
	t := strings.TrimSpace(text)
	if !strings.HasPrefix(t, "/") || strings.Contains(t, "\n") {
		return nil, ""
	}
	word, arg, _ := strings.Cut(t[1:], " ")
	arg = strings.TrimSpace(arg)
	for _, c := range commands() {
		if c.is(word) && (arg == "" || c.withArg != nil) {
			return &c, arg
		}
	}
	return nil, ""
}

func (c command) do(s *session, a *App, arg string) {
	if arg != "" {
		c.withArg(s, a, arg)
		return
	}
	c.run(s, a)
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
		case slices.ContainsFunc(c.aliases, func(al string) bool { return strings.HasPrefix(al, t[1:]) }):
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
		if c.name != "help" && c.name != "exit" {
			run := c.run
			add("Session", c.label, c.key, func(a *App) { run(s, a) })
		}
	}
	add("Session", "Previous session", "ctrl+up", func(a *App) { s.list.step(a, -1) })
	add("Session", "Next session", "ctrl+down", func(a *App) { s.list.step(a, 1) })
	add("Session", "Copy session id", "", func(a *App) {
		if s.id != "" {
			a.Copy(s.id)
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
	to.input.Clear() // not the home draft, which stays kept until this screen leaves
	to.input.Attach("[continue session "+s.id+"]", "You are continuing session "+s.id+". Retrieve key context from it as you go.")
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

// fork copies the whole session into a new one and opens it. The harness
// copies only a session that is done.
func (s *session) fork(a *App) {
	switch {
	case s.id == "":
		a.Say("No session to fork yet")
	case s.info.ID != s.id:
		a.Say("The session is still loading")
	case s.stepping():
		a.Say("Fork when the session is done: wait, or /stop it")
	default:
		s.copyBefore(a, s.info.N+1, "")
	}
}

// rewind is the menu of the messages sent, newest first. Picking one opens
// a copy of the session from before it, with the message in the input.
func (s *session) rewind(a *App) {
	if len(s.asked) == 0 {
		a.Say("No message to rewind to")
		return
	}
	m := &Menu{}
	for _, r := range slices.Backward(s.asked) {
		text, label := "", "["+media(r)+"]"
		if isText(r.Type) {
			text = string(r.Body)
			label = strings.TrimSpace(lines(text)[0])
		}
		n := r.N
		m.Items = append(m.Items, MenuItem{Label: label, Hint: stamp(r.Time), Pick: func(a *App, _ *MenuItem) { s.copyBefore(a, n, text) }})
	}
	a.OpenMenu("Rewind to before", m)
}

// copyBefore makes a session of this one's records before record n, then
// opens it with input in its input.
func (s *session) copyBefore(a *App, n int, input string) {
	id := s.id
	a.Go(func() {
		ctx, cancel := context.WithTimeout(context.Background(), callTime)
		defer cancel()
		to, err := s.c.Create(ctx, harness.Create{From: id, N: n}, via)
		a.Post(func() {
			if err != nil {
				a.SayError(err)
				return
			}
			if input != "" {
				s.list.drafts[to] = textElems(input)
			}
			if s.ctx.Err() != nil {
				a.Say("Made session " + to) // the screen moved on: the list shows it
				return
			}
			s.list.open(a, to)
		})
	})
}

// renameDialog asks for the session's name, the current one to edit.
func (s *session) renameDialog(a *App) {
	if s.id == "" {
		a.Say("No session to rename yet")
		return
	}
	in := NewInput()
	in.Placeholder, in.Cursor = "Name", true
	in.insert(textElems(s.info.Name)...)
	var l *Layer
	in.OnSubmit = func(a *App, name string) {
		a.Pop(l)
		s.rename(a, name)
	}
	l = a.Dialog("Rename session", &Node{View: in})
}

func (s *session) rename(a *App, name string) {
	if s.id == "" {
		a.Say("No session to rename yet")
		return
	}
	id := s.id
	a.Go(func() {
		ctx, cancel := context.WithTimeout(context.Background(), callTime)
		defer cancel()
		err := s.c.SetName(ctx, id, via, strings.TrimSpace(name))
		a.Post(func() {
			if err != nil {
				a.SayError(err)
			}
		})
	})
}

// models is the menu of the models the listed sessions use, newest first.
// A model's context limit, params and input come from its newest session:
// they belong to the model, and the harness lists no models of its own.
func (s *session) models(a *App) {
	m := &Menu{}
	cur := s.base().Model
	for _, cf := range s.modelConfs() {
		hint := tokens(cf.ContextLimit)
		if cf.Model == cur {
			hint = "current · " + hint
		}
		m.Items = append(m.Items, MenuItem{Label: cf.Model, Hint: hint, Pick: func(a *App, _ *MenuItem) { s.useModel(a, cf) }})
	}
	if len(m.Items) == 0 {
		a.Say("No session lists a model yet")
		return
	}
	a.OpenMenu("Model for a new session", m)
}

// modelConfs is the current model's config, then each other model's from
// its newest listed session.
func (s *session) modelConfs() []statefile.Conf {
	all := []statefile.Conf{s.base()}
	for _, in := range s.list.infos {
		all = append(all, in.Config)
	}
	var confs []statefile.Conf
	for _, cf := range all {
		if cf.Model != "" && !slices.ContainsFunc(confs, func(c statefile.Conf) bool { return c.Model == cf.Model }) {
			confs = append(confs, cf)
		}
	}
	return confs
}

// model is /model name: the model whose id is name, or ends in /name.
func (s *session) model(a *App, name string) {
	for _, cf := range s.modelConfs() {
		if cf.Model == name || strings.HasSuffix(cf.Model, "/"+name) {
			s.useModel(a, cf)
			return
		}
	}
	a.Say("No session uses " + name + ": /model lists those that do")
}

// base is the config a new session starts from: this session's, the one
// chosen for this screen, else the newest session's.
func (s *session) base() statefile.Conf {
	switch {
	case s.id != "":
		return s.info.Config
	case s.cfg != nil:
		return *s.cfg
	case len(s.list.infos) > 0:
		return s.list.infos[0].Config
	}
	return statefile.Conf{}
}

// useModel opens a blank session screen that makes its session with the
// model's config and this session's workers; on a blank screen, that one.
func (s *session) useModel(a *App, m statefile.Conf) {
	if s.id != "" && s.info.ID != s.id {
		a.Say("The session is still loading")
		return
	}
	m.Workers = s.base().Workers
	if s.id == "" {
		s.cfg = &m
		return
	}
	to := newSession(a, s.list, "")
	to.cfg = &m
	s.switchTo(a, to)
}

// halt is /stop: Esc Esc, for whoever looks for it by name.
func (s *session) halt(a *App) {
	if !s.stepping() {
		a.Say("The session is not working")
		return
	}
	s.stop(a)
}

// infoHint is the most characters of a value /info shows, so its label fits
// beside it in a dialog; a pick still copies all of it.
const infoHint = 36

// infoMenu is what is known of the session, its workers and the app. A
// pick copies the value.
func (s *session) infoMenu(a *App) {
	a.Go(func() {
		ctx, cancel := context.WithTimeout(context.Background(), callTime)
		defer cancel()
		hv, err := s.c.Version(ctx)
		var listed []connector.Worker
		if err == nil {
			listed, err = s.c.Workers(ctx)
		}
		a.Post(func() {
			if s.ctx.Err() != nil {
				return
			}
			if err != nil {
				a.SayError(err)
			}
			a.OpenMenu("Session info", s.infoItems(a, hv, listed))
		})
	})
}

// infoItems is the rows of /info.
func (s *session) infoItems(a *App, harnessV string, listed []connector.Worker) *Menu {
	m := &Menu{}
	add := func(section, label, value string) {
		if value == "" {
			return
		}
		hint := value
		if rs := []rune(value); len(rs) > infoHint {
			hint = "…" + string(rs[len(rs)-infoHint+1:]) // the end of a URL tells more
		}
		m.Items = append(m.Items, MenuItem{Label: label, Hint: hint, Section: section, Pick: func(a *App, _ *MenuItem) { a.Copy(value) }})
	}
	in := s.info
	if s.id != "" && in.ID == s.id {
		add("Session", "Id", in.ID)
		add("Session", "Name", in.Name)
		add("Session", "Status", string(in.Status))
		add("Session", "Model", in.Config.Model)
		add("Session", "Context", fmt.Sprintf("%s of %s", tokens(in.ContextUsed), tokens(in.Config.ContextLimit)))
		if c, ok := s.cost(); ok {
			add("Session", "Cost", fmt.Sprintf("$%.2f", c))
		}
		add("Session", "Records", fmt.Sprint(in.N))
		add("Session", "Created", in.Created.Local().Format(time.DateTime))
		add("Session", "Updated", in.Updated.Local().Format(time.DateTime))
		add("Session", "Input", strings.Join(in.Config.Input, ", "))
		_, state := workerStates(listed, in.Config.Workers)
		for _, w := range in.Config.Workers {
			add("Workers", w.Name, state[w.Name])
		}
	} else {
		add("Session", "Model", s.base().Model)
	}
	add("App", "Harness", s.c.URL)
	add("App", "Harness version", harnessV)
	add("App", "App version", version.V)
	add("App", "Terminal", fmt.Sprintf("%dx%d, kitty keys %v, truecolor %v", a.W, a.H, a.T.Caps.Kitty, a.T.Caps.Truecolor))
	return m
}
