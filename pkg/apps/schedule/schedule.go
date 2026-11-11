// Package schedule is the schedule app: one-shot and cron timers that post
// to the agent that set them. A timer names the agent, not a session, so it
// lands on whichever session is live when it fires; timers are a file in the
// app's data dir, so they outlast a harness restart.
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

	"github.com/mainplane-ai/mainplane/pkg/apps"
	"github.com/mainplane-ai/mainplane/pkg/harness"
	"github.com/mainplane-ai/mainplane/pkg/statefile"
)

//go:embed skill.md
var Skill []byte

// Name is the app's, and its posts come via app:<Name>.
const Name = "schedule"

// idle is how long the loop sleeps with no timer set; a new one wakes it.
const idle = 24 * time.Hour

// retryDelay is how long a timer whose post failed waits to try again: long
// enough not to spin on a session that does not load, short next to the
// minute that is a cron line's finest step.
const retryDelay = 5 * time.Second

// Timer is one timer. Next is when it fires; a cron timer then moves Next on.
type Timer struct {
	ID    string    `json:"id"`
	Agent string    `json:"agent"`
	Text  string    `json:"text"`
	Cron  string    `json:"cron,omitempty"`
	Next  time.Time `json:"next"`
}

type schedule struct {
	h    *harness.Harness
	path string
	wake chan struct{}

	mu     sync.Mutex
	timers []Timer
	retry  map[string]time.Time // timer -> not before, after its post failed
}

// due is when t fires next, under mu.
func (s *schedule) due(t Timer) time.Time {
	if r := s.retry[t.ID]; r.After(t.Next) {
		return r
	}
	return t.Next
}

