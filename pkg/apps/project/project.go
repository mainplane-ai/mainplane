// Package project is the built-in app that keeps projects: a name, the one
// orchestrator agent a person talks to, a status with a line for a sidebar,
// and a store of files that is the project's dashboard. A project's agents
// are its orchestrator and every agent made under it, found by who made
// whom; the harness itself knows nothing of projects.
package project

import (
	"bytes"
	"cmp"
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
	"regexp"
	"slices"
	"strings"
	"sync"
	"text/template"
	"time"
	"unicode/utf8"

	"github.com/mainplane-ai/mainplane/pkg/catalog"
	"github.com/mainplane-ai/mainplane/pkg/harness"
	"github.com/mainplane-ai/mainplane/pkg/statefile"
)

// Skill is what an agent reads before it uses the app. start is the first
// message of a project's orchestrator, and dashboard the page a new project's
// store starts with.
var (
	//go:embed skill.md
	Skill string
	//go:embed start.md
	start string
	//go:embed dashboard.html
	dashboard []byte
)

// The three states of a project: the user should look, it waits on something
// outside, nothing needs the user.
const (
	Open    = "open"
	Waiting = "waiting"
	Closed  = "closed"
)

const (
	// via is who posts a project's first message.
	via = "app:project"
	// settingUp is the line of a project until its orchestrator sets one.
	settingUp = "setting up"
	// index is the file of the store that is the dashboard's page.
	index = "index.html"
	// maxLine bounds a status line: one line of a sidebar.
	maxLine = 100
	// maxFile bounds one file of a store, so a write is held in memory.
	maxFile = 8 << 20
	// depth bounds the walk from an agent up to who made it. Nothing makes a
	// loop, since an agent is made after its maker; a file edited by hand could.
	depth = 64
	// catalogAge is how long a fetched catalog is used before it is fetched again.
	catalogAge = time.Hour
	// modelsAge is how long the models are used before the providers are
	// asked again: a sheet of the interface opens on them, and a provider
	// added to the harness should show within the minute.
	modelsAge = time.Minute
	// modelsWait is how long one provider has to list its models: a local
	// server answers in less, and one that is down must not hold the others.
	modelsWait = 2 * time.Second
	// inline is the size up to which a file comes with the list that asks
	// for bodies: a dashboard's data is many small files, and each read
	// apart is a round trip.
	inline = 16 << 10
	// Sandbox is the policy each file of a store is served with: an origin
	// of its own, and no network.
	Sandbox = "sandbox allow-scripts; default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data: blob:"
	// changed is the note an orchestrator reads when someone else set its
	// project's status. It is a system record: it wakes nobody.
	changed = "%s set the status of project %q to %s, with the line %q. This is a note, not a message: nobody waits for an answer to it."
)

// Dashboard is the policy a dashboard is shown under. Its origin is its own,
// so it reads no key of the interface, and it cannot move the page around
// it. The rest is a web page's: code of its own, what https serves, links
// that open beside it, and the design system's files at ds, sources such as
// "https://example.com/web/ds/".
func Dashboard(ds string) string {
	return "sandbox allow-scripts allow-popups allow-popups-to-escape-sandbox; default-src 'none'; script-src 'unsafe-inline' https: " + ds + "; style-src 'unsafe-inline' https: " + ds + "; font-src https: data: " + ds + "; img-src https: data: blob:; media-src https: data: blob:; connect-src https: wss:"
}

// maxStore bounds a project's whole store: a page and a thousand cards with
// a screenshot each, and no more, so the stores cannot fill the harness's
// disk. A test lowers it.
var maxStore int64 = 256 << 20

// required are the apps every orchestrator has, whatever a create asks for.
var required = []string{"message", "orchestrate", "project", "schedule"}

// A file's name is path segments of letters, digits, dot, dash and underscore.
var fileName = regexp.MustCompile(`^[A-Za-z0-9._-]+(/[A-Za-z0-9._-]+)*$`)

var first = template.Must(template.New("start").Parse(start))

// Project is <id>.json in the app's directory. Its files are in <id>/.
// Counter goes up at every write to the store, so a reader knows to list it again.
type Project struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Orchestrator string    `json:"orchestrator"`
	Status       string    `json:"status"`
	Line         string    `json:"line"`
	Updated      time.Time `json:"updated"`
	Counter      int       `json:"counter"`
}

