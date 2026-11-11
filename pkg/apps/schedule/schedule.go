// Package schedule is the built-in timer and cron app.
package schedule

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

	"github.com/mainplane-ai/mainplane/pkg/statefile"
)

// maxBody bounds a timer creation payload.
const maxBody = 64 << 10

// horizon bounds searching for the next matching cron minute (5 years covers leap years).
const horizon = 5 * 366 * 24 * 60

//go:embed skill.md
var Skill string

//go:embed app.json
var ManifestJSON []byte

// Host is what schedule needs from the harness.
type Host interface {
	AgentHasApp(agentID, app string) bool
	PostText(ctx context.Context, agentID, via, text string) (session string, n int, err error)
}

// Job is one persisted timer or cron entry.
type Job struct {
	ID      string    `json:"id"`
	Agent   string    `json:"agent"`
	Text    string    `json:"text"`
	Cron    string    `json:"cron,omitempty"`
	At      time.Time `json:"at,omitzero"`
	NextRun time.Time `json:"next_run"`
	Created time.Time `json:"created"`
}

// App runs persistent timers stored in dataDir/jobs.json.
type App struct {
	path string
	host Host
	mu   sync.Mutex
	jobs []*Job
	wake chan struct{}
}

func New(dataDir string, host Host) (*App, error) {
	a := &App{
		path: filepath.Join(dataDir, "jobs.json"),
		host: host,
		wake: make(chan struct{}, 1),
	}
	b, err := os.ReadFile(a.path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if len(b) > 0 {
		if err := json.Unmarshal(b, &a.jobs); err != nil {
			return nil, err
		}
	}
	return a, nil
}

func (a *App) signal() {
	select {
	case a.wake <- struct{}{}:
	default:
	}
}

func (a *App) saveLocked() error {
	b, err := json.MarshalIndent(a.jobs, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(a.path), 0o700); err != nil {
		return err
	}
	tmp := a.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(b)
	if err := errors.Join(werr, f.Sync(), f.Close()); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, a.path)
}

// Start runs the background timer loop until ctx ends.
func (a *App) Start(ctx context.Context) {
	for {
		wait := a.fireDue(ctx)
		if wait < 0 {
			select {
			case <-ctx.Done():
				return
			case <-a.wake:
			}
			continue
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-a.wake:
			timer.Stop()
		case <-timer.C:
		}
	}
}

type dueItem struct {
	id    string
	agent string
	text  string
}

func (a *App) collectDueLocked(now time.Time) []dueItem {
	var due []dueItem
	changed := false
	kept := a.jobs[:0]
	for _, j := range a.jobs {
		if now.Before(j.NextRun) {
			kept = append(kept, j)
			continue
		}
		if a.host == nil || !a.host.AgentHasApp(j.Agent, "schedule") {
			changed = true
			continue
		}
		kept = append(kept, j)
		due = append(due, dueItem{id: j.ID, agent: j.Agent, text: j.Text})
	}
	a.jobs = kept
	if changed {
		if err := a.saveLocked(); err != nil {
			log.Printf("schedule: save: %v", err)
		}
	}
	return due
}

func advanceCronJob(j *Job, ref time.Time) bool {
	if j.Cron == "" {
		return false
	}
	spec, err := parseCron(j.Cron)
	if err != nil {
		return false
	}
	next, ok := spec.next(ref)
	if !ok {
		return false
	}
	j.NextRun = next
	return true
}

func (a *App) applyDueResultsLocked(now time.Time, delivered, drop map[string]bool) time.Time {
	if len(delivered) > 0 || len(drop) > 0 {
		ref := time.Now().UTC()
		if now.After(ref) {
			ref = now
		}
		kept := a.jobs[:0]
		for _, j := range a.jobs {
			switch {
			case drop[j.ID]:
			case delivered[j.ID]:
				if advanceCronJob(j, ref) {
					kept = append(kept, j)
				}
			default:
				kept = append(kept, j)
			}
		}
		a.jobs = kept
		if err := a.saveLocked(); err != nil {
			log.Printf("schedule: save: %v", err)
		}
	}
	var earliest time.Time
	for _, j := range a.jobs {
		if earliest.IsZero() || j.NextRun.Before(earliest) {
			earliest = j.NextRun
		}
	}
	return earliest
}

