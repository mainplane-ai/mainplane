package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/mainplane-ai/mainplane/pkg/apps"
	"github.com/mainplane-ai/mainplane/pkg/auth"
	"github.com/mainplane-ai/mainplane/pkg/statefile"
)

// agentRoutes serve the agent verbs and appRoutes the app, skill and secret
// admin routes, all for an api key. Gate decides what a signed token reaches.
//
//	GET    /agents                ?via= ?status= ?after=<id> ?limit=      -> [AgentInfo], newest updated first
//	POST   /agents                NewAgent; ?via= required                -> {"id","session"}
//	GET    /agents/{id}           -> AgentInfo
//	DELETE /agents/{id}           ?via= required. stops the live session  -> 204; the sessions stay
//	POST   /agents/{id}/records   as POST /sessions/{id}/records, onto    -> {"session","n"}
//	                              the live session or the next (Rule 2)
//	POST   /agents/{id}/stop      ?via= required                          -> 204
//	POST   /agents/{id}/retry     -> {"status"}
//	PUT    /agents/{id}/name      the name as text; ?via= required        -> 204
//	PUT    /agents/{id}/config    AgentConfig; ?via= required (Rule 1)    -> 204
//	PUT    /agents/{id}/apps      ["name"]; ?via= required                -> 204
//	GET    /skills                -> [{"name","description"}]
//	GET    /skills/{app}          -> the app's skill.md
//	GET    /apps                  -> [apps.Info]
//	POST   /apps/{name}/start     enable a run app                        -> 204
//	POST   /apps/{name}/stop      disable a run app                       -> 204
//	GET    /secrets               -> ["NAME"]: no route reads a value
//	PUT    /secrets/{name}        the value as the body                   -> 204
//	DELETE /secrets/{name}        -> 204
func (h *Harness) agentRoutes(ctx context.Context, mux *http.ServeMux) {
	handle := func(pattern string, fn func(w http.ResponseWriter, r *http.Request, via string) error) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			if id := r.PathValue("id"); id != "" && !statefile.ValidID(id) {
				fail(w, fmt.Errorf("bad agent id %q", id))
				return
			}
			v := r.URL.Query().Get("via")
			if v == "" && r.Method != http.MethodGet && !strings.HasSuffix(pattern, "/retry") {
				fail(w, fmt.Errorf("?via= names who is acting"))
				return
			}
			if err := fn(w, r, v); err != nil {
				fail(w, err)
			}
		})
	}
	handle("GET /agents", func(w http.ResponseWriter, r *http.Request, via string) error {
		infos, q := h.AgentInfos(), r.URL.Query()
		if via != "" {
			infos = slices.DeleteFunc(infos, func(i AgentInfo) bool { return i.Via != via })
		}
		reply(w, page(infos, q.Get("status"), q.Get("after"), q.Get("limit"), func(i AgentInfo) (string, statefile.Status) { return i.ID, i.Status }))
		return nil
	})
	handle("POST /agents", func(w http.ResponseWriter, r *http.Request, via string) error {
		var n NewAgent
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, MaxPost)).Decode(&n); err != nil {
			return err
		}
		a, err := h.CreateAgent(ctx, n, via)
		if err != nil {
			return err
		}
		reply(w, map[string]string{"id": a.ID, "session": a.Live()})
		return nil
	})
	handle("GET /agents/{id}", func(w http.ResponseWriter, r *http.Request, _ string) error {
		info, err := h.AgentInfo(r.PathValue("id"))
		if err == nil {
			reply(w, info)
		}
		return err
	})
	handle("DELETE /agents/{id}", func(w http.ResponseWriter, r *http.Request, via string) error {
		return done(w, h.DeleteAgent(ctx, r.PathValue("id"), via))
	})
	handle("POST /agents/{id}/records", func(w http.ResponseWriter, r *http.Request, via string) error {
		return h.serveAgentPost(ctx, w, r, via)
	})
	handle("POST /agents/{id}/stop", func(w http.ResponseWriter, r *http.Request, via string) error {
		return done(w, h.StopAgent(ctx, r.PathValue("id"), via))
	})
	handle("POST /agents/{id}/retry", func(w http.ResponseWriter, r *http.Request, _ string) error {
		if err := h.RetryAgent(ctx, r.PathValue("id")); err != nil {
			return err
		}
		reply(w, map[string]statefile.Status{"status": statefile.StatusStepping})
		return nil
	})
	handle("PUT /agents/{id}/name", func(w http.ResponseWriter, r *http.Request, _ string) error {
		b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxName+1))
		if err != nil {
			return err
		}
		return done(w, h.SetAgentName(r.PathValue("id"), string(b)))
	})
	handle("PUT /agents/{id}/config", func(w http.ResponseWriter, r *http.Request, via string) error {
		var c AgentConfig
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, MaxPost)).Decode(&c); err != nil {
			return err
		}
		return done(w, h.SetAgentConfig(r.PathValue("id"), via, c))
	})
	handle("PUT /agents/{id}/apps", func(w http.ResponseWriter, r *http.Request, _ string) error {
		var names []string
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, MaxPost)).Decode(&names); err != nil {
			return err
		}
		return done(w, h.SetAgentApps(r.PathValue("id"), names))
	})
}

