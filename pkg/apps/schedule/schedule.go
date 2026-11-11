// Package schedule is the built-in app that messages an agent later: once,
// or each time a cron expression matches. A timer names the agent, never a
// session, so it outlives rollovers, and timers are a file, so they outlive
// the harness.
package schedule

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mainplane-ai/mainplane/pkg/harness"
	"github.com/mainplane-ai/mainplane/pkg/statefile"
)

//go:embed skill.md
var Skill string

// A post that fails for a reason other than the agent being gone is tried
// again after retry: the harness may be mid-restart.
const retry = time.Minute

// A cron expression that matches no minute in this many years never fires:
// long enough for a leap day.
const horizon = 8

// Timer is one timer as the file and the routes have it.
type Timer struct {
	ID    string    `json:"id"`
	Agent string    `json:"agent"`
	Text  string    `json:"text"`
	Cron  string    `json:"cron,omitempty"`
	Next  time.Time `json:"next"`
}

type app struct {
	h    *harness.Harness
	path string

	mu     sync.Mutex
	timers []Timer
	wake   chan struct{} // a timer was added: the wait may be shorter now
}

// New loads the timers in dir, fires the due ones until ctx ends, and is the
// app's handler.
//
//	POST   /       {"text", and one of "in", "at", "cron"}  -> Timer
//	GET    /       -> the caller's [Timer]
//	DELETE /{id}   -> 204
func New(ctx context.Context, h *harness.Harness, dir string) (http.Handler, error) {
	a := &app{h: h, path: filepath.Join(dir, "timers.json"), wake: make(chan struct{}, 1)}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(a.path)
	if err == nil {
		err = json.Unmarshal(b, &a.timers)
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("schedule: %s: %w", a.path, err)
	}
	go a.run(ctx)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /{$}", a.add)
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		defer a.mu.Unlock()
		out := []Timer{}
		for _, t := range a.timers {
			if t.Agent == r.Header.Get("X-Mainplane-Agent") {
				out = append(out, t)
			}
		}
		reply(w, out)
	})
	mux.HandleFunc("DELETE /{id}", func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		defer a.mu.Unlock()
		i := slices.IndexFunc(a.timers, func(t Timer) bool {
			return t.ID == r.PathValue("id") && t.Agent == r.Header.Get("X-Mainplane-Agent")
		})
		if i < 0 {
			http.Error(w, "no such timer", http.StatusNotFound)
			return
		}
		a.timers = slices.Delete(a.timers, i, i+1)
		if err := a.save(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	return mux, nil
}

func reply(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (a *app) add(w http.ResponseWriter, r *http.Request) {
	var in struct{ Text, In, At, Cron string }
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, harness.MaxPost)).Decode(&in); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	t := Timer{ID: statefile.NewID(), Agent: r.Header.Get("X-Mainplane-Agent"), Text: in.Text, Cron: in.Cron}
	var err error
	switch {
	case in.Text == "" || t.Agent == "":
		err = errors.New("a timer has a text, and an agent to send it to")
	case in.In != "" && in.At == "" && in.Cron == "":
		var d time.Duration
		d, err = time.ParseDuration(in.In)
		t.Next = time.Now().Add(d)
	case in.At != "" && in.In == "" && in.Cron == "":
		t.Next, err = time.Parse(time.RFC3339, in.At)
	case in.Cron != "" && in.In == "" && in.At == "":
		t.Next, err = next(in.Cron, time.Now())
	default:
		err = errors.New("give exactly one of in, at, cron")
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	t.Next = t.Next.UTC()
	a.mu.Lock()
	a.timers = append(a.timers, t)
	err = a.save()
	a.mu.Unlock()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	select {
	case a.wake <- struct{}{}:
	default:
	}
	reply(w, t)
}

// save writes the timers whole, as a temporary file renamed over the old
// one. The caller holds mu.
func (a *app) save() error {
	b, err := json.MarshalIndent(a.timers, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(a.path+".tmp", b, 0o644); err != nil {
		return err
	}
	return os.Rename(a.path+".tmp", a.path)
}

// run fires each timer when it is due. A timer due while the harness was
// down fires at once. The post carries the timer and its time as a key, so
// one that landed before a crash could record it is not sent twice.
func (a *app) run(ctx context.Context) {
	for {
		now := time.Now()
		a.mu.Lock()
		due, wait := []Timer{}, time.Hour
		for _, t := range a.timers {
			if !t.Next.After(now) {
				due = append(due, t)
			} else {
				wait = min(wait, t.Next.Sub(now))
			}
		}
		a.mu.Unlock()
		for _, t := range due {
			_, _, err := a.h.PostAgent(ctx, t.Agent, "app:schedule", t.ID+"@"+t.Next.Format(time.RFC3339Nano), []harness.Part{{Type: "text/plain", Body: fmt.Appendf(nil, "[timer %s] %s", t.ID, t.Text)}})
			gone := errors.Is(err, harness.ErrNoAgent) || err == nil && t.Cron == ""
			switch {
			case gone:
			case err != nil:
				log.Printf("schedule: timer %s for agent %s: %v", t.ID, t.Agent, err)
				t.Next = now.Add(retry)
			default:
				t.Next, _ = next(t.Cron, now)
			}
			a.mu.Lock()
			if i := slices.IndexFunc(a.timers, func(o Timer) bool { return o.ID == t.ID }); i >= 0 && gone {
				a.timers = slices.Delete(a.timers, i, i+1)
			} else if i >= 0 {
				a.timers[i].Next = t.Next
			}
			if err := a.save(); err != nil {
				log.Printf("schedule: %v", err)
			}
			a.mu.Unlock()
		}
		if len(due) > 0 {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-a.wake:
		case <-time.After(wait):
		}
	}
}

// next is the first minute after t that the cron expression matches, in UTC.
func next(expr string, t time.Time) (time.Time, error) {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return t, fmt.Errorf("cron %q: five fields: minute hour day-of-month month day-of-week", expr)
	}
	var sets [5]uint64
	for i, r := range [5][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 6}} {
		var err error
		if sets[i], err = field(fields[i], r[0], r[1]); err != nil {
			return t, fmt.Errorf("cron %q: %w", expr, err)
		}
	}
	t = t.UTC().Truncate(time.Minute)
	for end := t.AddDate(horizon, 0, 0); t.Before(end); {
		t = t.Add(time.Minute)
		if sets[0]>>t.Minute()&1 == 1 && sets[1]>>t.Hour()&1 == 1 && sets[2]>>t.Day()&1 == 1 && sets[3]>>int(t.Month())&1 == 1 && sets[4]>>int(t.Weekday())&1 == 1 {
			return t, nil
		}
	}
	return t, fmt.Errorf("cron %q never matches", expr)
}

// field is the set of values one cron field matches, as bits lo through hi.
func field(f string, lo, hi int) (uint64, error) {
	var set uint64
	for _, part := range strings.Split(f, ",") {
		span, stepText, stepped := strings.Cut(part, "/")
		from, to, step := lo, hi, 1
		var err error
		if a, b, ranged := strings.Cut(span, "-"); span != "*" {
			if from, err = strconv.Atoi(a); err == nil && ranged {
				to, err = strconv.Atoi(b)
			} else if !stepped {
				to = from
			}
		}
		if err == nil && stepped {
			step, err = strconv.Atoi(stepText)
		}
		if err != nil || from < lo || to > hi || from > to || step < 1 {
			return 0, fmt.Errorf("bad field %q (%d-%d)", f, lo, hi)
		}
		for v := from; v <= to; v += step {
			set |= 1 << v
		}
	}
	return set, nil
}
