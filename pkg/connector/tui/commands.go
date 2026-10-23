package tui

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/http"
	neturl "net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/mainplane-ai/mainplane/pkg/catalog"
	"github.com/mainplane-ai/mainplane/pkg/connector"
	"github.com/mainplane-ai/mainplane/pkg/connector/tui/term"
	"github.com/mainplane-ai/mainplane/pkg/harness"
	"github.com/mainplane-ai/mainplane/pkg/mesh"
	"github.com/mainplane-ai/mainplane/pkg/provider"
	"github.com/mainplane-ai/mainplane/pkg/statefile"
	"github.com/mainplane-ai/mainplane/pkg/version"
)

// command is a slash command: typed as /name or an alias, picked from the
// list a / opens above the input, or from the menu Ctrl+P opens. Each one
// costs a place in that list, so a command earns it or is not one. A
// command takes no text: the text after it stays in the input, and a
// command that opens a new session takes it there.
type command struct {
	name    string
	aliases []string
	label   string
	key     string // shown in Ctrl+P; a key of one press also runs it
	run     func(s *session, a *App)
}

// commands is a function, not a variable: the commands make sessions, whose
// keys open the commands. Where names share a start, the one used more
// comes first, and the list shows names before aliases: /re is /rename.
func commands() []command {
	return []command{
		{"new", []string{"clear"}, "New session", "ctrl+n", (*session).fresh},
		{"sessions", []string{"resume"}, "Sessions", "ctrl+o", (*session).sessions},
		{"continue", []string{"compact"}, "Continue in a new session", "ctrl+k", (*session).carryOn},
		{"config", nil, "Config for a new session", "", (*session).config},
		{"rename", nil, "Rename the session", "", (*session).renameDialog},
		{"model", nil, "Model for a new session", "", (*session).models},
		{"providers", []string{"connect"}, "Connect a provider", "", (*session).providers},
		{"goto", []string{"jump", "timeline"}, "Go to a message", "", (*session).goTo},
		{"fork", nil, "Fork into a new session", "", (*session).fork},
		{"rollback", []string{"revert", "rewind"}, "Roll back to before a message", "", (*session).rollback},
		{"workers", nil, "Workers", "ctrl+e", (*session).workers},
		{"stop", nil, "Stop the session", "esc esc", (*session).halt},
		{"info", []string{"debug", "id"}, "Session info", "", (*session).infoMenu},
		{"log", nil, "Save the session's log file", "", (*session).saveLog},
		{"delete", nil, "Delete the session", "", (*session).deleteMenu},
		{"help", nil, "Commands and keys", "f1", (*session).palette},
		{"exit", []string{"quit"}, "Exit", "ctrl+c ctrl+c", func(_ *session, a *App) { a.Quit() }},
	}
}

func (c command) is(word string) bool { return word == c.name || slices.Contains(c.aliases, word) }

// slashRows is the most rows the list above the input shows.
const slashRows = 10

// slash is the list of commands above an input while the input up to the
// cursor is a / and the start of a command's name or alias. Up and Down
// move, Enter, Tab or a click runs, Esc closes it until that text changes.
// The text after the cursor stays in the input.
type slash struct {
	s     *session
	in    *Input
	all   []command // the commands it offers: commands(), or the quickstart's one
	query string    // the input up to the cursor that made the list
	cmds  []command
	sel   int
	top   int
}

