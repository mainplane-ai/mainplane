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
// #65757A darkened to #607075 to reach WCAG AA, 4.5:1, on the panel. Dark is
// near black, oklch 0.120 0.0060 220, its panel 0.035 lighter at the same chroma;
// on it the blue and gray are lifted to read.
var (
	dark = Theme{
		BG: term.Hex("#040607"), Panel: term.Hex("#090d0e"),
		Text: term.Hex("#f8f8f5"), Muted: term.Hex("#8b9ca1"), Accent: term.Hex("#26a9c6"),
		Selection: term.Hex("#1c4e5c"), Error: term.Hex("#e8787a"),
	}
	light = Theme{
		BG: term.Hex("#f8f8f5"), Panel: term.Hex("#eef1f0"),
		Text: term.Hex("#172428"), Muted: term.Hex("#607075"), Accent: term.Hex("#0087a4"),
		Selection: term.Hex("#c4e3ea"), Error: term.Hex("#c4363a"),
	}
)

func themeFor(isDark bool) Theme {
	if isDark {
		return dark
	}
	return light
}

func (t Theme) text() term.Style  { return term.Style{FG: t.Text} }
func (t Theme) muted() term.Style { return term.Style{FG: t.Muted} }
