package tui

import "github.com/mainplane-ai/mainplane/pkg/connector/tui/term"

// Theme is the colors every component takes its look from. Panels are set
// apart by shade, not by lines, and share one shade.
type Theme struct {
	BG        term.Color // the screen
	Panel     term.Color // raised: sidebar, dialogs, the input, the scrollbar track
	Text      term.Color
	Muted     term.Color
	Accent    term.Color
	Selection term.Color
	Error     term.Color
}

// Alexander's picks in design's tui-colors tuner, near the brand's hue, 220.
// Light is the brand's: Paper #F8F8F5, Ink #172428, Blue #0087A4, and Gray
// #65757A darkened to #59696e to reach WCAG AA, 4.5:1, on the panel. Dark is
// near black, oklch 0.120 0.0060 220; on it the blue and gray are lifted to
// read. Each panel is 0.060 in oklch lightness from its screen.
var (
	dark = Theme{
		BG: term.Hex("#040607"), Panel: term.Hex("#0e1213"),
		Text: term.Hex("#f8f8f5"), Muted: term.Hex("#8b9ca1"), Accent: term.Hex("#26a9c6"),
		Selection: term.Hex("#1c4e5c"), Error: term.Hex("#e8787a"),
	}
	light = Theme{
		BG: term.Hex("#f8f8f5"), Panel: term.Hex("#e2e5e4"),
		Text: term.Hex("#172428"), Muted: term.Hex("#59696e"), Accent: term.Hex("#0087a4"),
		Selection: term.Hex("#c4e3ea"), Error: term.Hex("#c4363a"),
	}
)

// themeFor is the theme for the terminal. With 256 colors the panel keeps a
// shade apart from the screen.
func themeFor(c term.Caps) Theme {
	t := light
	if c.Dark {
		t = dark
	}
	if !c.Truecolor {
		t.Panel = term.Apart256(t.Panel, t.BG)
	}
	return t
}

func (t Theme) text() term.Style  { return term.Style{FG: t.Text} }
func (t Theme) muted() term.Style { return term.Style{FG: t.Muted} }