// update makes the list for the input up to the cursor. Names that match
// come before aliases that do.
func (l *slash) update() {
	in := l.in
	before := Input{els: in.els[:in.cur]}
	t := before.Text()
	if t == l.query {
		return
	}
	l.query, l.cmds, l.sel, l.top = t, nil, 0, 0
	if !strings.HasPrefix(t, "/") || slices.ContainsFunc(before.els, func(e elem) bool { return e.paste != "" || e.space() }) {
		return
	}
	var byAlias []command
	for _, c := range l.all {
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
	l.runCommand(a, l.cmds[i], l.in.cur)
}

// runCommand runs c, typed in the input's first end elements, which leave it.
func (l *slash) runCommand(a *App, c command, end int) {
	in := l.in
	rest := in.els[end:]
	for len(rest) > 0 && rest[0].space() {
		rest = rest[1:]
	}
	in.Clear()
	in.els = append([]elem(nil), rest...)
	in.cur = len(in.els)
	c.run(l.s, a)
}

// typed is the command the input's first word names, and the word's end:
// Enter runs it when the list is closed.
func (l *slash) typed() (*command, int) {
	els := l.in.els
	end := slices.IndexFunc(els, func(e elem) bool { return e.space() || e.paste != "" })
	if end < 0 {
		end = len(els)
	}
	word := Input{els: els[:end]}
	t := word.Text()
	if !strings.HasPrefix(t, "/") {
		return nil, 0
	}
	for _, c := range l.all {
		if c.is(t[1:]) {
			return &c, end
		}
	}
	return nil, 0
}

// Key is the input's Before: it takes the keys of the list while it shows,
// and Enter on a typed command.
func (l *slash) Key(a *App, k term.Key) bool {
	if l.update(); len(l.cmds) == 0 {
		if c, end := l.typed(); c != nil && k.String() == "enter" {
			l.runCommand(a, *c, end)
			return true
		}
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
	for _, cmd := range l.all {
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
		s.input.cur, s.input.anchor = 0, -1
		s.input.insert(textElems("/")...)
	})
	add("Input", "Select", "shift+arrows", nil)
	add("Input", "Undo", "ctrl+z", nil)
	add("Input", "Redo", "ctrl+y, ctrl+shift+z", nil)
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
func (s *session) fresh(a *App) {
	to := newSession(a, s.list, "")
	s.carry(to)
	s.switchTo(a, to)
}

// carry moves the text in the input to the new session screen to, in place
// of its draft: a command that opens a new session takes it along.
func (s *session) carry(to *session) {
	if len(s.input.els) == 0 {
		return
	}
	to.input.Clear()
	to.input.insert(s.input.els...)
	s.input.Clear()
}

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
	s.continueAs(a, s.info.Config, s.price)
}

// continueAs is carryOn with the config cf at price p.
func (s *session) continueAs(a *App, cf statefile.Conf, p *catalog.Cost) {
	to := newSession(a, s.list, "")
	to.cfg, to.price, to.started = &cf, p, true
	to.input.Clear() // not the home draft, which stays kept until this screen leaves
	to.input.Attach("[continue session "+s.id+"]", "You are continuing session "+s.id+". Retrieve key context from it as you go.")
	if len(s.input.els) > 0 {
		to.input.insert(elem{g: "\n"})
		to.input.insert(s.input.els...)
		s.input.Clear()
	}
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
		els := s.input.els
		s.input.Clear()
		s.copyBefore(a, s.info.N+1, els, true)
	}
}

// rollback is the menu of the messages sent, newest first. Picking one
// opens a copy of the session from before it, with the message in the input.
func (s *session) rollback(a *App) {
	if len(s.asked) == 0 {
		a.Say("No message to roll back to")
		return
	}
	m := s.askedMenu(func(a *App, b *msgBlock, text string) { s.copyBefore(a, b.r.N, textElems(text), false) })
	a.OpenMenu("Roll back to before", m)
}

// goTo is the menu of the messages sent, newest first. Picking one scrolls
// the transcript to it.
func (s *session) goTo(a *App) {
	if len(s.asked) == 0 {
		a.Say("No message to go to")
		return
	}
	a.OpenMenu("Go to", s.askedMenu(func(_ *App, b *msgBlock, _ string) { s.transcript.Show(b) }))
}

// askedMenu is the messages sent, newest first, each its first line.
func (s *session) askedMenu(pick func(a *App, b *msgBlock, text string)) *Menu {
	m := &Menu{}
	for _, b := range slices.Backward(s.asked) {
		text, label := "", "["+media(b.r)+"]"
		if isText(b.r.Type) {
			text = string(b.r.Body)
			label = strings.TrimSpace(lines(text)[0])
		}
		m.Items = append(m.Items, MenuItem{Label: label, Hint: stamp(b.r.Time), Pick: func(a *App, _ *MenuItem) { pick(a, b, text) }})
	}
	return m
}

