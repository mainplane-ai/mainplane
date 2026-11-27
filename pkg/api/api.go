// Package api is the HTTP API's contract: the JSON shapes the harness and
// its clients send each other and the paths they meet at. Servers register
// these paths and clients fill them in, so a route has one spelling.
package api

import (
	"encoding/json"
	"time"

	"github.com/mainplane-ai/mainplane/pkg/statefile"
)

// Create is the body of POST /sessions. From set is a copy of records 1
// through N-1 of that session; otherwise a fresh session from Model,
// ContextLimit, and Workers. Workers are not checked: the config is the
// whitelist, and a worker named before it dials in is a worker the session
// waits for.
type Create struct {
	From         string                     `json:"from,omitempty"`
	N            int                        `json:"n,omitempty"`
	Model        string                     `json:"model,omitempty"`
	ContextLimit int                        `json:"context_limit,omitempty"`
	Workers      []statefile.Worker         `json:"workers,omitempty"`
	Tools        string                     `json:"tools,omitempty"`
	Input        []string                   `json:"input,omitempty"`
	Params       map[string]json.RawMessage `json:"params,omitempty"`
	Preset       string                     `json:"preset,omitempty"`
	Name         string                     `json:"name,omitempty"`
}

// Info is what GET /sessions/{id} and each entry of GET /sessions say about a
// session: everything the tip states and its name, no cost. ContextUsed is
// the last step's prompt in tokens, against Config.ContextLimit. Updated is
// the newest record that is not a name, so a rename does not reorder a list.
// Cache is the prompt cache of the newest step that kept one.
type Info struct {
	ID          string           `json:"id"`
	Name        string           `json:"name,omitempty"`
	Via         string           `json:"via,omitempty"` // who made it: a connector, or agent:<id> for an agent's session
	Status      statefile.Status `json:"status"`
	Config      statefile.Conf   `json:"config"`
	Created     time.Time        `json:"created"`
	Updated     time.Time        `json:"updated"`
	N           int              `json:"n"`
	ContextUsed int              `json:"context_used"`
	Usage       statefile.Usage  `json:"usage"`
	Cache       Cache            `json:"cache,omitzero"`
}

// Cache is how long the vendor keeps a session's prompt: warm until Until,
// or with Likely, probably warm until then, on a vendor that evicts by load.
// Zero when no step kept one or the vendor states no window.
type Cache struct {
	Until  time.Time `json:"until"`
	Likely bool      `json:"likely,omitempty"`
}

// Agent is agents/<id>.json: an identity that outlives a session. It holds
// the config of its next session, the apps it may use, the instructions
// every session of it starts with, and every session it has had, oldest
// first. The last one is live: the one a post lands on, and the only one
// whose key reaches the agent's apps. There is always one.
type Agent struct {
	ID           string      `json:"id"`
	Name         string      `json:"name,omitempty"`
	Via          string      `json:"via,omitempty"` // who made it: a connector, or agent:<id> for one an agent created
	Created      time.Time   `json:"created"`
	Config       AgentConfig `json:"config"`
	Apps         []string    `json:"apps"`
	Instructions string      `json:"instructions,omitempty"`
	Sessions     []string    `json:"sessions"`
}

// AgentConfig is a session's config and the limit that is the agent's alone.
// Past ColdLimit a post that finds the prompt cache expired goes to a new
// session: the old one would be paid for again in full. Zero is no such limit.
type AgentConfig struct {
	statefile.Conf
	ColdLimit int `json:"cold_limit,omitempty"`
}

// AgentInfo is an agent as GET /agents shows it. Status, ContextUsed and
// Updated are its live session's. Usage is the agent's: the sum over every
// session it has had whose file is still there. The live session's own is
// in GET /sessions/{current_session}.
type AgentInfo struct {
	Agent
	Updated        time.Time        `json:"updated"`
	Status         statefile.Status `json:"status"`
	CurrentSession string           `json:"current_session"`
	ContextUsed    int              `json:"context_used"`
	Usage          statefile.Usage  `json:"usage"`
}

// AgentCreate is the body of POST /agents. With Session the agent adopts that
// session, and takes its config when it names none; without, Config is
// required and the first session is made from it.
type AgentCreate struct {
	Name         string       `json:"name,omitempty"`
	Config       *AgentConfig `json:"config,omitempty"`
	Apps         []string     `json:"apps,omitempty"`
	Instructions string       `json:"instructions,omitempty"`
	Session      string       `json:"session,omitempty"`
}

// ProviderInfo is a provider as GET /providers shows it: no key.
type ProviderInfo struct {
	Name         string `json:"name"`
	Shape        string `json:"shape"`
	URL          string `json:"url"`
	ContextLimit int    `json:"context_limit,omitempty"`
}

// Live is the agent's live session.
func (a Agent) Live() string { return a.Sessions[len(a.Sessions)-1] }
