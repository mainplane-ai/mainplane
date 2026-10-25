package tui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/mainplane-ai/mainplane/pkg/connector"
	"github.com/mainplane-ai/mainplane/pkg/connector/tui/term"
)

// The data sharing question. sharingText names its version, which the harness
// keeps with the answer.
const (
	sharingTitle = "Help improve Mainplane"
	sharingWords = "Allow the use of your sessions to improve Mainplane and train AI models. Change anytime in /settings."
	sharingText  = "b1"
)

type settings struct {
	Version struct {
		Current string `json:"current"`
		Latest  string `json:"latest"`
	} `json:"version"`
	DataSharing *struct {
		Share bool   `json:"share"`
		Via   string `json:"via"`
	} `json:"data_sharing"`
}

func getSettings(c connector.Client) (settings, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTime)
	defer cancel()
	var s settings
	return s, c.Call(ctx, http.MethodGet, "/settings", "", nil, &s)
}

// postSettings is bounded by the harness's own time for an update.
func postSettings(c connector.Client, v any) error {
	b, _ := json.Marshal(v)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	return c.Call(ctx, http.MethodPost, "/settings", "application/json", strings.NewReader(string(b)), nil)
}

// askSharing asks for data sharing permission, unless it was answered,
// DO_NOT_TRACK is set, or the harness is a local build. Enter allows, Esc
// does not.
func askSharing(a *App, c connector.Client) {
	if v := os.Getenv("DO_NOT_TRACK"); v != "" && v != "0" && v != "false" {
		return
	}
	a.Go(func() {
		s, err := getSettings(c)
		if err != nil || s.DataSharing != nil || s.Version.Current == "dev" {
			return
		}
		a.Post(func() {
			key, muted := term.Style{FG: a.Theme.Text, Attr: term.Bold}, a.Theme.muted()
			var l *Layer
			l = a.Dialog(sharingTitle, Box(&Node{Dir: Col, View: onEnter(func(a *App) {
				a.Pop(l)
				setSharing(a, c, true)
			})},
				&Node{View: &Text{S: sharingWords, Style: a.Theme.text()}}, &Node{Fixed: 1},
				&Node{View: &Glyphs{Gs: cat(run("enter", key), run(" allow   ", muted), run("esc", key), run(" don't allow", muted))}}))
			l.Esc = func() { setSharing(a, c, false) }
		})
	})
}

// onEnter runs on Enter.
type onEnter func(*App)

func (onEnter) Draw(*App, *Canvas, Rect) {}

func (f onEnter) Key(a *App, k term.Key) bool {
	if k.String() != "enter" {
		return false
	}
	f(a)
	return true
}

func setSharing(a *App, c connector.Client, share bool) {
	a.Go(func() {
		err := postSettings(c, map[string]any{"data_sharing": map[string]any{"share": share, "via": via, "text": sharingText}})
		a.Post(func() {
			if err != nil {
				a.SayError(err)
			}
		})
	})
}

// settingsMenu is the harness's version, with an update to the latest, and
// whether it shares data.
func (s *session) settingsMenu(a *App) {
	a.Go(func() {
		st, err := getSettings(s.c)
		a.Post(func() {
			if err != nil {
				a.SayError(err)
				return
			}
			m := &Menu{}
			v := MenuItem{Section: "Version", Label: st.Version.Current, Hint: "Latest"}
			if l := st.Version.Latest; l != "" && l != st.Version.Current {
				v.Hint = "Update to " + l
				v.Pick = func(a *App, _ *MenuItem) {
					a.Say("Updating to " + l)
					a.Go(func() {
						// A success restarts the harness, so it never answers.
						err := postSettings(s.c, map[string]string{"version": l})
						var se *connector.StatusError
						a.Post(func() {
							if errors.As(err, &se) {
								a.SayError(err)
							} else {
								a.Say("The harness is restarting")
							}
						})
					})
				}
			}
			share := st.DataSharing != nil && st.DataSharing.Share
			u := MenuItem{Section: sharingTitle, Label: "Off", Hint: "Turn on"}
			if share {
				u.Label, u.Hint = "On", "Turn off"
			}
			if st.DataSharing != nil && st.DataSharing.Via == "DO_NOT_TRACK" {
				u.Label, u.Hint = "Off: DO_NOT_TRACK is set", ""
			} else {
				u.Pick = func(a *App, _ *MenuItem) { setSharing(a, s.c, !share) }
			}
			m.Items = []MenuItem{v, u}
			a.OpenMenu("Settings", m)
		})
	})
}