// Model is a preset of the catalog whose provider the harness serves: a full
// config for an agent but its workers.
type Model struct {
	ID       string            `json:"id"`
	Name     string            `json:"name"`
	Provider string            `json:"provider"` // the provider's name, a section of the picker; Custom for one the catalog lacks
	Config   statefile.Conf    `json:"config"`
	Settings []catalog.Setting `json:"settings,omitempty"` // such as effort, as the TUI's /config offers them
}

// File is one file of a store as a list shows it.
type File struct {
	Name     string    `json:"name"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
	Body     []byte    `json:"body,omitempty"` // base64, of a small file when the list is asked for bodies
}

// App is the handler of the routes a session's key reaches, and the projects.
type App struct {
	http.Handler
	Catalog func(context.Context) (catalog.Catalog, error) // the published catalog; a test sets its own

	ctx context.Context
	h   *harness.Harness
	dir string

	mu       sync.Mutex // the projects and every write to a store
	projects map[string]Project

	cmu      sync.Mutex
	cat      catalog.Catalog
	catAt    time.Time
	listed   []Model
	listedAt time.Time
}

// New loads the projects in dir and is the app. An agent of a project
// reaches, with its session's key:
//
//	GET    /                the agent's project                            -> Project
//	PUT    /status          {"status","line"}                              -> Project
//	GET    /models          -> [Model]
//	GET    /files/          ?prefix=                                       -> [File]
//	GET    /files/{name}    -> the file
//	PUT    /files/{name}    the file as the body                           -> 204
//	DELETE /files/{name}    -> 204
func New(ctx context.Context, h *harness.Harness, dir string) (*App, error) {
	a := &App{Catalog: catalog.Fetch, ctx: ctx, h: h, dir: dir, projects: map[string]Project{}}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok {
			continue
		}
		var p Project
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err == nil {
			err = json.Unmarshal(b, &p)
		}
		if err == nil && p.ID != id {
			err = fmt.Errorf("it holds project %q", p.ID)
		}
		if err != nil {
			return nil, fmt.Errorf("project: %s: %w", e.Name(), err)
		}
		a.projects[id] = p
	}
	mux := http.NewServeMux()
	// mine is a route for the project the calling agent is in.
	mine := func(pattern string, fn func(w http.ResponseWriter, r *http.Request, p Project, agent string) error) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			agent := r.Header.Get("X-Mainplane-Agent")
			p, ok := a.of(agent)
			if !ok {
				http.Error(w, "agent "+agent+" is in no project: a project's agents are its orchestrator and the agents made under it", http.StatusNotFound)
				return
			}
			answer(w, fn(w, r, p, agent))
		})
	}
	mine("GET /{$}", func(w http.ResponseWriter, r *http.Request, p Project, _ string) error { return reply(w, p) })
	mine("PUT /status", func(w http.ResponseWriter, r *http.Request, p Project, agent string) error {
		return a.status(w, r, p.ID, "Agent "+agent, harness.AgentVia+agent, agent)
	})
	mux.HandleFunc("GET /models", a.models)
	mine("GET /files/{name...}", func(w http.ResponseWriter, r *http.Request, p Project, _ string) error { return a.get(w, r, p.ID) })
	mine("PUT /files/{name...}", func(w http.ResponseWriter, r *http.Request, p Project, _ string) error { return a.put(w, r, p.ID) })
	mine("DELETE /files/{name...}", func(w http.ResponseWriter, r *http.Request, p Project, _ string) error {
		return a.write(w, p.ID, r.PathValue("name"), nil)
	})
	a.Handler = mux
	return a, nil
}

// Routes adds what a full key may ask about projects:
//
//	POST   /projects                  {"name","note","config","apps"}; ?via= who makes it.   -> Project
//	                                  makes the orchestrator agent from config with apps
//	                                  and the ones every orchestrator has, saves the project
//	                                  open, and posts the orchestrator its first message
//	GET    /projects                  -> [Project and "working": an agent of it is stepping]
//	GET    /projects/{id}/agents      -> [AgentInfo]: the orchestrator, then the agents under it
//	PUT    /projects/{id}/status      {"status","line"}; ?via= required. the orchestrator    -> Project
//	                                  reads a note of it at its next step, and is not woken
//	DELETE /projects/{id}             ?via= required. ?agents=1 deletes its agents too       -> 204
//	GET    /projects/{id}/files/      ?prefix= ?bodies=1 for those of the small files too    -> [File]
//	GET    /projects/{id}/files/{name} -> the file
//	PUT    /projects/{id}/files/{name} the file, whole                                       -> 204
//	DELETE /projects/{id}/files/{name}                                                       -> 204
//	GET    /models                    -> [Model]
func (a *App) Routes(mux *http.ServeMux) {
	handle := func(pattern string, fn func(w http.ResponseWriter, r *http.Request) error) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) { answer(w, fn(w, r)) })
	}
	// one is a route on a project there is.
	one := func(pattern string, fn func(w http.ResponseWriter, r *http.Request, p Project) error) {
		handle(pattern, func(w http.ResponseWriter, r *http.Request) error {
			a.mu.Lock()
			p, ok := a.projects[r.PathValue("id")]
			a.mu.Unlock()
			if !ok {
				return fmt.Errorf("%w: no project %q", fs.ErrNotExist, r.PathValue("id"))
			}
			return fn(w, r, p)
		})
	}
	handle("POST /projects", a.create)
	handle("GET /projects", a.list)
	mux.HandleFunc("GET /models", a.models)
	one("GET /projects/{id}/agents", func(w http.ResponseWriter, r *http.Request, p Project) error {
		return reply(w, a.agents(p))
	})
	one("PUT /projects/{id}/status", func(w http.ResponseWriter, r *http.Request, p Project) error {
		via := r.URL.Query().Get("via")
		if via == "" {
			return errors.New("?via= names who is acting")
		}
		return a.status(w, r, p.ID, "The user", via, "")
	})
	one("DELETE /projects/{id}", a.remove)
	one("GET /projects/{id}/files/{name...}", func(w http.ResponseWriter, r *http.Request, p Project) error { return a.get(w, r, p.ID) })
	one("PUT /projects/{id}/files/{name...}", func(w http.ResponseWriter, r *http.Request, p Project) error { return a.put(w, r, p.ID) })
	one("DELETE /projects/{id}/files/{name...}", func(w http.ResponseWriter, r *http.Request, p Project) error {
		return a.write(w, p.ID, r.PathValue("name"), nil)
	})
}

// answer is a route's error in the harness's words: 404 for what is not
// there, 413 for what does not fit, else the caller's to fix.
func answer(w http.ResponseWriter, err error) {
	var big *http.MaxBytesError
	switch {
	case err == nil:
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, harness.ErrNoAgent):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.As(err, &big), errors.Is(err, errFull):
		http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
	default:
		http.Error(w, err.Error(), http.StatusBadRequest)
	}
}

func reply(w http.ResponseWriter, v any) error {
	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(v)
}

// save writes the project's file whole, as a temporary file renamed over
// it, then makes memory agree. The caller holds mu.
func (a *App) save(p Project) error {
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	file := filepath.Join(a.dir, p.ID+".json")
	if err := os.WriteFile(file+".tmp", b, 0o644); err != nil {
		return err
	}
	if err := os.Rename(file+".tmp", file); err != nil {
		return err
	}
	a.projects[p.ID] = p
	return nil
}

// of is the project an agent is in: the one it orchestrates, or the one of
// the agent that made it, and so on up.
func (a *App) of(agent string) (Project, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for range depth {
		for _, p := range a.projects {
			if p.Orchestrator == agent {
				return p, true
			}
		}
		made, err := a.h.Agent(agent)
		maker, ok := strings.CutPrefix(made.Via, harness.AgentVia)
		if err != nil || !ok {
			break
		}
		agent = maker
	}
	return Project{}, false
}

// members is every agent by the project it is in, the orchestrator first.
func (a *App) members() map[string][]harness.Agent {
	all := a.h.AllAgents()
	slices.SortFunc(all, func(x, y harness.Agent) int { return x.Created.Compare(y.Created) })
	out := map[string][]harness.Agent{}
	for _, g := range all {
		if p, ok := a.of(g.ID); ok {
			out[p.ID] = append(out[p.ID], g)
		}
	}
	return out
}

func (a *App) agents(p Project) []harness.AgentInfo {
	out := []harness.AgentInfo{}
	for _, g := range a.members()[p.ID] {
		if i, err := a.h.AgentInfo(g.ID); err == nil {
			out = append(out, i)
		}
	}
	return out
}

func (a *App) list(w http.ResponseWriter, r *http.Request) error {
	type listed struct {
		Project
		Working bool `json:"working"`
	}
	members := a.members()
	a.mu.Lock()
	out := make([]listed, 0, len(a.projects))
	for _, p := range a.projects {
		out = append(out, listed{p, slices.ContainsFunc(members[p.ID], a.h.Working)})
	}
	a.mu.Unlock()
	slices.SortFunc(out, func(x, y listed) int { return y.Updated.Compare(x.Updated) })
	return reply(w, out)
}

func (a *App) create(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Name   string               `json:"name"`
		Note   string               `json:"note"`
		Config *harness.AgentConfig `json:"config"`
		Apps   []string             `json:"apps"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, harness.MaxPost)).Decode(&in); err != nil {
		return err
	}
	if strings.TrimSpace(in.Name) == "" || in.Config == nil {
		return errors.New(`the body is {"name","note","config","apps"}: a project has a name, and a config for its orchestrator`)
	}
	by := r.URL.Query().Get("via")
	var text bytes.Buffer
	if err := first.Execute(&text, map[string]any{"Name": in.Name, "Note": in.Note, "Models": a.connected(r.Context())}); err != nil {
		return err
	}
	g, err := a.h.CreateAgent(a.ctx, harness.AgentCreate{Name: in.Name, Config: in.Config, Apps: append(in.Apps, required...)}, by)
	if err != nil {
		return err
	}
	p := Project{ID: statefile.NewID(), Name: in.Name, Orchestrator: g.ID, Status: Open, Line: settingUp, Updated: time.Now().UTC().Truncate(time.Millisecond)}
	a.mu.Lock()
	err = os.MkdirAll(filepath.Join(a.dir, p.ID), 0o755)
	if err == nil {
		err = os.WriteFile(filepath.Join(a.dir, p.ID, index), dashboard, 0o644)
	}
	if err == nil {
		err = a.save(p)
	}
	a.mu.Unlock()
	if err == nil {
		_, _, err = a.h.PostAgent(a.ctx, g.ID, via, "", []harness.Part{{Type: "text/plain", Body: text.Bytes()}})
	}
	if err != nil {
		// The caller is told the create failed and may try again: nothing of
		// this try stays.
		if undo := errors.Join(a.drop(p.ID), a.h.DeleteAgent(a.ctx, g.ID, by), a.h.Delete(a.ctx, g.Sessions[0], by)); undo != nil {
			log.Printf("project: undoing the create of %s: %v", p.ID, undo)
		}
		return err
	}
	return reply(w, p)
}

