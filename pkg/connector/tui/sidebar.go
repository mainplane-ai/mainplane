package tui

import (
	"context"
	"fmt"
	"time"

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
	// sessionRows is one session in the sidebar: three rows and a gap.
	sessionRows = 4
	// listTop is the rows above the first session: New session and a gap.
	listTop = 2
	// hoverShade is how far a hovered session's panel goes toward the text color.
	hoverShade = 0.06
)

// sessionList is the sessions, newest updated first, read every
// sessionsEvery. It outlives the session screens: the sidebar shows it and
// Ctrl+Up/Down step through it.
type sessionList struct {
	c      connector.Client
	cur    *session // the screen showing now
	infos  []harness.Info
	seen   map[string]time.Time // per session, its update when last read here
	loaded bool
	top    int  // the first row the sidebar shows
	reveal bool // scroll the open session into view at the next draw
}

// newSessionList starts reading the sessions. A session is unread once it
// changes after the first read, so a fresh start shows none unread.
func newSessionList(a *App, c connector.Client) *sessionList {
	l := &sessionList{c: c, seen: map[string]time.Time{}}
	a.Go(func() {
		for {
			ctx, cancel := context.WithTimeout(context.Background(), callTime)
			infos, err := c.Sessions(ctx, "", sessionsMax)
			cancel()
			a.Post(func() { l.update(a, infos, err) })
			time.Sleep(sessionsEvery)
		}
	})
	return l
}

func (l *sessionList) update(a *App, infos []harness.Info, err error) {
	if err != nil {
		a.SayError(err)
		return
	}
	for _, in := range infos {
		if !l.loaded || in.ID == l.cur.id {
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

// leave marks the open session read as it is now, as its screen goes.
func (l *sessionList) leave() {
	if s := l.cur; s.id != "" && s.info.ID == s.id {
		l.seen[s.id] = s.info.Updated
	}
}

// tick animates while any session steps.
func (l *sessionList) tick(a *App) {
	on := l.cur.stepping()
	for i := range l.infos {
		on = on || l.info(i).Status == statefile.StatusStepping
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

// row is what the sidebar shows at screen row y: -1 New session, a
// session's index, or -2 for none.
func (l *sessionList) row(y int, r Rect) int {
	at := l.top + y - r.Y
	switch {
	case !r.Has(r.X, y):
		return -2
	case at == 0:
		return -1
	case at >= listTop && (at-listTop)%sessionRows < sessionRows-1 && (at-listTop)/sessionRows < len(l.infos):
		return (at - listTop) / sessionRows
	}
	return -2
}

func (l *sessionList) maxTop(h int) int { return max(0, listTop+len(l.infos)*sessionRows-1-h) }

func (l *sessionList) Mouse(a *App, m term.Mouse, r Rect) bool {
	switch {
	case m.Button == term.WheelUp:
		l.top = max(0, l.top-sessionRows)
	case m.Button == term.WheelDown:
		l.top = min(l.top+sessionRows, l.maxTop(r.H))
	case m.Action == term.Press && m.Button == term.Left:
		switch i := l.row(m.Y, r); {
		case i == -1:
			l.cur.fresh(a)
		case i >= 0:
			l.open(a, l.infos[i].ID)
		}
	case m.Action == term.Motion && m.Button == term.NoButton:
	default:
		return false
	}
	return true
}

func (l *sessionList) Draw(a *App, c *Canvas, r Rect) {
	if i := l.index(l.cur.id); l.reveal && i >= 0 {
		y := listTop + i*sessionRows
		l.top = min(max(l.top, y+sessionRows-1-r.H), y)
		l.reveal = false
	}
	l.top = min(l.top, l.maxTop(r.H))
	muted, text := a.Theme.muted(), a.Theme.text()
	hover := -2
	if r.Has(a.MouseX, a.MouseY) {
		hover = l.row(a.MouseY, r)
	}
	x, w := r.X+2, r.W-4
	line := func(y int, gs []Glyph) { (&Glyphs{gs}).Draw(a, c, Rect{x, y, w, 1}) }
	mark := func(y, h int, fg term.Color, shade bool) {
		if shade {
			c.Fill(Rect{r.X, y, r.W, h}, a.Theme.Panel.Blend(a.Theme.Text, hoverShade))
		}
		for i := range h {
			c.Set(r.X, y+i, "┃", 1, term.Style{FG: fg})
		}
	}
	y := r.Y - l.top
	if hover == -1 {
		mark(y, 1, a.Theme.Muted, true)
	}
	line(y, run("+ New session", text))
	for i := range l.infos {
		y := r.Y - l.top + listTop + i*sessionRows
		if y+sessionRows <= r.Y || y >= r.Y+r.H {
			continue
		}
		in := l.info(i)
		switch {
		case in.ID == l.cur.id:
			mark(y, sessionRows-1, a.Theme.Accent, hover == i)
		case hover == i:
			mark(y, sessionRows-1, a.Theme.Muted, true)
		}
		state := run(age(time.Since(in.Updated)), muted)
		if in.Status == statefile.StatusFailed {
			state = run("failed", term.Style{FG: a.Theme.Error})
		}
		if l.unread(in) {
			state = cat(state, run(" ●", term.Style{FG: a.Theme.Accent}))
		}
		if in.Status == statefile.StatusStepping {
			state = run(spinner[l.cur.frame%len(spinner)]+" Working", text)
		}
		used := tokens(in.ContextUsed)
		if lim := in.Config.ContextLimit; lim > 0 {
			used += "/" + tokens(lim)
		}
		line(y, spread(state, run(in.ID, muted), w))
		line(y+1, clip(run(in.Name, term.Style{FG: a.Theme.Text, Attr: term.Bold}), w))
		line(y+2, spread(run(in.Config.Model, muted), run(used, muted), w))
	}
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