// appRoutes serve skills, apps and secrets to an api key; Gate keeps a
// session key to the skills and apps of its agent.
func (h *Harness) appRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /skills", func(w http.ResponseWriter, r *http.Request) { reply(w, h.skills(h.Apps.Names())) })
	mux.HandleFunc("GET /skills/{app}", h.serveSkill)
	mux.HandleFunc("GET /apps", func(w http.ResponseWriter, r *http.Request) { reply(w, h.Apps.List()) })
	mux.HandleFunc("POST /apps/{app}/start", func(w http.ResponseWriter, r *http.Request) { _ = done(w, report(w, h.Apps.Start(r.PathValue("app")))) })
	mux.HandleFunc("POST /apps/{app}/stop", func(w http.ResponseWriter, r *http.Request) { _ = done(w, report(w, h.Apps.Stop(r.PathValue("app")))) })
	unkeyed := func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "an app call needs a session key: call it from a worker of the agent's live session, with $MAINPLANE_KEY", http.StatusForbidden)
	}
	mux.HandleFunc("/apps/{app}", unkeyed)
	mux.HandleFunc("/apps/{app}/{path...}", unkeyed)
	mux.HandleFunc("GET /secrets", func(w http.ResponseWriter, r *http.Request) { reply(w, h.Apps.SecretNames()) })
	mux.HandleFunc("PUT /secrets/{name}", func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxPost))
		if err == nil {
			err = h.Apps.SetSecret(r.PathValue("name"), string(b))
		}
		_ = done(w, report(w, err))
	})
	mux.HandleFunc("DELETE /secrets/{name}", func(w http.ResponseWriter, r *http.Request) {
		_ = done(w, report(w, h.Apps.DeleteSecret(r.PathValue("name"))))
	})
}

// done is 204 unless err, which it hands on.
func done(w http.ResponseWriter, err error) error {
	if err == nil {
		w.WriteHeader(http.StatusNoContent)
	}
	return err
}

// report fails the request on err and hands err on, so done writes nothing.
func report(w http.ResponseWriter, err error) error {
	if err != nil {
		fail(w, err)
	}
	return err
}

// serveAgentPost posts to an agent as via.
func (h *Harness) serveAgentPost(ctx context.Context, w http.ResponseWriter, r *http.Request, via string) error {
	parts, err := parts(r.Header.Get("Content-Type"), http.MaxBytesReader(w, r.Body, MaxPost))
	if err != nil {
		return err
	}
	sid, n, err := h.PostAgent(ctx, r.PathValue("id"), via, r.Header.Get("Idempotency-Key"), parts)
	if err == nil {
		reply(w, map[string]any{"session": sid, "n": n})
	}
	return err
}

// AgentInfos is every agent, newest updated first.
func (h *Harness) AgentInfos() []AgentInfo {
	out := []AgentInfo{}
	for _, a := range h.AllAgents() {
		info, err := h.AgentInfo(a.ID)
		if err != nil {
			continue // deleted since
		}
		out = append(out, info)
	}
	slices.SortFunc(out, func(a, b AgentInfo) int { return b.Updated.Compare(a.Updated) })
	return out
}

// SkillInfo is an app as GET /skills lists it.
type SkillInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

func (h *Harness) skills(names []string) []SkillInfo {
	out := []SkillInfo{}
	for _, n := range names {
		if a, ok := h.Apps.Get(n); ok {
			out = append(out, SkillInfo{n, a.Summary()})
		}
	}
	return out
}