// drop removes the project's store, then its file, then the project: one
// that could not all be removed is still there to remove again.
func (a *App) drop(id string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := os.RemoveAll(filepath.Join(a.dir, id)); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(a.dir, id+".json")); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	delete(a.projects, id)
	return nil
}

func (a *App) remove(w http.ResponseWriter, r *http.Request, p Project) error {
	by := r.URL.Query().Get("via")
	if by == "" {
		return errors.New("?via= names who is acting")
	}
	if r.URL.Query().Get("agents") == "1" {
		// The last made first, and the project last: when one fails the rest
		// are still found by who made them, and the delete can be asked again.
		// Then again, for the agents the orchestrator made meanwhile, until
		// there are none: a deleted agent makes no more.
		for members := a.members()[p.ID]; len(members) > 0; members = a.members()[p.ID] {
			for _, g := range slices.Backward(members) {
				if err := a.h.DeleteAgent(r.Context(), g.ID, by); err != nil && !errors.Is(err, harness.ErrNoAgent) {
					return err
				}
			}
		}
	}
	if err := a.drop(p.ID); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// status sets the project's status and line. When someone other than the
// orchestrator did, the orchestrator's live session gets a note of it, which
// does not wake it. who is the setter as the note names it, by its via,
// agent its id when it is an agent.
func (a *App) status(w http.ResponseWriter, r *http.Request, id, who, by, agent string) error {
	var in struct{ Status, Line string }
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, harness.MaxPost)).Decode(&in); err != nil {
		return err
	}
	if !slices.Contains([]string{Open, Waiting, Closed}, in.Status) {
		return fmt.Errorf("status %q: it is %s (the user should look), %s (on something outside) or %s (nothing needs the user)", in.Status, Open, Waiting, Closed)
	}
	if in.Line = strings.TrimSpace(in.Line); utf8.RuneCountInString(in.Line) > maxLine || strings.ContainsAny(in.Line, "\r\n") {
		return fmt.Errorf("line: one line of at most %d characters, as a sidebar shows it under the project's name", maxLine)
	}
	a.mu.Lock()
	p, ok := a.projects[id]
	var err error
	if !ok {
		err = fmt.Errorf("%w: no project %q", fs.ErrNotExist, id)
	} else {
		p.Status, p.Line, p.Updated = in.Status, in.Line, time.Now().UTC().Truncate(time.Millisecond)
		err = a.save(p)
	}
	a.mu.Unlock()
	if err != nil {
		return err
	}
	if agent != p.Orchestrator {
		if err := a.h.NoteAgent(p.Orchestrator, by, "project", fmt.Sprintf(changed, who, p.Name, p.Status, p.Line)); err != nil && !errors.Is(err, harness.ErrNoAgent) {
			return err
		}
	}
	return reply(w, p)
}

