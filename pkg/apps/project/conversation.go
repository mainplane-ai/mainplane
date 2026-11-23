package project

import (
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/mainplane-ai/mainplane/pkg/harness"
	"github.com/mainplane-ai/mainplane/pkg/statefile"
)

// talk is how many bytes of the conversation one read returns: about
// 10,000 tokens, enough for the user's recent asks and the answers to them.
const talk = 40_000

// said is one block of the conversation: a person's message, or the
// orchestrator's text in a turn a person opened. at is <session>.<n>.
type said struct{ at, head, body string }

// conversation is GET /conversation: what the user and the orchestrator said
// to each other, across the orchestrator's sessions, oldest first, as plain
// text. A turn is a person's when a person's message opened it or came in
// it; the text of turns that agents, apps or jobs opened is left out. The
// newest blocks that fit in talk are given whole, the newest one cut at its
// start when it alone is larger. ?before=<session>.<n> gives the blocks
// before that one, which the first line names when some were left out.
func (a *App) conversation(w http.ResponseWriter, r *http.Request, p Project) error {
	all, err := a.blocks(p)
	if err != nil {
		return err
	}
	if before := r.URL.Query().Get("before"); before != "" {
		i := slices.IndexFunc(all, func(s said) bool { return s.at == before })
		if i < 0 {
			return fmt.Errorf("%w: no block %q: ?before= takes the <session>.<n> an [earlier: ...] line names", fs.ErrNotExist, before)
		}
		all = all[:i]
	}
	from, size := len(all), 0
	for from > 0 && size+len(all[from-1].head)+len(all[from-1].body)+2 <= talk {
		from--
		size += len(all[from].head) + len(all[from].body) + 2
	}
	if from == len(all) && from > 0 { // the newest alone is larger: its end
		from--
		b := all[from].body[len(all[from].body)-(talk-len(all[from].head)-8):]
		for len(b) > 0 && !utf8.RuneStart(b[0]) {
			b = b[1:]
		}
		all[from].body = "[...] " + b
	}
	var out strings.Builder
	if from > 0 {
		fmt.Fprintf(&out, "[earlier: mainplane app project GET \"/conversation?before=%s\"]\n", all[from].at)
	}
	for _, s := range all[from:] {
		fmt.Fprintf(&out, "%s\n%s\n", s.head, s.body)
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, err = w.Write([]byte(out.String()))
	return err
}

// blocks is every block of the project's conversation, oldest first.
func (a *App) blocks(p Project) ([]said, error) {
	g, err := a.h.Agent(p.Orchestrator)
	if err != nil {
		return nil, err
	}
	var all []said
	user, ended, calls := false, true, false
	for _, id := range g.Sessions {
		chain, err := a.h.Sessions.Load(id)
		if errors.Is(err, fs.ErrNotExist) {
			continue // deleted through /sessions
		}
		if err != nil {
			return nil, err
		}
		// A handoff (a continue record, then the error that ends the session)
		// moved the messages no step of the session saw to the next one.
		moved, n := len(chain)+1, len(chain)
		if last := statefile.LastStep(chain); last != nil && n > 1 && chain[n-1].Kind == statefile.Error && chain[n-2].Kind == statefile.System && chain[n-2].Source == "continue" {
			moved = last.Upto
		}
		for _, rec := range chain {
			if rec.Kind == statefile.Message && rec.N > moved {
				continue
			}
			at, when := fmt.Sprintf("%s.%d", id, rec.N), rec.Time.UTC().Format("2006-01-02 15:04")
			switch rec.Kind {
			case statefile.Call:
				calls = true
			case statefile.Step:
				ended, calls = !calls, false
			case statefile.Message:
				if harness.Person(rec.Via) {
					user = true
					all = append(all, said{at, fmt.Sprintf("--- %s user (%s)", when, rec.Via), body(rec)})
				} else if ended {
					user = false
				}
				ended = false
			case statefile.Text:
				if user {
					all = append(all, said{at, fmt.Sprintf("--- %s orchestrator", when), body(rec)})
				}
			case statefile.Start, statefile.Config, statefile.System, statefile.Thinking, statefile.Result, statefile.Error, statefile.Name:
			}
		}
	}
	return all, nil
}

// body is a record's text, or for a part that is not text its type and size.
func body(r statefile.Record) string {
	if strings.HasPrefix(r.Type, "text/") {
		return strings.TrimRight(string(r.Body), "\n")
	}
	return fmt.Sprintf("[%s, %d bytes]", r.Type, len(r.Body))
}