func (h *Harness) serveSkill(w http.ResponseWriter, r *http.Request) {
	a, ok := h.Apps.Get(r.PathValue("app"))
	if !ok {
		http.Error(w, "no app "+r.PathValue("app"), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	_, _ = w.Write(a.Skill)
}

// Gate gives a signed token, under ctx as Handler, its few routes and an api key full. A session
// key reaches the skills and apps of its agent while it is the agent's live
// session, so a rollover or an app taken away revokes it. An app key makes
// the calls its app's entitlements name, as that app.
func (h *Harness) Gate(ctx context.Context, full http.Handler) http.Handler {
	skey := http.NewServeMux()
	skey.HandleFunc("GET /skills", func(w http.ResponseWriter, r *http.Request) {
		if a, ok := h.liveCaller(w, r, ""); ok {
			reply(w, h.skills(a.Apps))
		}
	})
	skey.HandleFunc("GET /skills/{app}", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := h.liveCaller(w, r, r.PathValue("app")); ok {
			h.serveSkill(w, r)
		}
	})
	app := func(w http.ResponseWriter, r *http.Request) {
		if a, ok := h.liveCaller(w, r, r.PathValue("app")); ok {
			h.Apps.Serve(w, r, r.PathValue("app"), r.PathValue("path"), a.ID, a.Live())
		}
	}
	skey.HandleFunc("/apps/{app}", app)
	skey.HandleFunc("/apps/{app}/{path...}", app)
	akey := http.NewServeMux()
	akey.HandleFunc("POST /agents/{id}/records", func(w http.ResponseWriter, r *http.Request) {
		name, ok := h.entitled(w, r, apps.Post)
		if !ok {
			return
		}
		if a, err := h.GetAgent(r.PathValue("id")); err != nil || !slices.Contains(a.Apps, name) {
			http.Error(w, fmt.Sprintf("agent %s does not have app %s", r.PathValue("id"), name), http.StatusForbidden)
			return
		}
		if err := h.serveAgentPost(ctx, w, r, "app:"+name); err != nil {
			fail(w, err)
		}
	})
	akey.HandleFunc("GET /agents", func(w http.ResponseWriter, r *http.Request) {
		name, ok := h.entitled(w, r, apps.List)
		if !ok {
			return
		}
		type holder struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		out := []holder{}
		for _, a := range h.AllAgents() {
			if slices.Contains(a.Apps, name) {
				out = append(out, holder{a.ID, a.Name})
			}
		}
		slices.SortFunc(out, func(a, b holder) int { return strings.Compare(a.ID, b.ID) })
		reply(w, out)
	})
	for _, m := range []*http.ServeMux{skey, akey} {
		m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "this key does not reach "+r.URL.Path, http.StatusForbidden)
		})
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch auth.CallerOf(r.Context()).Kind {
		case auth.SessionKey:
			skey.ServeHTTP(w, r)
		case auth.AppKey:
			akey.ServeHTTP(w, r)
		default:
			full.ServeHTTP(w, r)
		}
	})
}

// liveCaller is the agent whose live session holds the request's session
// key, and that has app unless app is empty; else the request is refused.
func (h *Harness) liveCaller(w http.ResponseWriter, r *http.Request, app string) (Agent, bool) {
	sid := auth.CallerOf(r.Context()).Subject
	a, ok := h.LiveAgent(sid)
	if !ok {
		http.Error(w, fmt.Sprintf("session %s is not the live session of an agent: its key is revoked", sid), http.StatusForbidden)
		return Agent{}, false
	}
	if app != "" && !slices.Contains(a.Apps, app) {
		http.Error(w, fmt.Sprintf("agent %s does not have app %s", a.ID, app), http.StatusForbidden)
		return Agent{}, false
	}
	return a, true
}

// entitled is the app of the request's app key, when that app declares e.
func (h *Harness) entitled(w http.ResponseWriter, r *http.Request, e string) (string, bool) {
	name := auth.CallerOf(r.Context()).Subject
	if a, ok := h.Apps.Get(name); !ok || !slices.Contains(a.Entitlements, e) {
		http.Error(w, fmt.Sprintf("app %s has no %q entitlement", name, e), http.StatusForbidden)
		return "", false
	}
	return name, true
}
