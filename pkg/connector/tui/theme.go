package tui

import "github.com/mainplane-ai/mainplane/pkg/connector/tui/term"

// Theme is the colors every component takes its look from. Panels are set
// apart by shade, not by lines.
type Theme struct {
	BG        term.Color // the screen
	Panel     term.Color // raised: sidebar, dialogs
	Element   term.Color // the input
	Text      term.Color
	Muted     term.Color
	Accent    term.Color
	Selection term.Color
	Error     term.Color
}

// The palette is the brand's, from design/identity: Blue #0087A4, Gray
// #65757A, Ink #172428, Paper #F8F8F5, line #DCE1DE, all near one hue, 220.
// Dark surfaces are Ink and lighter shades of its hue; on them the blue and
// gray are lifted to read.
var (
	dark = Theme{
		BG: term.Hex("#172428"), Panel: term.Hex("#1e2f34"), Element: term.Hex("#26393f"),
		Text: term.Hex("#f8f8f5"), Muted: term.Hex("#8b9ca1"), Accent: term.Hex("#26a9c6"),
		Selection: term.Hex("#1c4e5c"), Error: term.Hex("#e8787a"),
	}
	light = Theme{
		BG: term.Hex("#f8f8f5"), Panel: term.Hex("#eef1f0"), Element: term.Hex("#dce1de"),
		Text: term.Hex("#172428"), Muted: term.Hex("#65757a"), Accent: term.Hex("#0087a4"),
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
