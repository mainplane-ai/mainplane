package tui

import (
	"encoding/json"
	"fmt"
	"mime"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/mainplane-ai/mainplane/pkg/connector/tui/term"
	"github.com/mainplane-ai/mainplane/pkg/statefile"
)

// The transcript shows messages, text, errors, and each call with its
// result. Start, config, system, name, thinking and step records are not
// shown.

// run is s as glyphs in one style.
func run(s string, st term.Style) []Glyph {
	var gs []Glyph
	term.Graphemes(s, func(g string, w int) { gs = append(gs, Glyph{G: g, W: w, St: st}) })
	return gs
}

// deco is s as glyphs that are drawn but never copied.
func deco(s string, st term.Style) []Glyph {
	gs := run(s, st)
	for i := range gs {
		gs[i].Deco = true
	}
	return gs
}

func gwidth(gs []Glyph) int {
	w := 0
	for _, g := range gs {
		w += g.W
	}
	return w
}

func cat(parts ...[]Glyph) []Glyph {
	var gs []Glyph
	for _, p := range parts {
		gs = append(gs, p...)
	}
	return gs
}

// flow wraps gs into rows w wide: the first row after first, the rest after
// rest, which should be as wide.
func flow(gs []Glyph, w int, first, rest []Glyph) []Line {
	avail := max(1, w-gwidth(first))
	var rows []Line
	for i, r := range wrap(len(gs), func(i int) int { return gs[i].W }, func(i int) bool { return gs[i].G == " " }, func(int) bool { return false }, avail) {
		pre := first
		if i > 0 {
			pre = rest
		}
		rows = append(rows, Line{Gs: cat(pre, gs[r[0]:r[1]]), Cont: i > 0})
	}
	return rows
}

// clip cuts gs to w cells, ending in an ellipsis when it cuts.
func clip(gs []Glyph, w int) []Glyph {
	if gwidth(gs) <= w {
		return gs
	}
	x := 0
	for i, g := range gs {
		if x+g.W > w-1 {
			st := term.Style{}
			if i > 0 {
				st = gs[i-1].St
			}
			return cat(gs[:i], deco("…", st))
		}
		x += g.W
	}
	return gs
}

// spread puts right at the right edge of w cells, after left cut to fit.
func spread(left, right []Glyph, w int) []Glyph {
	if len(right) == 0 {
		return clip(left, w)
	}
	right = clip(right, w)
	left = clip(left, max(0, w-gwidth(right)-2))
	return cat(left, deco(strings.Repeat(" ", max(0, w-gwidth(left)-gwidth(right))), term.Style{}), right)
}

// clean is text as the transcript draws it: no carriage returns, tabs as
// four spaces.
func clean(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\t", "    ")
}

func lines(s string) []string { return strings.Split(strings.TrimRight(clean(s), "\n"), "\n") }

func isText(typ string) bool {
	mt, _, _ := mime.ParseMediaType(typ)
	return typ == "" || strings.HasPrefix(mt, "text/") || mt == "application/json"
}

// media is a body that is not text, as its type and size.
func media(r statefile.Record) string {
	mt, _, _ := mime.ParseMediaType(r.Type)
	return mt + " · " + size(len(r.Body))
}

func size(n int) string {
	switch {
	case n < 1000:
		return fmt.Sprintf("%d B", n)
	case n < 1000*1000:
		return fmt.Sprintf("%d KB", n/1000)
	}
	return fmt.Sprintf("%.1f MB", float64(n)/1e6)
}