// get serves one file of the project's store, or with no name the list of
// the files whose names start with ?prefix=.
func (a *App) get(w http.ResponseWriter, r *http.Request, id string) error {
	name, root := r.PathValue("name"), filepath.Join(a.dir, id)
	if name == "" {
		files, err := list(root, r.URL.Query().Get("prefix"), r.URL.Query().Has("bodies"))
		if err != nil {
			return err
		}
		return reply(w, files)
	}
	if !fileName.MatchString(name) || !fs.ValidPath(name) {
		return fmt.Errorf("%w: no file %q", fs.ErrNotExist, name)
	}
	f, err := os.Open(filepath.Join(root, filepath.FromSlash(name)))
	if err != nil {
		return fmt.Errorf("%w: no file %q", fs.ErrNotExist, name)
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		return fmt.Errorf("%w: no file %q", fs.ErrNotExist, name)
	}
	w.Header().Set("Content-Security-Policy", Sandbox)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, no-cache") // asked again each time, and answered 304 when it is the same
	http.ServeContent(w, r, name, st.ModTime(), f)
	return nil
}

// list is the files under root whose names start with prefix, by name, and
// with bodies what is in the small ones.
func list(root, prefix string, bodies bool) ([]File, error) {
	out := []File{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		if name := filepath.ToSlash(rel); strings.HasPrefix(name, prefix) && fileName.MatchString(name) { // a write under way has a name no file has
			i, err := d.Info()
			if err != nil {
				return err
			}
			f := File{Name: name, Size: i.Size(), Modified: i.ModTime().UTC()}
			if bodies && f.Size <= inline {
				if f.Body, err = os.ReadFile(p); err != nil {
					return err
				}
			}
			out = append(out, f)
		}
		return nil
	})
	return out, err
}