func (a *App) fireDue(ctx context.Context) time.Duration {
	now := time.Now().UTC()
	a.mu.Lock()
	due := a.collectDueLocked(now)
	a.mu.Unlock()

	delivered := map[string]bool{}
	drop := map[string]bool{}
	retrySoon := false
	for _, d := range due {
		if ctx.Err() != nil {
			break
		}
		_, _, err := a.host.PostText(ctx, d.agent, "app:schedule", d.text)
		switch {
		case err == nil:
			delivered[d.id] = true
		case errors.Is(err, fs.ErrNotExist):
			drop[d.id] = true
		default:
			log.Printf("schedule: post to %s: %v", d.agent, err)
			retrySoon = true
		}
	}

	a.mu.Lock()
	earliest := a.applyDueResultsLocked(now, delivered, drop)
	a.mu.Unlock()

	if earliest.IsZero() {
		return -1
	}
	wait := max(time.Until(earliest), 0)
	if retrySoon && wait < time.Second {
		wait = time.Second
	}
	return wait
}

func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	agent := r.Header.Get("X-Mainplane-Agent")
	if agent == "" {
		http.Error(w, "agent context required", http.StatusForbidden)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/")
	switch {
	case r.Method == http.MethodGet && path == "":
		a.handleList(w, agent)
	case r.Method == http.MethodPost && path == "":
		a.handleCreate(w, r, agent)
	case r.Method == http.MethodDelete && path != "" && !strings.Contains(path, "/"):
		a.handleDelete(w, agent, path)
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

func (a *App) handleList(w http.ResponseWriter, agent string) {
	a.mu.Lock()
	out := []Job{}
	for _, j := range a.jobs {
		if j.Agent == agent {
			out = append(out, *j)
		}
	}
	a.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func (a *App) handleCreate(w http.ResponseWriter, r *http.Request, agent string) {
	var in struct {
		Text string `json:"text"`
		At   string `json:"at"`
		In   string `json:"in"`
		Cron string `json:"cron"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxBody)).Decode(&in); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(in.Text) == "" {
		http.Error(w, "text is required", http.StatusBadRequest)
		return
	}
	now := time.Now().UTC()
	j, err := buildJob(agent, in.Text, in.At, in.In, in.Cron, now)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	a.mu.Lock()
	a.jobs = append(a.jobs, j)
	err = a.saveLocked()
	a.mu.Unlock()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.signal()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(j)
}

func buildJob(agent, text, atStr, inStr, cronStr string, now time.Time) (*Job, error) {
	set := 0
	for _, v := range []string{atStr, inStr, cronStr} {
		if v != "" {
			set++
		}
	}
	if set != 1 {
		return nil, errors.New("set exactly one of at, in, or cron")
	}
	j := &Job{ID: statefile.NewID(), Agent: agent, Text: text, Created: now}
	switch {
	case inStr != "":
		d, err := time.ParseDuration(inStr)
		if err != nil || d <= 0 {
			return nil, fmt.Errorf("bad duration %q", inStr)
		}
		j.At = now.Add(d)
		j.NextRun = j.At
	case atStr != "":
		t, err := time.Parse(time.RFC3339, atStr)
		if err != nil {
			return nil, fmt.Errorf("bad RFC3339 timestamp %q: %w", atStr, err)
		}
		j.At = t.UTC()
		j.NextRun = j.At
	case cronStr != "":
		spec, err := parseCron(cronStr)
		if err != nil {
			return nil, err
		}
		next, ok := spec.next(now)
		if !ok {
			return nil, fmt.Errorf("cron %q has no upcoming run", cronStr)
		}
		j.Cron = cronStr
		j.NextRun = next
	}
	return j, nil
}

func (a *App) handleDelete(w http.ResponseWriter, agent, id string) {
	a.mu.Lock()
	before := len(a.jobs)
	a.jobs = slices.DeleteFunc(a.jobs, func(j *Job) bool { return j.ID == id && j.Agent == agent })
	removed := len(a.jobs) < before
	var err error
	if removed {
		err = a.saveLocked()
	}
	a.mu.Unlock()
	if !removed {
		http.Error(w, "job not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.signal()
	w.WriteHeader(http.StatusNoContent)
}

type cronSpec struct {
	min, hour, dom, mon, dow uint64
	domWild, dowWild         bool
}

func parseCron(s string) (cronSpec, error) {
	f := strings.Fields(s)
	if len(f) != 5 {
		return cronSpec{}, fmt.Errorf("cron %q must have 5 fields (min hour dom mon dow)", s)
	}
	minB, err := parseCronField(f[0], 0, 59, false)
	if err != nil {
		return cronSpec{}, err
	}
	hourB, err := parseCronField(f[1], 0, 23, false)
	if err != nil {
		return cronSpec{}, err
	}
	domB, err := parseCronField(f[2], 1, 31, false)
	if err != nil {
		return cronSpec{}, err
	}
	monB, err := parseCronField(f[3], 1, 12, false)
	if err != nil {
		return cronSpec{}, err
	}
	dowB, err := parseCronField(f[4], 0, 7, true)
	if err != nil {
		return cronSpec{}, err
	}
	return cronSpec{
		min:     minB,
		hour:    hourB,
		dom:     domB,
		mon:     monB,
		dow:     dowB,
		domWild: f[2] == "*",
		dowWild: f[4] == "*",
	}, nil
}

func parseCronField(field string, lo, hi int, sunday7 bool) (uint64, error) {
	var bits uint64
	for part := range strings.SplitSeq(field, ",") {
		b, err := parseCronPart(part, lo, hi, sunday7)
		if err != nil {
			return 0, err
		}
		bits |= b
	}
	return bits, nil
}

func parseCronPart(part string, lo, hi int, sunday7 bool) (uint64, error) {
	base, stepStr, hasStep := strings.Cut(part, "/")
	step := 1
	if hasStep {
		n, err := strconv.Atoi(stepStr)
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("bad cron step %q", part)
		}
		step = n
	}
	start, end := lo, hi
	if base != "*" {
		aStr, bStr, hasRange := strings.Cut(base, "-")
		a, err := strconv.Atoi(aStr)
		if err != nil || a < lo || a > hi {
			return 0, fmt.Errorf("bad cron value %q", part)
		}
		start, end = a, a
		if hasRange {
			b, err := strconv.Atoi(bStr)
			if err != nil || b < a || b > hi {
				return 0, fmt.Errorf("bad cron range %q", part)
			}
			end = b
		} else if hasStep {
			end = hi
		}
	}
	var bits uint64
	for v := start; v <= end; v += step {
		pos := v
		if sunday7 && pos == 7 {
			pos = 0
		}
		bits |= 1 << uint(pos)
	}
	return bits, nil
}

func (c cronSpec) next(after time.Time) (time.Time, bool) {
	t := after.UTC().Truncate(time.Minute).Add(time.Minute)
	for range horizon {
		domMatch := c.dom&(1<<uint(t.Day())) != 0
		dowMatch := c.dow&(1<<uint(t.Weekday())) != 0
		dayMatch := domMatch && dowMatch
		if !c.domWild && !c.dowWild {
			dayMatch = domMatch || dowMatch
		}
		if c.mon&(1<<uint(t.Month())) != 0 &&
			dayMatch &&
			c.hour&(1<<uint(t.Hour())) != 0 &&
			c.min&(1<<uint(t.Minute())) != 0 {
			return t, true
		}
		t = t.Add(time.Minute)
	}
	return time.Time{}, false
}