// New loads the timers and fires them under ctx: one past due at load, set
// before a restart, fires at once.
func New(ctx context.Context, h *harness.Harness, dir string) (http.Handler, error) {
	s := &schedule{h: h, path: filepath.Join(dir, "timers.json"), wake: make(chan struct{}, 1), timers: []Timer{}, retry: map[string]time.Time{}}
	b, err := os.ReadFile(s.path)
	if err == nil {
		err = json.Unmarshal(b, &s.timers)
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	go s.run(ctx)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /{$}", s.add)
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		agent := r.Header.Get(apps.AgentHeader)
		s.mu.Lock()
		mine := slices.DeleteFunc(slices.Clone(s.timers), func(t Timer) bool { return t.Agent != agent })
		s.mu.Unlock()
		reply(w, mine)
	})
	mux.HandleFunc("DELETE /{id}", func(w http.ResponseWriter, r *http.Request) {
		agent := r.Header.Get(apps.AgentHeader)
		s.mu.Lock()
		defer s.mu.Unlock()
		i := slices.IndexFunc(s.timers, func(t Timer) bool { return t.ID == r.PathValue("id") && t.Agent == agent })
		if i < 0 {
			http.Error(w, "no timer "+r.PathValue("id"), http.StatusNotFound)
			return
		}
		delete(s.retry, s.timers[i].ID)
		s.timers = slices.Delete(s.timers, i, i+1)
		if err := s.save(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	return mux, nil
}

// add is POST /: {"text"} with one of {"at": RFC 3339}, {"in": a Go
// duration} or {"cron": five fields, UTC}.
func (s *schedule) add(w http.ResponseWriter, r *http.Request) {
	var in struct{ Text, At, In, Cron string }
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, harness.MaxPost)).Decode(&in); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	t := Timer{ID: statefile.NewID(), Agent: r.Header.Get(apps.AgentHeader), Text: in.Text, Cron: in.Cron}
	var err error
	switch {
	case in.Text == "" || countSet(in.At, in.In, in.Cron) != 1:
		err = errors.New(`a timer is {"text"} and one of "at" (RFC 3339), "in" (as 90m or 2h30m) or "cron" (five fields, UTC)`)
	case in.At != "":
		t.Next, err = time.Parse(time.RFC3339, in.At)
	case in.In != "":
		var d time.Duration
		if d, err = time.ParseDuration(in.In); err == nil {
			t.Next = time.Now().Add(d)
		}
	default:
		var c cron
		if c, err = parseCron(in.Cron); err == nil {
			t.Next = c.next(time.Now())
		}
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	t.Next = t.Next.UTC()
	s.mu.Lock()
	s.timers = append(s.timers, t)
	err = s.save()
	s.mu.Unlock()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
	reply(w, t)
}

func countSet(ss ...string) int {
	n := 0
	for _, s := range ss {
		if s != "" {
			n++
		}
	}
	return n
}

// save writes the timers through a file beside them, under mu.
func (s *schedule) save() error {
	b, err := json.MarshalIndent(s.timers, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(s.path+".tmp", b, 0o600); err != nil {
		return err
	}
	return os.Rename(s.path+".tmp", s.path)
}

func (s *schedule) run(ctx context.Context) {
	for {
		s.mu.Lock()
		wait := idle
		for _, t := range s.timers {
			wait = min(wait, time.Until(s.due(t)))
		}
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		case <-time.After(wait):
		}
		s.fire(ctx)
	}
}

// fire posts every timer due. A timer whose agent is gone, or no longer has
// the app, is dropped: no one is there to read it. A timer moves on only
// once its post lands; a failed one is tried again with the same key.
func (s *schedule) fire(ctx context.Context) {
	now := time.Now()
	var due []Timer
	s.mu.Lock()
	s.timers = slices.DeleteFunc(s.timers, func(t Timer) bool {
		a, err := s.h.GetAgent(t.Agent)
		if err != nil || !slices.Contains(a.Apps, Name) {
			log.Printf("schedule: timer %s dropped: agent %s is gone or has no %s app", t.ID, t.Agent, Name)
			delete(s.retry, t.ID)
			return true
		}
		if !s.due(t).After(now) {
			due = append(due, t)
		}
		return false
	})
	s.mu.Unlock()
	for _, t := range due {
		s.mu.Lock()
		gone := !slices.ContainsFunc(s.timers, func(x Timer) bool { return x.ID == t.ID })
		s.mu.Unlock()
		if gone { // deleted while an earlier timer posted
			continue
		}
		body := fmt.Sprintf("Timer %s fired: %s", t.ID, t.Text)
		key := "schedule:" + t.ID + ":" + strconv.FormatInt(t.Next.Unix(), 10)
		_, _, err := s.h.PostAgent(ctx, t.Agent, "app:"+Name, key, []harness.Part{{Type: "text/plain", Body: []byte(body)}})
		s.mu.Lock()
		i := slices.IndexFunc(s.timers, func(x Timer) bool { return x.ID == t.ID })
		switch {
		case i < 0: // deleted while it posted
			delete(s.retry, t.ID)
		case err != nil:
			log.Printf("schedule: timer %s to agent %s: %v; trying again in %s", t.ID, t.Agent, err, retryDelay)
			s.retry[t.ID] = time.Now().Add(retryDelay)
		case t.Cron == "":
			delete(s.retry, t.ID)
			s.timers = slices.Delete(s.timers, i, i+1)
		default:
			delete(s.retry, t.ID)
			c, _ := parseCron(t.Cron) // add parsed it
			s.timers[i].Next = c.next(now)
		}
		s.mu.Unlock()
	}
	s.mu.Lock()
	if err := s.save(); err != nil {
		log.Printf("schedule: %v", err)
	}
	s.mu.Unlock()
}

func reply(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// cron is a five-field cron line as a bit set of the values each field
// takes: minute, hour, day of month, month, day of week (0 or 7 is Sunday).
type cron struct {
	bits           [5]uint64
	anyDom, anyDow bool
}

var bounds = [5][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 7}}

func parseCron(line string) (cron, error) {
	var c cron
	fields := strings.Fields(line)
	if len(fields) != 5 {
		return c, fmt.Errorf("cron %q: five fields, minute hour day-of-month month day-of-week", line)
	}
	for i, f := range fields {
		for _, part := range strings.Split(f, ",") {
			lo, hi, step, ok := field(part, bounds[i][0], bounds[i][1])
			if !ok {
				return c, fmt.Errorf("cron %q: %q is not a value, range or step in %d-%d", line, part, bounds[i][0], bounds[i][1])
			}
			for v := lo; v <= hi; v += step {
				c.bits[i] |= 1 << v
			}
		}
	}
	if c.bits[4]&(1<<7) != 0 {
		c.bits[4] |= 1
	}
	c.anyDom, c.anyDow = fields[2] == "*", fields[4] == "*"
	if c.next(time.Now()).IsZero() {
		return c, fmt.Errorf("cron %q never fires", line)
	}
	return c, nil
}

// field reads one part of a cron field, a value, a-b or *, then an optional
// /step, within min-max.
func field(part string, minV, maxV int) (lo, hi, step int, ok bool) {
	rng, stepText, stepped := strings.Cut(part, "/")
	lo, hi, step = minV, maxV, 1
	var err error
	if stepped {
		if step, err = strconv.Atoi(stepText); err != nil || step < 1 {
			return 0, 0, 0, false
		}
	}
	if rng == "*" {
		return lo, hi, step, true
	}
	a, b, ranged := strings.Cut(rng, "-")
	if lo, err = strconv.Atoi(a); err != nil {
		return 0, 0, 0, false
	}
	if ranged {
		hi, err = strconv.Atoi(b)
	} else if !stepped {
		hi = lo
	}
	return lo, hi, step, err == nil && lo >= minV && hi <= maxV && lo <= hi
}

func (c cron) has(field, v int) bool { return c.bits[field]&(1<<v) != 0 }

// day is cron's rule: with both day fields set, either one matching will do.
func (c cron) day(t time.Time) bool {
	dom, dow := c.has(2, t.Day()), c.has(4, int(t.Weekday()))
	switch {
	case c.anyDom && c.anyDow:
		return true
	case c.anyDom:
		return dow
	case c.anyDow:
		return dom
	}
	return dom || dow
}

// next is the first minute after t that c matches, in UTC; zero when there is
// none in five years, as for February 31.
func (c cron) next(t time.Time) time.Time {
	t = t.UTC().Truncate(time.Minute).Add(time.Minute)
	for end := t.AddDate(5, 0, 0); t.Before(end); {
		switch {
		case !c.has(3, int(t.Month())):
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, time.UTC)
		case !c.day(t):
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, time.UTC)
		case !c.has(1, t.Hour()):
			t = t.Truncate(time.Hour).Add(time.Hour)
		case !c.has(0, t.Minute()):
			t = t.Add(time.Minute)
		default:
			return t
		}
	}
	return time.Time{}
}
