package tui

import "github.com/mainplane-ai/mainplane/pkg/connector/tui/term"

// Theme is the colors every component takes its look from. Panels are set
// apart by shade, not by lines.
type Theme struct {
	BG        term.Color // the screen
	Panel     term.Color // raised: sidebar, dialogs, toasts
	Element   term.Color // the input
	Text      term.Color
	Muted     term.Color
	Accent    term.Color
	Selection term.Color
	Error     term.Color
}

var (
	dark = Theme{
		BG: term.Hex("#0a0a0a"), Panel: term.Hex("#141414"), Element: term.Hex("#1e1e1e"),
		Text: term.Hex("#eeeeee"), Muted: term.Hex("#808080"), Accent: term.Hex("#fab283"),
		Selection: term.Hex("#264f78"), Error: term.Hex("#e06c75"),
	}
	light = Theme{
		BG: term.Hex("#ffffff"), Panel: term.Hex("#f3f3f3"), Element: term.Hex("#e8e8e8"),
		Text: term.Hex("#1a1a1a"), Muted: term.Hex("#8a8a8a"), Accent: term.Hex("#c4682b"),
		Selection: term.Hex("#add6ff"), Error: term.Hex("#d1383d"),
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