// saveLog writes the session as its .log file to ~/.mainplane/logs on this
// machine, where a worker gets the copies the harness sends, and copies the
// path. As the harness's copy, a body that is not text is left out, so rg
// reads the file.
func (s *session) saveLog(a *App) {
	if s.id == "" {
		a.Say("No session to save yet")
		return
	}
	id := s.id
	a.Go(func() {
		path, err := func() (string, error) {
			ctx, cancel := context.WithTimeout(context.Background(), callTime)
			defer cancel()
			recs, err := s.c.Records(ctx, id, 0, "")
			if err != nil {
				return "", err
			}
			var b []byte
			for _, r := range recs {
				if !isText(r.Type) {
					r.Body = nil
				}
				enc, err := statefile.Encode(r)
				if err != nil {
					return "", err
				}
				b = append(b, enc...)
			}
			h, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			dir := filepath.Join(h, ".mainplane", "logs")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return "", err
			}
			path := filepath.Join(dir, id+".log")
			return path, os.WriteFile(path, b, 0o600)
		}()
		a.Post(func() {
			if err != nil {
				a.SayError(err)
				return
			}
			a.T.Copy(path)
			a.Say("Saved " + path + " (path copied)")
		})
	})
}

// deleteMenu asks before the session is gone for good.
func (s *session) deleteMenu(a *App) {
	switch {
	case s.id == "":
		a.Say("No session to delete yet")
		return
	case s.stepping():
		a.Say("Delete when the session is done: wait, or /stop it")
		return
	}
	id, name := s.id, s.id
	if s.info.Name != "" {
		name = s.info.Name
	}
	m := &Menu{Items: []MenuItem{
		{Label: "Keep it", Pick: func(*App, *MenuItem) {}},
		{Label: "Delete " + name, Hint: "for good", Pick: func(a *App, _ *MenuItem) { s.remove(a, id) }},
	}}
	a.OpenMenu("Delete the session?", m)
}

// remove deletes the session, then opens a new session screen.
func (s *session) remove(a *App, id string) {
	a.Go(func() {
		ctx, cancel := context.WithTimeout(context.Background(), callTime)
		defer cancel()
		err := s.c.Delete(ctx, id, via)
		a.Post(func() {
			if err != nil {
				a.SayError(err)
				return
			}
			delete(s.list.drafts, id)
			s.list.infos = slices.DeleteFunc(s.list.infos, func(in harness.Info) bool { return in.ID == id })
			a.Say("Deleted session " + id)
			if s.ctx.Err() == nil {
				s.fresh(a)
			}
		})
	})
}

