package connector

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/mainplane-ai/mainplane/pkg/api"
	"github.com/mainplane-ai/mainplane/pkg/apps"
	"github.com/mainplane-ai/mainplane/pkg/statefile"
)

// Session is the client a worker's environment gives: the harness at
// MAINPLANE_URL with the session's key, which reaches the skills and apps of
// the agent whose live session it is. False when the environment has none.
func Session() (*Client, bool) {
	c := &Client{Mesh: os.Getenv("MAINPLANE_URL"), Key: os.Getenv("MAINPLANE_KEY")}
	return c, c.Mesh != "" && c.Key != ""
}

// Agents lists agents, newest updated first; via and status, when given,
// keep the agents made by that caller and in that status.
func (c *Client) Agents(ctx context.Context, via, status string) ([]api.AgentInfo, error) {
	var infos []api.AgentInfo
	err := c.Call(ctx, "GET", api.AgentsPath+"?via="+url.QueryEscape(via)+"&status="+url.QueryEscape(status), "", nil, &infos)
	return infos, err
}

func (c *Client) Agent(ctx context.Context, id string) (api.AgentInfo, error) {
	var i api.AgentInfo
	err := c.Call(ctx, "GET", api.Path(api.AgentPath, id), "", nil, &i)
	return i, err
}

// CreateAgent makes an agent and returns its id and its first session.
func (c *Client) CreateAgent(ctx context.Context, cr api.AgentCreate, via string) (id, session string, err error) {
	b, err := json.Marshal(cr)
	if err != nil {
		return "", "", err
	}
	var out struct{ ID, Session string }
	err = c.Call(ctx, "POST", api.AgentsPath+"?via="+url.QueryEscape(via), "application/json", bytes.NewReader(b), &out)
	return out.ID, out.Session, err
}

// PostAgent posts a turn to the agent, as Post does to a session, and
// returns the session it landed on and the last record's n.
func (c *Client) PostAgent(ctx context.Context, id, via, text string, files []string) (string, int, error) {
	var out struct {
		Session string
		N       int
	}
	err := c.post(ctx, api.Path(api.AgentRecordsPath, id)+"?via="+url.QueryEscape(via), text, files, &out)
	return out.Session, out.N, err
}

func (c *Client) StopAgent(ctx context.Context, id, via string) error {
	return c.Call(ctx, "POST", api.Path(api.AgentStopPath, id)+"?via="+url.QueryEscape(via), "", nil, nil)
}

// RetryAgent steps the agent's live session from its tip, and returns the
// status the harness answers.
func (c *Client) RetryAgent(ctx context.Context, id string) (statefile.Status, error) {
	var out struct{ Status statefile.Status }
	err := c.Call(ctx, "POST", api.Path(api.AgentRetryPath, id), "", nil, &out)
	return out.Status, err
}

// DeleteAgent removes the agent. Its sessions stay.
func (c *Client) DeleteAgent(ctx context.Context, id, via string) error {
	return c.Call(ctx, "DELETE", api.Path(api.AgentPath, id)+"?via="+url.QueryEscape(via), "", nil, nil)
}

// SetAgentName names the agent; an empty name removes it.
func (c *Client) SetAgentName(ctx context.Context, id, via, name string) error {
	return c.Call(ctx, "PUT", api.Path(api.AgentNamePath, id)+"?via="+url.QueryEscape(via), "text/plain; charset=utf-8", strings.NewReader(name), nil)
}

// SetAgentConfig is the config of the agent's next session. No session is
// made until a post finds the live one cannot go on.
func (c *Client) SetAgentConfig(ctx context.Context, id, via string, conf api.AgentConfig) error {
	return c.put(ctx, api.Path(api.AgentConfigPath, id)+"?via="+url.QueryEscape(via), conf)
}

// SetAgentInstructions is the text every session of the agent starts with;
// empty removes it.
func (c *Client) SetAgentInstructions(ctx context.Context, id, via, text string) error {
	return c.Call(ctx, "PUT", api.Path(api.AgentInstructionsPath, id)+"?via="+url.QueryEscape(via), "text/plain; charset=utf-8", strings.NewReader(text), nil)
}

// SetAgentApps makes names the apps the agent may call.
func (c *Client) SetAgentApps(ctx context.Context, id, via string, names []string) error {
	return c.put(ctx, api.Path(api.AgentAppsPath, id)+"?via="+url.QueryEscape(via), names)
}

func (c *Client) put(ctx context.Context, path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.Call(ctx, "PUT", path, "application/json", bytes.NewReader(b), nil)
}

// Skill is an app's name and line 1 of its skill.
type Skill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Skills lists the apps the key reaches: every app for a full key, the
// agent's own for a session's key.
func (c *Client) Skills(ctx context.Context) ([]Skill, error) {
	var out []Skill
	err := c.Call(ctx, "GET", api.SkillsPath, "", nil, &out)
	return out, err
}

// Skill is the app's skill.md: how an agent uses the app.
func (c *Client) Skill(ctx context.Context, app string) (string, error) {
	resp, err := c.Do(ctx, "GET", api.Path(api.SkillPath, app), "", nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	return string(b), err
}

// App calls an app with a session's key: method and path are the app's own,
// and so is the reply, which the caller closes.
func (c *Client) App(ctx context.Context, app, method, path, ctype string, body io.Reader) (*http.Response, error) {
	return c.Do(ctx, method, api.Path(api.AppPath, app)+"/"+strings.TrimPrefix(path, "/"), ctype, body)
}

// Apps lists every app the harness has, with its kind and status.
func (c *Client) Apps(ctx context.Context) ([]apps.Info, error) {
	var out []apps.Info
	err := c.Call(ctx, "GET", api.AppsPath, "", nil, &out)
	return out, err
}

// RunApp starts a daemon app, or with run false stops it until a start.
func (c *Client) RunApp(ctx context.Context, app string, run bool) error {
	p := api.AppStopPath
	if run {
		p = api.AppStartPath
	}
	return c.Call(ctx, "POST", api.Path(p, app), "", nil, nil)
}

// Secrets is the names of the secrets apps may be given. No call reads a
// value.
func (c *Client) Secrets(ctx context.Context) ([]string, error) {
	var out []string
	err := c.Call(ctx, "GET", api.SecretsPath, "", nil, &out)
	return out, err
}

// SetSecret saves the secret and starts the apps that waited for it.
func (c *Client) SetSecret(ctx context.Context, name, value string) error {
	return c.Call(ctx, "PUT", api.Path(api.SecretPath, name), "text/plain", strings.NewReader(value), nil)
}

func (c *Client) DeleteSecret(ctx context.Context, name string) error {
	return c.Call(ctx, "DELETE", api.Path(api.SecretPath, name), "", nil, nil)
}
