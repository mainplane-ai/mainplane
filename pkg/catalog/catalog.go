// Package catalog is what connectors offer people to choose: the providers a
// harness may have and the presets, a full session config each, they show as
// models. The harness never reads it; it takes whatever full config a
// connector sends. build/ makes it from source.json and models.dev, and a
// workflow publishes it at URL.
package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"
)

// URL is the published catalog. v1 changes only with a change that breaks
// readers.
const URL = "https://dl.mainplane.ai/catalog/v1.json"

const (
	fetchTime = 30 * time.Second // the whole fetch; the file is tens of KiB
	maxSize   = 16 << 20         // a catalog of every model there is stays far below this
)

const (
	// SelfHosted is the group of the presets a harness's own user picks from.
	SelfHosted = "self-hosted-all"
	// CustomLimit is the context limit of a session on a model no preset
	// has, when its provider's config gives none: under the window a local
	// server loads a model with by default.
	CustomLimit = 30_000
)

type Catalog struct {
	Version   string              `json:"version"`
	Providers map[string]Provider `json:"providers"` // by id: the name a harness config gives it
	Presets   map[string]Preset   `json:"presets"`   // by id: provider/model
	Groups    map[string][]string `json:"groups"`    // preset ids, in the order to show
}

type Provider struct {
	Name    string `json:"name"`
	Shape   string `json:"shape"`
	URL     string `json:"url,omitempty"`     // the config's url, where it is not the shape's default
	Session string `json:"session,omitempty"` // the config's session header
	Group   string `json:"group,omitempty"`   // providers that take one key, one per shape a vendor's models span: a connector asks once and sets it on each
}

// Keyed is the ids of the providers that take provider id's key: its group,
// or id alone.
func (c Catalog) Keyed(id string) []string {
	g := c.Providers[id].Group
	if g == "" {
		return []string{id}
	}
	var ids []string
	for _, k := range slices.Sorted(maps.Keys(c.Providers)) {
		if c.Providers[k].Group == g {
			ids = append(ids, k)
		}
	}
	return ids
}

// Sections order the providers a connector offers: subscriptions first, then
// each kind of provider, the ones people use most first. A provider the
// catalog adds later is under More, by name, until it is placed here.
var Sections = []struct {
	Name string
	IDs  []string
}{
	{"Subscriptions", []string{"claude-code", "chatgpt", "opencode-go"}},
	{"Frontier labs", []string{"anthropic", "openai", "google", "meta", "xai", "deepseek", "moonshot", "zai", "minimax", "xiaomi", "mistral"}},
	{"Clouds", []string{"bedrock"}},
	{"Gateways", []string{"openrouter"}},
}

// Offer is a provider a connector offers, under its section.
type Offer struct{ Section, ID string }

// Offered is the providers to offer, a group once: those of Sections in
// order, then the rest under More, by name.
func (c Catalog) Offered() []Offer {
	var out []Offer
	placed := map[string]bool{}
	for _, sec := range Sections {
		for _, id := range sec.IDs {
			if _, ok := c.Providers[id]; ok {
				out = append(out, Offer{sec.Name, id})
				for _, k := range c.Keyed(id) {
					placed[k] = true
				}
			}
		}
	}
	byName := func(a, b string) int {
		return strings.Compare(strings.ToLower(c.Providers[a].Name), strings.ToLower(c.Providers[b].Name))
	}
	for _, id := range slices.SortedFunc(maps.Keys(c.Providers), byName) {
		if !placed[id] && c.Keyed(id)[0] == id {
			out = append(out, Offer{"More", id})
		}
	}
	return out
}

// Preset is a full session config but its workers.
type Preset struct {
	Name         string                     `json:"name"`
	Provider     string                     `json:"provider"`
	Model        string                     `json:"model"`
	Tools        string                     `json:"tools"`
	ContextLimit int                        `json:"context_limit"`
	Input        []string                   `json:"input"`
	Params       map[string]json.RawMessage `json:"params"`
	Cost         Cost                       `json:"cost"`
	Settings     []Setting                  `json:"settings,omitempty"`
}

// Cost is USD per million tokens.
type Cost struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cache_read"`
	CacheWrite float64 `json:"cache_write"`
}

// Setting is one choice a connector may offer, such as effort. Each value is
// params merged over the preset's; the value whose params the preset's hold is
// the current one.
type Setting struct {
	Name   string  `json:"name"`
	Values []Value `json:"values"`
}

type Value struct {
	Name   string                     `json:"name"`
	Params map[string]json.RawMessage `json:"params"`
}

// Fetch reads the published catalog.
func Fetch(ctx context.Context) (Catalog, error) {
	var c Catalog
	req, err := http.NewRequestWithContext(ctx, "GET", URL, nil)
	if err != nil {
		return c, err
	}
	resp, err := (&http.Client{Timeout: fetchTime}).Do(req)
	if err != nil {
		return c, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return c, fmt.Errorf("catalog: %s", resp.Status)
	}
	err = json.NewDecoder(io.LimitReader(resp.Body, maxSize)).Decode(&c)
	return c, err
}