// copyBefore makes a session of this one's records before record n, then
// opens it with input in its input. When the input was taken from this
// session's, a failure gives it back.
func (s *session) copyBefore(a *App, n int, input []elem, taken bool) {
	id := s.id
	a.Go(func() {
		ctx, cancel := context.WithTimeout(context.Background(), callTime)
		defer cancel()
		to, err := s.c.Create(ctx, harness.Create{From: id, N: n}, via)
		a.Post(func() {
			if err != nil {
				a.SayError(err)
				switch _, kept := s.list.drafts[id]; {
				case !taken:
				case s.ctx.Err() == nil && len(s.input.els) == 0:
					s.input.insert(input...)
				case s.ctx.Err() != nil && !kept:
					s.list.drafts[id] = input // the screen moved on: back in its draft
				}
				return
			}
			if len(input) > 0 {
				s.list.drafts[to] = input
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

// withProviders runs then with the catalog and the providers the harness
// serves.
func (s *session) withProviders(a *App, then func(cat *catalog.Catalog, ps []harness.ProviderInfo)) {
	cat := s.list.cat
	if cat == nil {
		a.SayError(errors.New("the catalog is not loaded"))
		return
	}
	a.Go(func() {
		ctx, cancel := context.WithTimeout(context.Background(), callTime)
		defer cancel()
		ps, err := s.c.Providers(ctx)
		a.Post(func() {
			if s.ctx.Err() != nil {
				return
			}
			if err != nil {
				a.SayError(err)
				return
			}
			then(cat, ps)
		})
	})
}

// models is the menu of the catalog's models whose provider the harness
// serves, by provider. Picking one makes the next session from its preset.
func (s *session) models(a *App) {
	s.withProviders(a, func(cat *catalog.Catalog, ps []harness.ProviderInfo) {
		m := &Menu{}
		cur := s.base().Preset
		for _, id := range connected(cat, ps) {
			p := cat.Presets[id]
			hint := tokens(p.ContextLimit)
			if id == cur {
				hint = "current · " + hint
			}
			m.Items = append(m.Items, MenuItem{Label: p.Name, Hint: hint, Section: cat.Providers[p.Provider].Name, Pick: func(a *App, _ *MenuItem) { s.useModel(a, presetConf(id, p)) }})
		}
		if len(m.Items) == 0 {
			a.Say("No provider is connected: /providers")
			return
		}
		a.OpenMenu("Model for a new session", m)
	})
}

// providerSections orders /providers: subscriptions first, then each kind
// of provider, the ones people use most first. A provider the catalog adds
// later is under More, by name, until it is placed here.
var providerSections = []struct {
	name string
	ids  []string
}{
	{"Subscriptions", []string{"claude-code", "chatgpt", "opencode-go"}},
	{"Frontier labs", []string{"anthropic", "openai", "google", "meta", "xai", "deepseek", "moonshot", "zai", "minimax", "xiaomi"}},
	{"Clouds", []string{"bedrock"}},
	{"Gateways", []string{"openrouter"}},
}

// providers is the menu of the catalog's providers, in providerSections, a
// group once. Picking one asks for its key, and the harness serves it, and
// each provider that takes the same key, with the catalog's shape and url.
func (s *session) providers(a *App) {
	s.withProviders(a, func(cat *catalog.Catalog, ps []harness.ProviderInfo) {
		type entry struct{ section, id string }
		var order []entry
		placed := map[string]bool{}
		for _, sec := range providerSections {
			for _, id := range sec.ids {
				if _, ok := cat.Providers[id]; ok {
					order = append(order, entry{sec.name, id})
					for _, k := range cat.Keyed(id) {
						placed[k] = true
					}
				}
			}
		}
		byName := func(a, b string) int {
			return strings.Compare(strings.ToLower(cat.Providers[a].Name), strings.ToLower(cat.Providers[b].Name))
		}
		for _, id := range slices.SortedFunc(maps.Keys(cat.Providers), byName) {
			if !placed[id] && cat.Keyed(id)[0] == id {
				order = append(order, entry{"More", id})
			}
		}
		m := &Menu{}
		for _, e := range order {
			id := e.id
			p, hint := cat.Providers[id], ""
			if slices.ContainsFunc(ps, func(x harness.ProviderInfo) bool { return x.Name == id }) {
				hint = "connected"
			}
			pick := func(a *App, _ *MenuItem) { s.keyDialog(a, cat, id) }
			if _, ok := provider.Logins[p.Shape]; ok {
				pick = func(a *App, _ *MenuItem) { s.signin(a, id, p) }
			}
			m.Items = append(m.Items, MenuItem{Label: p.Name, Hint: hint, Section: e.section, Pick: pick})
		}
		a.OpenMenu("Providers", m)
	})
}

// signinStarting is the dialog's words until the harness shows the login's URL.
const signinStarting = "Starting the sign-in..."

// signin runs the login of a provider that takes no key, in a dialog that
// opens at once. Its words are for a user whose browser is on another
// machine: open the URL there and paste back the address it lands on. When
// this machine shows a browser, the URL opens in it, and catch serves the
// URL's loopback callback here, where the browser lands, so the login ends
// with no paste. Esc ends the login here.
func (s *session) signin(a *App, id string, p catalog.Provider) {
	ctx, cancel := context.WithTimeout(context.Background(), signinTime)
	words, in := &Node{}, NewInput()
	in.Cursor = true
	show := func(md string) {
		sb, b := NewScrollbox(), &textBlock{md}
		sb.Append(b)
		sb.Show(b)
		words.View = sized{sb, b, a}
	}
	show(signinStarting)
	field := &Node{View: in, Hidden: true}
	l := a.Dialog("Sign in to "+p.Name, Box(&Node{Dir: Col}, words, &Node{Fixed: 1}, field))
	l.Esc = cancel
	in.OnSubmit = func(a *App, text string) {
		in.Clear()
		a.Go(func() {
			if err := s.c.SignInPaste(ctx, id, strings.TrimSpace(text)); err != nil {
				a.Post(func() { a.SayError(err) })
			}
		})
	}
	a.Go(func() {
		defer cancel()
		st, err := s.c.SignIn(ctx, id, p.Shape, p.URL)
		if err == nil {
			go catch(ctx, st.URL, func(text string) error { return s.c.SignInPaste(ctx, id, text) })
			openBrowser(st.URL)
			a.Post(func() {
				md := st.Text + "\n\n```\n" + st.URL + "\n```"
				if st.Code != "" {
					md += "\n\nCode: `" + st.Code + "`"
				}
				show(md)
				field.Hidden, in.Placeholder = !st.Paste, landing(st.URL)
			})
			err = s.c.SignInWait(ctx, id)
		}
		a.Post(func() {
			a.Pop(l)
			switch {
			case errors.Is(ctx.Err(), context.Canceled): // closed with Esc
			case err != nil:
				a.SayError(err)
			default:
				s.home, s.homeAsked = nil, false
				a.Say("Connected " + p.Name)
			}
		})
	})
}

// sized is a Scrollbox of one block, as tall as the block: words in a
// dialog, to select and copy as in the transcript.
type sized struct {
	*Scrollbox
	b *textBlock
	a *App
}

func (s sized) Height(w int) int { return len(s.b.Rows(s.a, w)) }

// landing is the shape of the address the browser lands on after the login
// at url: its loopback callback with a code and state.
func landing(url string) string {
	u, err := neturl.Parse(url)
	if err != nil {
		return ""
	}
	return u.Query().Get("redirect_uri") + "?code=...&state=..."
}

// catch serves the loopback callback that url redirects to, until ctx ends,
// and pastes the address the browser lands on. A harness on this machine may
// hold the port itself, and then the browser reaches it directly.
func catch(ctx context.Context, url string, paste func(string) error) {
	u, err := neturl.Parse(url)
	if err != nil {
		return
	}
	cb, err := neturl.Parse(u.Query().Get("redirect_uri"))
	if err != nil || cb.Scheme != "http" {
		return
	}
	ln, err := net.Listen("tcp", cb.Host)
	if err != nil {
		return
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != cb.Path {
			http.NotFound(w, r)
			return
		}
		land := *cb
		land.RawQuery = r.URL.RawQuery
		if err := paste(land.String()); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		_, _ = fmt.Fprint(w, "Mainplane has the sign-in. Close this tab and return to the terminal.")
	})}
	go func() { <-ctx.Done(); _ = srv.Close() }()
	_ = srv.Serve(ln)
}

// openBrowser opens url in the browser of this machine, unless it has no
// screen to show it: an ssh session, or Linux without a display.
func openBrowser(url string) bool {
	var c *exec.Cmd
	switch {
	case os.Getenv("SSH_CONNECTION") != "":
		return false
	case runtime.GOOS == "darwin":
		c = exec.Command("open", url)
	case runtime.GOOS == "windows":
		c = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "":
		return false
	default:
		c = exec.Command("xdg-open", url)
	}
	if c.Start() != nil {
		return false
	}
	go func() { _ = c.Wait() }()
	return true
}

// keyDialog asks for the provider's key, and a bedrock provider's region,
// which its endpoint is built from.
func (s *session) keyDialog(a *App, cat *catalog.Catalog, id string) {
	p := cat.Providers[id]
	ask(a, "Key for "+p.Name, "API key", func(a *App, key string) {
		if p.Shape != "bedrock" {
			s.connect(a, cat, id, "", key)
			return
		}
		ask(a, "Region for "+p.Name, "us-east-1", func(a *App, region string) { s.connect(a, cat, id, region, key) })
	})
}

// ask is a dialog of one line of text.
func ask(a *App, title, placeholder string, then func(a *App, text string)) *Layer {
	in := NewInput()
	in.Placeholder, in.Cursor = placeholder, true
	var l *Layer
	in.OnSubmit = func(a *App, text string) {
		a.Pop(l)
		then(a, strings.TrimSpace(text))
	}
	l = a.Dialog(title, &Node{View: in})
	return l
}

// connect sets the key on provider id and each that takes it.
func (s *session) connect(a *App, cat *catalog.Catalog, id, region, key string) {
	a.Go(func() {
		ctx, cancel := context.WithTimeout(context.Background(), callTime)
		defer cancel()
		var errs []error
		for _, k := range cat.Keyed(id) {
			p := cat.Providers[k]
			errs = append(errs, s.c.SetProvider(ctx, k, p.Shape, p.URL, region, p.Session, key))
		}
		err := errors.Join(errs...)
		a.Post(func() {
			if err != nil {
				a.SayError(err)
				return
			}
			s.home, s.homeAsked = nil, false
			a.Say("Connected " + cat.Providers[id].Name)
		})
	})
}

// config is the menu of the next session's settings, a section each, and
// the full config as JSON. A change makes the next session: on a session,
// it continues in a new one.
func (s *session) config(a *App) {
	if s.id != "" && s.info.ID != s.id {
		a.Say("The session is still loading")
		return
	}
	s.withProviders(a, func(cat *catalog.Catalog, ps []harness.ProviderInfo) {
		cf := s.base()
		if s.id == "" && s.cfg == nil {
			d, err := defaultConf(cat, ps, s.list.infos)
			if err != nil {
				a.SayError(err)
				return
			}
			cf = d
		}
		m := &Menu{Checks: true, Single: true}
		pr, ok := cat.Presets[cf.Preset]
		for _, st := range pr.Settings {
			section := strings.ToUpper(st.Name[:1]) + st.Name[1:] // effort is Effort
			for _, v := range st.Values {
				m.Items = append(m.Items, MenuItem{Label: v.Name, Section: section, On: holds(cf.Params, v.Params), Pick: func(a *App, _ *MenuItem) {
					cf.Params = merge(cf.Params, v.Params)
					if s.id != "" {
						a.Pop(m.layer)
					}
					s.reconfig(a, cf, s.price)
				}})
			}
		}
		m.Items = append(m.Items, MenuItem{Label: "Edit full JSON", Pick: func(a *App, _ *MenuItem) { s.configJSON(a, cat, cf) }})
		title := cf.Model
		if ok {
			title = pr.Name
		}
		a.OpenMenu("Config: "+title, m)
	})
}

// fullConf is the config as /config's JSON shows it: all of it but the
// workers, which /workers sets, and with the price, which the user keeps
// right when editing it.
type fullConf struct {
	*statefile.Conf
	Workers []statefile.Worker `json:"workers,omitempty"` // hides the config's: never shown, ignored if typed
	Cost    catalog.Cost       `json:"cost"`
}

// configJSON edits the next session's config as JSON.
func (s *session) configJSON(a *App, cat *catalog.Catalog, cf statefile.Conf) {
	p := price(cat, cf)
	if s.price != nil {
		p = *s.price
	}
	b, err := json.MarshalIndent(fullConf{Conf: &cf, Cost: p}, "", "  ")
	if err != nil {
		a.SayError(err)
		return
	}
	in := NewInput()
	in.Cursor, in.MaxRows = true, max(1, a.H-8)
	in.insert(textElems(string(b))...)
	var l *Layer
	in.OnSubmit = func(a *App, text string) {
		next := statefile.Conf{Workers: cf.Workers}
		fc := fullConf{Conf: &next}
		dec := json.NewDecoder(strings.NewReader(text))
		dec.DisallowUnknownFields()
		err := dec.Decode(&fc)
		if err == nil && dec.More() {
			err = errors.New("the config is one JSON object, with nothing after it")
		}
		if err != nil {
			in.insert(textElems(text)...) // back to fix: a submit clears the input
			a.SayError(err)
			return
		}
		a.Pop(l)
		s.reconfig(a, next, &fc.Cost)
	}
	l = a.Dialog("Config JSON · enter saves, shift+enter new line", &Node{View: in})
}

// reconfig makes cf at price p the next session's config: this screen's
// when it has no session yet, else a new screen's that continues this one.
func (s *session) reconfig(a *App, cf statefile.Conf, p *catalog.Cost) {
	if s.id == "" {
		s.cfg, s.price = &cf, p
		return
	}
	s.continueAs(a, cf, p)
}

// base is the config a new session starts from: this session's, the one
// chosen for this screen, else the newest session's, which the default
// takes its preset from.
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
		s.cfg, s.price = &m, nil
		return
	}
	to := newSession(a, s.list, "")
	to.cfg = &m
	s.carry(to)
	s.switchTo(a, to)
}

// halt is /stop: Esc Esc, for whoever looks for it by name. A session that
// is not working still has its environments and jobs killed.
func (s *session) halt(a *App) {
	if s.stepping() {
		s.stop(a)
		return
	}
	if s.id == "" {
		a.Say("No session to stop yet")
		return
	}
	id := s.id
	a.Go(func() {
		ctx, cancel := context.WithTimeout(context.Background(), callTime)
		defer cancel()
		err := s.c.Stop(ctx, id, via)
		a.Post(func() {
			if s.ctx.Err() != nil { // the screen left the session
				return
			}
			if err != nil {
				a.SayError(err)
				return
			}
			a.Say("Stopped: the session's connected workers were told to kill its environments and jobs")
		})
	})
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
		network, nerr := mesh.Status()
		worker := !errors.Is(nerr, mesh.ErrNoWorker)
		if !worker {
			nerr = nil
		}
		a.Post(func() {
			if s.ctx.Err() != nil {
				return
			}
			if err = errors.Join(err, nerr); err != nil {
				a.SayError(err)
			}
			m := s.infoItems(a, hv, listed)
			if s.c.Local != "" {
				m.Items = append(m.Items, MenuItem{Label: "Harness on this machine", Hint: s.c.Local, Section: "Network", Pick: func(a *App, _ *MenuItem) { a.Copy(s.c.Local) }})
			}
			if s.c.Mesh != "" {
				m.Items = append(m.Items, MenuItem{Label: "Harness on the mesh", Hint: s.c.Mesh, Section: "Network", Pick: func(a *App, _ *MenuItem) { a.Copy(s.c.Mesh) }})
			}
			if !worker {
				m.Items = append(m.Items, MenuItem{Label: "This machine", Hint: "not a worker", Section: "Network"})
			}
			for _, l := range strings.Split(strings.TrimSpace(network), "\n") {
				// a row is columns two spaces apart: a name, then its value; other lines are said whole
				label, value, _ := strings.Cut(strings.TrimSpace(l), "  ")
				value = strings.Join(strings.Fields(value), " ")
				if label != "" {
					m.Items = append(m.Items, MenuItem{Label: label, Hint: cut(value), Section: "Network", Pick: func(a *App, _ *MenuItem) { a.Copy(cmp.Or(value, label)) }})
				}
			}
			a.OpenMenu("Session info", m)
		})
	})
}

// cut is a value as /info shows it: its end, when it is long, as the end
// of a URL tells more.
func cut(value string) string {
	if rs := []rune(value); len(rs) > infoHint {
		return "…" + string(rs[len(rs)-infoHint+1:])
	}
	return value
}

// infoItems is the rows of /info.
func (s *session) infoItems(a *App, harnessV string, listed []connector.Worker) *Menu {
	m := &Menu{}
	add := func(section, label, value string) {
		if value == "" {
			return
		}
		m.Items = append(m.Items, MenuItem{Label: label, Hint: cut(value), Section: section, Pick: func(a *App, _ *MenuItem) { a.Copy(value) }})
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
	add("App", "Harness", cmp.Or(s.c.URL, s.c.Local))
	add("App", "Harness version", harnessV)
	add("App", "App version", version.V)
	add("App", "Terminal", fmt.Sprintf("%dx%d, kitty keys %v, truecolor %v", a.W, a.H, a.T.Caps.Kitty, a.T.Caps.Truecolor))
	return m
}