func dur(d time.Duration) string {
	switch {
	case d < time.Second:
		return fmt.Sprintf("%.1fs", d.Seconds())
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}

// stamp is when, as the time of day, with the date when it is not today.
func stamp(t time.Time) string {
	t = t.Local()
	if y, m, d := t.Date(); y == time.Now().Year() && m == time.Now().Month() && d == time.Now().Day() {
		return t.Format("15:04")
	}
	return t.Format("Jan 2 15:04")
}

// textBlock is the agent's text, as markdown.
type textBlock struct{ s string }

func (b *textBlock) Rows(a *App, w int) []Line { return markdown(a, b.s, w) }

var (
	heading  = regexp.MustCompile(`^#{1,6}\s+(.*)$`)
	rule     = regexp.MustCompile(`^\s*(-{3,}|\*{3,}|_{3,})\s*$`)
	listItem = regexp.MustCompile(`^(\s*)([-*+]|\d+[.)])\s+(.*)$`)
	quote    = regexp.MustCompile(`^>\s?(.*)$`)
)

// markdown is a small subset: headings, bold, inline code, lists, quotes,
// rules and fenced code. Tables and the rest show as written.
func markdown(a *App, s string, w int) []Line {
	text, muted := a.Theme.text(), a.Theme.muted()
	var rows []Line
	code := false
	pad := deco(" ", term.Style{})
	ls := lines(s)
	for i := 0; i < len(ls); i++ {
		l := ls[i]
		if !code && strings.HasPrefix(strings.TrimSpace(l), "|") {
			j := i
			for j < len(ls) && strings.HasPrefix(strings.TrimSpace(ls[j]), "|") {
				j++
			}
			rows = append(rows, table(a, ls[i:j], w)...)
			i = j - 1
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(l), "```") {
			code = !code
			rows = append(rows, Line{BG: a.Theme.Panel})
			continue
		}
		if code {
			for _, r := range flow(run(l, text), w-1, pad, pad) {
				r.BG = a.Theme.Panel
				rows = append(rows, r)
			}
			continue
		}
		h, q, li := heading.FindStringSubmatch(l), quote.FindStringSubmatch(l), listItem.FindStringSubmatch(l)
		switch {
		case strings.TrimSpace(l) == "":
			rows = append(rows, Line{})
		case rule.MatchString(l):
			rows = append(rows, Line{Gs: deco(strings.Repeat("─", w), muted)})
		case h != nil:
			st := text
			st.Attr |= term.Bold
			rows = append(rows, flow(inline(a, h[1], st), w, nil, nil)...)
		case q != nil:
			bar := deco("│ ", muted)
			rows = append(rows, flow(inline(a, q[1], muted), w, bar, bar)...)
		case li != nil:
			m := li
			mark := m[2]
			if !strings.ContainsAny(mark[:1], "0123456789") {
				mark = "•"
			}
			first := run(m[1]+mark+" ", text)
			rows = append(rows, flow(inline(a, m[3], text), w, first, deco(strings.Repeat(" ", gwidth(first)), text))...)
		default:
			rows = append(rows, flow(inline(a, l, text), w, nil, nil)...)
		}
	}
	return rows
}

var tableRule = regexp.MustCompile(`^\s*\|?(\s*:?-+:?\s*\|)+\s*:?-*:?\s*$`)

// table lines up the cells of a markdown table in columns three spaces
// apart, the header in bold over a rule. A table too wide for w shows as
// written.
func table(a *App, ls []string, w int) []Line {
	cells, head := tableCells(a, ls)
	var widths []int
	for _, row := range cells {
		for i, c := range row {
			if i == len(widths) {
				widths = append(widths, 0)
			}
			widths[i] = max(widths[i], gwidth(c))
		}
	}
	total := 3 * (len(widths) - 1)
	for _, cw := range widths {
		total += cw
	}
	if total > w {
		var rows []Line
		for _, l := range ls {
			rows = append(rows, flow(run(l, a.Theme.text()), w, nil, nil)...)
		}
		return rows
	}
	var rows []Line
	for r, row := range cells {
		var gs []Glyph
		for i, c := range row {
			if i > 0 {
				gs = append(gs, run("   ", term.Style{})...)
			}
			gs = append(gs, c...)
			if i < len(row)-1 {
				gs = append(gs, run(strings.Repeat(" ", widths[i]-gwidth(c)), term.Style{})...)
			}
		}
		rows = append(rows, Line{Gs: gs})
		if r == 0 && head {
			rows = append(rows, Line{Gs: deco(strings.Repeat("─", total), a.Theme.muted())})
		}
	}
	return rows
}

// tableCells is the table's rows of cells, and whether a rule follows the
// first, making it the header, in bold.
func tableCells(a *App, ls []string) (cells [][][]Glyph, head bool) {
	for _, l := range ls {
		if tableRule.MatchString(l) {
			head = head || len(cells) == 1
			continue
		}
		var row [][]Glyph
		for _, c := range strings.Split(strings.Trim(strings.TrimSpace(l), "|"), "|") {
			row = append(row, inline(a, strings.TrimSpace(c), a.Theme.text()))
		}
		cells = append(cells, row)
	}
	if head {
		for _, c := range cells[0] {
			for k := range c {
				c[k].St.Attr |= term.Bold
			}
		}
	}
	return cells, head
}

// inline styles **bold** and `code` and drops their marks; a mark with no
// partner on the line stays as written.
func inline(a *App, s string, base term.Style) []Glyph {
	var gs []Glyph
	bold, code := false, false
	for s != "" {
		if s[0] == '`' && (code || strings.Contains(s[1:], "`")) {
			code, s = !code, s[1:]
			continue
		}
		if !code && strings.HasPrefix(s, "**") && (bold || strings.Contains(s[2:], "**")) {
			bold, s = !bold, s[2:]
			continue
		}
		n := len(s)
		if i := strings.IndexAny(s[1:], "`*"); i >= 0 {
			n = i + 1
		}
		st := base
		if code {
			st.FG = a.Theme.Accent
		}
		if bold {
			st.Attr |= term.Bold
		}
		gs = append(gs, run(s[:n], st)...)
		s = s[n:]
	}
	return gs
}

// msgBlock is a message on a panel after an accent bar, where it came from
// and when at the top right.
type msgBlock struct{ r statefile.Record }

func (b *msgBlock) Rows(a *App, w int) []Line {
	bar := deco("┃", term.Style{FG: a.Theme.Accent})
	pre := cat(bar, deco(" ", term.Style{}))
	meta := deco("via "+b.r.Via+" · "+stamp(b.r.Time)+" ", a.Theme.muted())
	rows := []Line{{Gs: cat(bar, spread(nil, meta, w-1))}}
	body := []string{"[" + media(b.r) + "]"}
	if isText(b.r.Type) {
		body = lines(string(b.r.Body))
	}
	for _, l := range body {
		rows = append(rows, flow(run(l, a.Theme.text()), w-1, pre, pre)...)
	}
	rows = append(rows, Line{Gs: bar})
	for i := range rows {
		rows[i].BG, rows[i].BGFrom = a.Theme.Panel, 1
	}
	return rows
}

// errBlock is an error after a bar, both in the error color.
type errBlock struct{ s string }

func (b *errBlock) Rows(a *App, w int) []Line {
	st := term.Style{FG: a.Theme.Error}
	pre := deco("┃ ", st)
	var rows []Line
	for _, l := range lines(b.s) {
		rows = append(rows, flow(run(l, st), w, pre, pre)...)
	}
	return rows
}

// callBlock is a call and its result: two rows, the call and the result's
// line, or every line of both when open. A click opens and closes it.
type callBlock struct {
	s    *session
	call statefile.Record // zero for a result with no call
	res  *statefile.Record
	name string
	args map[string]any
	open bool
}

func newCall(s *session, r statefile.Record) *callBlock {
	var c struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	_ = json.Unmarshal(r.Body, &c) // a call that does not decode shows its name empty
	return &callBlock{s: s, call: r, name: c.Name, args: c.Arguments}
}

func (b *callBlock) Click(*App) { b.open = !b.open }

func (b *callBlock) arg(k string) string { s, _ := b.args[k].(string); return s }

var patchFile = regexp.MustCompile(`(?m)^\*\*\* (?:Add|Update|Delete) File: (.+)$`)

// summary is the call in one line: the code's first line, or the path.
func (b *callBlock) summary() string {
	switch b.name {
	case "run":
		for _, l := range lines(b.arg("code")) {
			if strings.TrimSpace(l) != "" {
				return strings.TrimSpace(l)
			}
		}
		return ""
	case "patch":
		var ps []string
		for _, m := range patchFile.FindAllStringSubmatch(b.arg("input"), -1) {
			ps = append(ps, strings.TrimSpace(m[1]))
		}
		return strings.Join(ps, ", ")
	}
	if p := b.arg("path"); p != "" {
		return p
	}
	rest := map[string]any{}
	for k, v := range b.args {
		if k != "worker" {
			rest[k] = v
		}
	}
	j, _ := json.Marshal(rest)
	return string(j)
}

// output is the result's text without the exit line the harness puts first.
func (b *callBlock) output() []string {
	ls := lines(string(b.res.Body))
	if b.res.Exit != nil && *b.res.Exit != 0 && len(ls) > 0 && ls[0] == "exit "+strconv.Itoa(*b.res.Exit) {
		ls = ls[1:]
	}
	return ls
}

// head is the call's tool and worker, then what follows.
func (b *callBlock) head(a *App) []Glyph {
	name := b.name
	if b.call.ID == "" {
		name = "result"
	}
	gs := run(name, term.Style{FG: a.Theme.Accent})
	if wk := b.arg("worker"); wk != "" {
		gs = cat(gs, run(" "+wk, a.Theme.muted()))
	}
	if in := b.arg("interpreter"); in != "" {
		gs = cat(gs, run(" "+in, a.Theme.muted()))
	}
	return gs
}

// meta is how long the call took, or has run so far.
func (b *callBlock) meta(a *App) []Glyph {
	switch {
	case b.call.ID == "":
		return nil
	case b.res != nil:
		return deco(dur(b.res.Time.Sub(b.call.Time)), a.Theme.muted())
	case b.s.stepping():
		return deco(dur(time.Since(b.call.Time).Truncate(time.Second)), a.Theme.muted())
	}
	return nil
}

// status is the result's one line: running, the exit and the last line for
// run, else the first line.
func (b *callBlock) status(a *App) []Glyph {
	muted := a.Theme.muted()
	switch {
	case b.res == nil && b.s.stepping():
		return cat([]Glyph{{G: "⠋", W: 1, St: muted, Spin: true}}, deco(" Running", muted))
	case b.res == nil:
		return deco("no result", muted)
	case !isText(b.res.Type):
		return run(media(*b.res), muted)
	}
	var gs []Glyph
	if b.res.Exit != nil && *b.res.Exit != 0 {
		gs = cat(run("exit "+strconv.Itoa(*b.res.Exit), term.Style{FG: a.Theme.Error}), run(" · ", muted))
	}
	ls := b.output()
	line := ""
	for i := range ls {
		l := ls[i]
		if b.name == "run" {
			l = ls[len(ls)-1-i]
		}
		if strings.TrimSpace(l) != "" {
			line = strings.TrimSpace(l)
			break
		}
	}
	if line == "" {
		return cat(gs, deco("no output", muted))
	}
	return cat(gs, run(line, muted))
}

func (b *callBlock) Rows(a *App, w int) []Line {
	muted := a.Theme.muted()
	elbow := deco("└ ", muted)
	if !b.open {
		head := b.head(a)
		if b.call.ID != "" {
			head = cat(head, run("  "+b.summary(), a.Theme.text()))
		}
		return []Line{{Gs: clip(head, w)}, {Gs: cat(elbow, spread(b.status(a), b.meta(a), w-2))}}
	}
	rows := []Line{{Gs: spread(b.head(a), b.meta(a), w)}}
	side := deco("│ ", muted)
	body := func(ls []string, st func(string) term.Style) {
		for _, l := range ls {
			rows = append(rows, flow(run(l, st(l)), w, side, side)...)
		}
	}
	plain := func(string) term.Style { return a.Theme.text() }
	diff := func(l string) term.Style {
		switch {
		case strings.HasPrefix(l, "+"):
			return term.Style{FG: a.Theme.Accent}
		case strings.HasPrefix(l, "-"):
			return term.Style{FG: a.Theme.Error}
		}
		return a.Theme.text()
	}
	switch b.name {
	case "run":
		body(lines(b.arg("code")), plain)
	case "read":
		body([]string{b.arg("path")}, plain)
	case "write":
		body(append([]string{b.arg("path")}, lines(b.arg("content"))...), plain)
	case "edit":
		var ls []string
		for _, l := range lines(b.arg("old")) {
			ls = append(ls, "- "+l)
		}
		for _, l := range lines(b.arg("new")) {
			ls = append(ls, "+ "+l)
		}
		body(append([]string{b.arg("path")}, ls...), diff)
	case "patch":
		body(lines(b.arg("input")), diff)
	default:
		if b.call.ID != "" {
			body([]string{b.summary()}, plain)
		}
	}
	if b.res == nil || !isText(b.res.Type) {
		return append(rows, Line{Gs: cat(elbow, b.status(a))})
	}
	if b.res.Exit != nil && *b.res.Exit != 0 {
		rows = append(rows, Line{Gs: cat(elbow, run("exit "+strconv.Itoa(*b.res.Exit), term.Style{FG: a.Theme.Error}))})
		elbow = deco("  ", muted)
	}
	out := b.output()
	if b.res.FullOutput != "" {
		out = append(out, "full output: "+b.res.FullOutput)
	}
	indent := deco("  ", muted)
	for i, l := range out {
		pre := elbow
		if i > 0 {
			pre = indent
		}
		rows = append(rows, flow(run(l, muted), w, pre, indent)...)
	}
	return rows
}
