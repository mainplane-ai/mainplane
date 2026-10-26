package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/mod/semver"

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

// updateWait bounds the wait for the harness to come back on a new release:
// it downloads one binary and restarts.
const updateWait = 3 * time.Minute

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

// askHarness asks what the harness does not know yet: whether to update to its
// latest release, unless the user skipped that release, and data sharing
// permission, unless it was answered, DO_NOT_TRACK is set, or the harness is
// a local build. The sharing question comes first. Enter says yes, Esc no.
func askHarness(a *App, c connector.Client) {
	a.Go(func() {
		s, err := getSettings(c)
		if err != nil {
			return
		}
		l := s.Version.Latest
		a.Post(func() {
			if newer(l, s.Version.Current) && l != skipped() {
				question(a, "Mainplane "+l+" is available",
					"Update now? The app opens again on "+l+" when it is done. You can update anytime in /settings.",
					"update", "not now", func(a *App) { update(a, c, l) }, func() { skip(a, l) })
			}
			if dnt := os.Getenv("DO_NOT_TRACK"); s.DataSharing == nil && s.Version.Current != "dev" && (dnt == "" || dnt == "0" || dnt == "false") {
				question(a, sharingTitle, sharingWords, "allow", "don't allow",
					func(a *App) { setSharing(a, c, true) }, func() { setSharing(a, c, false) })
			}
		})
	})
}

// question is a dialog of words that runs yes on Enter and no on Esc.
func question(a *App, title, words, yesLabel, noLabel string, yes func(*App), no func()) {
	key, muted := term.Style{FG: a.Theme.Text, Attr: term.Bold}, a.Theme.muted()
	var l *Layer
	l = a.Dialog(title, Box(&Node{Dir: Col, View: onEnter(func(a *App) {
		a.Pop(l)
		yes(a)
	})},
		&Node{View: &Text{S: words, Style: a.Theme.text()}}, &Node{Fixed: 1},
		&Node{View: &Glyphs{Gs: cat(run("enter", key), run(" "+yesLabel+"   ", muted), run("esc", key), run(" "+noLabel, muted))}}))
	l.Esc = no
}

// newer is whether release latest is newer than current; a local build is
// never behind.
func newer(latest, current string) bool {
	return semver.IsValid(latest) && semver.IsValid(current) && semver.Compare(latest, current) > 0
}

// skippedPath holds the release the user said not now to. The update
// question comes again for a newer one.
func skippedPath() (string, error) {
	h, err := os.UserHomeDir()
	return filepath.Join(h, ".mainplane", "update-skipped"), err
}

func skipped() string {
	p, err := skippedPath()
	if err != nil {
		return ""
	}
	b, _ := os.ReadFile(p)
	return strings.TrimSpace(string(b))
}

func skip(a *App, v string) {
	p, err := skippedPath()
	if err == nil {
		err = os.MkdirAll(filepath.Dir(p), 0o700)
	}
	if err == nil {
		err = os.WriteFile(p, []byte(v+"\n"), 0o600)
	}
	if err != nil {
		a.SayError(err)
	}
}

// update makes the harness release v and waits until it answers on v. Then
// the app ends to become v too, and main opens it again.
func update(a *App, c connector.Client, v string) {
	a.Say("Updating Mainplane to " + v)
	a.Go(func() {
		// A success restarts the harness, so the call seldom gets its answer:
		// it ends with no error, a network error, or a proxy's 502 or 52x.
		// The harness's own failures are other codes.
		err := postSettings(c, map[string]string{"version": v})
		if se := (*connector.StatusError)(nil); !errors.As(err, &se) || se.Code == http.StatusBadGateway || se.Code >= 520 {
			err = waitFor(c, v)
		}
		a.Post(func() {
			if err != nil {
				a.SayError(err)
				return
			}
			a.Update = v
			a.Quit()
		})
	})
}

// waitFor polls the harness until it is release v. A harness that restarted
// may be at a new URL.
func waitFor(c connector.Client, v string) error {
	for end := time.Now().Add(updateWait); time.Now().Before(end); time.Sleep(2 * time.Second) {
		ctx, cancel := context.WithTimeout(context.Background(), callTime)
		got, err := c.Version(ctx)
		if err != nil {
			_ = c.Find(ctx)
		}
		cancel()
		if err == nil && got == v {
			return nil
		}
	}
	return fmt.Errorf("Mainplane did not come back on %s within %s", v, updateWait)
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
			if l := st.Version.Latest; newer(l, st.Version.Current) {
				v.Hint = "Update to " + l
				v.Pick = func(a *App, _ *MenuItem) { update(a, s.c, l) }
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
