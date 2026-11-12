package tui

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/mainplane-ai/mainplane/pkg/catalog"
	"github.com/mainplane-ai/mainplane/pkg/connector"
	"github.com/mainplane-ai/mainplane/pkg/connector/tui/term"
	"github.com/mainplane-ai/mainplane/pkg/harness"
	"github.com/mainplane-ai/mainplane/pkg/statefile"
)

const (
	// sessionsEvery is how often the list of sessions is read: a session
	// another client starts or ends shows within a few seconds.
	sessionsEvery = 3 * time.Second
	sessionsMax   = 100
	// sessionRows is one session in the sidebar: two rows and a gap.
	sessionRows = 3
	// How far a session's panel goes toward the text color: the open one
	// stands out more than the one under the mouse.
	openShade  = 0.07
	hoverShade = 0.04
	// The cache mark blinks in the cache's last minute, faster in its last
	// 10 s: a message sent then still reads the prompt from the cache.
	blinkFrom, blinkFastFrom   = time.Minute, 10 * time.Second
	blinkEvery, blinkFastEvery = time.Second, 300 * time.Millisecond
)

// sessionList is the sessions, newest updated first, read every
// sessionsEvery. It outlives the session screens: the sidebar shows it and
// Ctrl+Up/Down step through it. An agent's sessions are left out, by the
// harness so the list is sessionsMax plain ones, unless agents is set, in
// /settings: agents are other connectors' to show.
type sessionList struct {
	c      *connector.Client
	cat    *catalog.Catalog // nil until loaded
	cur    *session         // the screen showing now
	infos  []harness.Info
	agents atomic.Bool          // show the sessions of agents too: set on the app's loop, read by each read
	seen   map[string]time.Time // per session, its update when last read here
	drafts map[string][]elem    // per session, "" for home, the input it was left with
	loaded bool
	top    int  // the first row the sidebar shows
	reveal bool // scroll the open session into view at the next draw
}

// newSessionList starts reading the sessions and loads the catalog. A session is unread once it
// changes after the first read, so a fresh start shows none unread.
func newSessionList(a *App, c *connector.Client) *sessionList {
	l := &sessionList{c: c, seen: map[string]time.Time{}, drafts: map[string][]elem{}}
	loadCatalog(a, l)
	a.Go(func() {
		for {
			l.read(a)
			time.Sleep(sessionsEvery)
		}
	})
	return l
}

// read lists the sessions once.
func (l *sessionList) read(a *App) {
	ctx, cancel := context.WithTimeout(context.Background(), callTime)
	agents := l.agents.Load()
	infos, err := l.c.Sessions(ctx, "", sessionsMax, !agents)
	cancel()
	a.Post(func() { l.update(a, infos, agents, err) })
}

// update takes a read of the sessions, agents whether it asked for those of
// agents too: a read from before /settings changed that is dropped. An
// agent's session is in the list while it is the open one, asked for or
// not. One is unread only once it changes after it first shows here, as
// those hidden until now have not.
func (l *sessionList) update(a *App, infos []harness.Info, agents bool, err error) {
	if err != nil {
		a.SayError(err)
		return
	}
	if agents != l.agents.Load() {
		return
	}
	theirs := func(in harness.Info) bool { return strings.HasPrefix(in.Via, harness.AgentVia) }
	if cur := l.cur.info; !agents && theirs(cur) && !slices.ContainsFunc(infos, func(in harness.Info) bool { return in.ID == cur.ID }) {
		i, _ := slices.BinarySearchFunc(infos, cur.Updated, func(in harness.Info, t time.Time) int { return t.Compare(in.Updated) })
		infos = slices.Insert(infos, i, cur)
	}
	for _, in := range infos {
		if _, known := l.seen[in.ID]; !l.loaded || in.ID == l.cur.id || !known && theirs(in) {
			l.seen[in.ID] = in.Updated
		}
	}
	l.infos, l.loaded = infos, true
	l.tick(a)
}

// info is the session at i, the open one as its screen last read it.
func (l *sessionList) info(i int) harness.Info {
	if in := l.infos[i]; in.ID != l.cur.id || l.cur.info.ID != in.ID {
		return in
	}
	return l.cur.info
}

// unread is a session that changed since it was last read here, and is done.
func (l *sessionList) unread(in harness.Info) bool {
	return in.ID != l.cur.id && in.Status != statefile.StatusStepping && in.Updated.After(l.seen[in.ID])
}

// leave marks the open session read as it is now, and keeps its input, as
// its screen goes.
func (l *sessionList) leave() {
	s := l.cur
	if s.id != "" && s.info.ID == s.id {
		l.seen[s.id] = s.info.Updated
	}
	if len(s.input.els) > 0 {
		l.drafts[s.id] = s.input.els
	} else {
		delete(l.drafts, s.id)
	}
}

// tick animates while any session steps or its cache mark blinks.
func (l *sessionList) tick(a *App) {
	now := time.Now()
	on := l.cur.stepping() || l.cur.blank && !a.Blurred || blinks(l.cur.info.Cache, now)
	for i := range l.infos {
		in := l.info(i)
		on = on || in.Status == statefile.StatusStepping || blinks(in.Cache, now)
	}
	a.Ticking(on)
}

