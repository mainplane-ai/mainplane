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
	"net/http"
	"time"
)

// URL is the published catalog. v1 changes only with a change that breaks
// readers.
const URL = "https://dl.mainplane.ai/catalog/v1.json"

const (
	fetchTime = 30 * time.Second // the whole fetch; the file is tens of KiB
	maxSize   = 16 << 20         // a catalog of every model there is stays far below this
)

type Catalog struct {
	Version   string              `json:"version"`
	Providers map[string]Provider `json:"providers"` // by id: the name a harness config gives it
	Presets   map[string]Preset   `json:"presets"`   // by id: provider/model
	Groups    map[string][]string `json:"groups"`    // preset ids, in the order to show
}

type Provider struct {
	Name  string `json:"name"`
	Shape string `json:"shape"`
	URL   string `json:"url,omitempty"` // what the config's url is, for an openai-chat provider
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