func (a *App) put(w http.ResponseWriter, r *http.Request, id string) error {
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxFile))
	if err != nil {
		return fmt.Errorf("a file of the store is at most %d MiB: %w", maxFile>>20, err)
	}
	return a.write(w, id, r.PathValue("name"), b)
}

// errFull is a write the project's store has no room for.
var errFull = fmt.Errorf("the project's store holds at most %d MiB: delete files, or keep large ones on a worker and link to them", maxStore>>20)

// write puts body in the store under name, whole or not at all, or with no
// body removes the file. One write at a time, so the store's size is checked
// against what is there.
func (a *App) write(w http.ResponseWriter, id, name string, body []byte) error {
	if !fileName.MatchString(name) || !fs.ValidPath(name) {
		return fmt.Errorf("file name %q: path segments of letters, digits, dot, dash and underscore, such as cards/0001.json", name)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	p, ok := a.projects[id]
	if !ok {
		return fmt.Errorf("%w: no project %q", fs.ErrNotExist, id)
	}
	root := filepath.Join(a.dir, id)
	file := filepath.Join(root, filepath.FromSlash(name))
	if body == nil {
		if st, err := os.Stat(file); err != nil || st.IsDir() {
			return fmt.Errorf("%w: no file %q", fs.ErrNotExist, name)
		}
	} else {
		files, err := list(root, "", false)
		if err != nil {
			return err
		}
		size := int64(len(body))
		for _, f := range files {
			if f.Name != name {
				size += f.Size
			}
		}
		if size > maxStore {
			return errFull
		}
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(file+"~", body, 0o644); err != nil {
			return err
		}
	}
	// The count goes up before the file changes: a write that then fails
	// costs a reader one list, and no change is ever left uncounted.
	p.Counter++
	if err := a.save(p); err != nil {
		_ = os.Remove(file + "~") // there is none when the write is a delete
		return err
	}
	if body == nil {
		if err := os.Remove(file); err != nil {
			return err
		}
	} else if err := os.Rename(file+"~", file); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// presets is the models an agent of this harness can run on: the catalog's
// presets whose provider the harness serves, in the catalog's order, then
// what the server of each provider the catalog does not have lists itself,
// such as a local one. The catalog is fetched at most once in catalogAge;
// when a fetch fails the one before it is used. The servers are asked at
// once, and one that does not answer in modelsWait adds nothing. The answer
// is used for modelsAge.
func (a *App) presets(ctx context.Context) ([]Model, error) {
	a.cmu.Lock()
	defer a.cmu.Unlock()
	if a.listed != nil && time.Since(a.listedAt) < modelsAge {
		return a.listed, nil
	}
	if time.Since(a.catAt) > catalogAge {
		c, err := a.Catalog(ctx)
		if err != nil && a.catAt.IsZero() {
			return nil, fmt.Errorf("catalog: %w", err)
		}
		if err == nil {
			a.cat, a.catAt = c, time.Now()
		}
	}
	served, err := a.h.ProviderInfos()
	if err != nil {
		return nil, err
	}
	out := []Model{}
	for _, id := range a.cat.Groups[catalog.SelfHosted] {
		if p := a.cat.Presets[id]; slices.ContainsFunc(served, func(s harness.ProviderInfo) bool { return s.Name == p.Provider }) {
			out = append(out, Model{id, p.Name, a.cat.Providers[p.Provider].Name, statefile.Conf{Model: p.Provider + "/" + p.Model, Tools: p.Tools, ContextLimit: p.ContextLimit, Input: p.Input, Params: p.Params, Preset: id}, p.Settings})
		}
	}
	ctx, cancel := context.WithTimeout(ctx, modelsWait)
	defer cancel()
	lists := make([][]string, len(served))
	var wg sync.WaitGroup
	for i, s := range served {
		if _, known := a.cat.Providers[s.Name]; known || s.Shape != "openai-chat" {
			continue
		}
		wg.Go(func() {
			ids, err := a.h.Models(ctx, s.Name)
			if err != nil {
				log.Printf("project: the models of %s: %v", s.Name, err)
			}
			lists[i] = ids
		})
	}
	wg.Wait()
	for i, s := range served {
		for _, id := range lists[i] {
			model := s.Name + "/" + id
			out = append(out, Model{model, id, "Custom", statefile.Conf{Model: model, Tools: "default", ContextLimit: cmp.Or(s.ContextLimit, catalog.CustomLimit), Input: []string{}}, nil})
		}
	}
	a.listed, a.listedAt = out, time.Now()
	return out, nil
}

func (a *App) models(w http.ResponseWriter, r *http.Request) {
	ms, err := a.presets(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	_ = reply(w, ms)
}

// connected is the models as the first message lists them.
func (a *App) connected(ctx context.Context) string {
	ms, err := a.presets(ctx)
	if err != nil || len(ms) == 0 {
		return "unknown; ask the user"
	}
	ids := make([]string, len(ms))
	for i, m := range ms {
		ids[i] = m.ID
	}
	return strings.Join(ids, ", ")
}