func (l *sessionList) index(id string) int {
	for i, in := range l.infos {
		if in.ID == id {
			return i
		}
	}
	return -1
}

// step opens the session n places down the list from the open one; from
// home, the first.
func (l *sessionList) step(a *App, n int) {
	if len(l.infos) == 0 {
		return
	}
	i := l.index(l.cur.id)
	switch {
	case i < 0:
		i = 0
	default:
		i = min(max(0, i+n), len(l.infos)-1)
	}
	l.open(a, l.infos[i].ID)
}

func (l *sessionList) open(a *App, id string) {
	if id != l.cur.id {
		l.reveal = true
		l.cur.switchTo(a, newSession(a, l, id))
	}
}

// row is the session at screen row y, or -1.
func (l *sessionList) row(y int, r Rect) int {
	at := l.top + y - r.Y
	if !r.Has(r.X, y) || at%sessionRows == sessionRows-1 || at/sessionRows >= len(l.infos) {
		return -1
	}
	return at / sessionRows
}

func (l *sessionList) maxTop(h int) int { return max(0, len(l.infos)*sessionRows-1-h) }

func (l *sessionList) Mouse(a *App, m term.Mouse, r Rect) bool {
	switch {
	case m.Button == term.WheelUp:
		l.top = max(0, l.top-sessionRows)
	case m.Button == term.WheelDown:
		l.top = min(l.top+sessionRows, l.maxTop(r.H))
	case m.Action == term.Press && m.Button == term.Left:
		if i := l.row(m.Y, r); i >= 0 {
			l.open(a, l.infos[i].ID)
		}
	case m.Action == term.Motion && m.Button == term.NoButton:
	default:
		return false
	}
	return true
}

// Draw shows each session as a title, its name or else its id, with its
// state on the right, over the model with the context on the right. Hovering a named session
// shows its id in place of the model.
func (l *sessionList) Draw(a *App, c *Canvas, r Rect) {
	if i := l.index(l.cur.id); l.reveal && i >= 0 {
		y := i * sessionRows
		l.top = min(max(l.top, y+sessionRows-1-r.H), y)
		l.reveal = false
	}
	l.top = min(l.top, l.maxTop(r.H))
	muted := a.Theme.muted()
	hover := -1
	if r.Has(a.MouseX, a.MouseY) {
		hover = l.row(a.MouseY, r)
	}
	x, w := r.X+2, r.W-4
	line := func(y int, gs []Glyph) { (&Glyphs{gs}).Draw(a, c, Rect{x, y, w, 1}) }
	mark := func(y int, fg term.Color, shade float64) {
		c.Fill(Rect{r.X, y, r.W, sessionRows - 1}, a.Theme.Panel.Blend(a.Theme.Text, shade))
		for i := range sessionRows - 1 {
			c.Set(r.X, y+i, "┃", 1, term.Style{FG: fg})
		}
	}
	for i := range l.infos {
		y := r.Y - l.top + i*sessionRows
		if y+sessionRows <= r.Y || y >= r.Y+r.H {
			continue
		}
		in := l.info(i)
		switch {
		case in.ID == l.cur.id:
			mark(y, a.Theme.Accent, openShade)
		case hover == i:
			mark(y, a.Theme.Muted, hoverShade)
		}
		state := run(age(time.Since(in.Updated)), muted)
		if in.Status == statefile.StatusFailed {
			state = run("failed", term.Style{FG: a.Theme.Error})
		}
		if l.unread(in) {
			state = cat(state, run(" ●", term.Style{FG: a.Theme.Accent}))
		}
		if in.Status == statefile.StatusStepping {
			state = run(Spin()+" Working", a.Theme.text())
		}
		title, meta := in.Name, in.Config.Model
		if title == "" {
			title = in.ID
		} else if hover == i {
			meta = in.ID
		}
		used := tokens(in.ContextUsed)
		if lim := in.Config.ContextLimit; lim > 0 {
			used += "/" + tokens(lim)
		}
		line(y, spread(run(title, term.Style{FG: a.Theme.Text, Attr: term.Bold}), state, w))
		right := run(used, muted)
		if mark := cacheMark(a, in.Cache, time.Now()); mark != nil {
			right = cat(right, run(" ", muted), mark)
		}
		line(y+1, spread(run(meta, muted), right, w))
	}
}

// cacheMark is the mark beside the context while the session's prompt is
// cached: filled where the vendor keeps it for certain, hollow where only
// likely. Muted like the row it sits in, steady until the cache's last
// minute and blinking then, nil once it is cold. In the off half of a blink
// a blank keeps its place.
func cacheMark(a *App, c harness.Cache, now time.Time) []Glyph {
	left := c.Until.Sub(now)
	every := blinkEvery
	if left <= blinkFastFrom {
		every = blinkFastEvery
	}
	switch {
	case left <= 0:
		return nil
	case left <= blinkFrom && left%every < every/2:
		return run(" ", a.Theme.muted())
	case c.Likely:
		return run("◇", a.Theme.muted())
	}
	return run("◆", a.Theme.muted())
}

// blinks is a cache in its last minute, whose mark needs the tick.
func blinks(c harness.Cache, now time.Time) bool {
	left := c.Until.Sub(now)
	return left > 0 && left <= blinkFrom
}

// age is how long ago, in its largest unit: now, 15m, 3h, 2d.
func age(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}
