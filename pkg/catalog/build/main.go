// Build prints the catalog: source.json, our choices, filled in with each
// model's name, limits, input and prices from models.dev. It fails on a
// preset models.dev does not have, or an effort the model does not take. A
// variant, such as a fast mode, is the model's preset again with params of its
// own and a price that is the model's models.dev price times the multiplier
// source.json gives with its source, since models.dev lists none.
//
//	go run ./pkg/catalog/build > catalog.json
package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/mainplane-ai/mainplane/pkg/catalog"
	"github.com/mainplane-ai/mainplane/pkg/provider"
)

const modelsDev = "https://models.dev/api.json"

// contextLimit caps every preset's context: past it a session costs more per
// step than it gains, and /continue starts over with a summary.
const contextLimit = 250_000

// media are the types each models.dev input modality may mean. A preset
// takes the ones its shape's code sends as media.
var media = map[string][]string{
	"image": {"image/jpeg", "image/png", "image/gif", "image/webp"},
	"pdf":   {"application/pdf"},
	"audio": {"audio/mpeg", "audio/aiff"},
	"video": {"video/mp4", "video/webm"},
}

//go:embed source.json
var sourceJSON []byte

type source struct {
	Providers map[string]struct {
		catalog.Provider
		ModelsDev string `json:"models_dev"`
		Plan      bool   `json:"plan"` // its use counts against the user's plan, not per token: its presets cost nothing
	} `json:"providers"`
	Params  map[string]map[string]any `json:"params"` // by provider: what every preset of it sends
	Effort  map[string]map[string]any `json:"effort"` // by provider: the params an effort is, "$" the value
	Presets []sourcePreset            `json:"presets"`
}

type sourcePreset struct {
	Provider string         `json:"provider"`
	Model    string         `json:"model"`
	Effort   string         `json:"effort"` // the one the provider uses when none is sent
	Tools    string         `json:"tools"`
	Variant  string         `json:"variant"`      // the id suffix and name suffix: provider/model:variant, "<name> <Variant>"
	Params   map[string]any `json:"params"`       // this preset's own, merged over the provider's and the effort's
	Price    float64        `json:"price"`        // a variant's price as a multiple of the model's
	Source   string         `json:"price_source"` // where a variant's price is published
}

type devProvider struct {
	Models map[string]model `json:"models"`
}

type model struct {
	Name  string `json:"name"`
	Limit struct {
		Context int `json:"context"`
	} `json:"limit"`
	Modalities struct {
		Input []string `json:"input"`
	} `json:"modalities"`
	Cost      catalog.Cost `json:"cost"`
	Reasoning []struct {
		Type   string   `json:"type"`
		Values []string `json:"values"`
	} `json:"reasoning_options"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "catalog:", err)
		os.Exit(1)
	}
}

func run() error {
	var src source
	if err := json.Unmarshal(sourceJSON, &src); err != nil {
		return err
	}
	resp, err := http.Get(modelsDev)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	var dev map[string]devProvider
	if err := json.NewDecoder(resp.Body).Decode(&dev); err != nil {
		return err
	}
	c := catalog.Catalog{Version: time.Now().UTC().Format(time.RFC3339), Providers: map[string]catalog.Provider{}, Presets: map[string]catalog.Preset{}, Groups: map[string][]string{}}
	for id, p := range src.Providers {
		c.Providers[id] = p.Provider
	}
	for _, s := range src.Presets {
		pr, err := preset(src, dev, s)
		if err != nil {
			return err
		}
		id := s.Provider + "/" + s.Model
		if s.Variant != "" {
			id += ":" + s.Variant
		}
		if _, dup := c.Presets[id]; dup {
			return fmt.Errorf("%s: twice in source.json", id)
		}
		c.Presets[id] = pr
		c.Groups["self-hosted-all"] = append(c.Groups["self-hosted-all"], id)
	}
	out := json.NewEncoder(os.Stdout)
	out.SetIndent("", "  ")
	return out.Encode(c)
}

// preset is the catalog's preset for one in source.json.
func preset(src source, dev map[string]devProvider, s sourcePreset) (catalog.Preset, error) {
	p, ok := src.Providers[s.Provider]
	if !ok {
		return catalog.Preset{}, fmt.Errorf("%s/%s: no provider %s", s.Provider, s.Model, s.Provider)
	}
	m, ok := dev[p.ModelsDev].Models[s.Model]
	if !ok {
		return catalog.Preset{}, fmt.Errorf("%s/%s: not in models.dev %s", s.Provider, s.Model, p.ModelsDev)
	}
	envelope := provider.Shapes[p.Shape](provider.Config{}).Envelope
	pr := catalog.Preset{Name: m.Name, Provider: s.Provider, Model: s.Model, Tools: s.Tools, ContextLimit: min(contextLimit, m.Limit.Context), Input: []string{}, Cost: m.Cost}
	if p.Plan {
		pr.Cost = catalog.Cost{}
	}
	if pr.Tools == "" {
		pr.Tools = "default"
	}
	for _, mod := range m.Modalities.Input {
		for _, t := range media[mod] {
			if envelope.Accepts(t) {
				pr.Input = append(pr.Input, t)
			}
		}
	}
	if (s.Variant == "") != (s.Price == 0) || (s.Variant == "") != (s.Source == "") {
		return catalog.Preset{}, fmt.Errorf("%s/%s: a variant, and only a variant, has price and price_source", s.Provider, s.Model)
	}
	if s.Variant != "" {
		pr.Name += " " + strings.ToUpper(s.Variant[:1]) + s.Variant[1:]
		times := func(v float64) float64 { return math.Round(v*s.Price*1e6) / 1e6 } // 0.1*6 is 0.6000000000000001
		c := pr.Cost
		pr.Cost = catalog.Cost{Input: times(c.Input), Output: times(c.Output), CacheRead: times(c.CacheRead), CacheWrite: times(c.CacheWrite)}
	}
	params := merge(map[string]any{}, src.Params[s.Provider])
	if s.Effort != "" {
		var levels []string
		for _, r := range m.Reasoning {
			if r.Type == "effort" {
				levels = r.Values
			}
		}
		if !slices.Contains(levels, s.Effort) {
			return catalog.Preset{}, fmt.Errorf("%s/%s: effort %q is not one of %v", s.Provider, s.Model, s.Effort, levels)
		}
		set := catalog.Setting{Name: "effort"}
		for _, l := range levels {
			v, err := effort(src.Effort[s.Provider], l)
			if err != nil {
				return catalog.Preset{}, err
			}
			set.Values = append(set.Values, catalog.Value{Name: l, Params: raw(v)})
			if l == s.Effort {
				params = merge(params, v)
			}
		}
		pr.Settings = []catalog.Setting{set}
	}
	pr.Params = raw(merge(params, s.Params))
	return pr, nil
}

// effort is the shape's effort params with "$" made the level.
func effort(tmpl map[string]any, level string) (map[string]any, error) {
	if tmpl == nil {
		return nil, fmt.Errorf("no effort params for a provider with effort %s", level)
	}
	b, _ := json.Marshal(tmpl)
	var v map[string]any
	return v, json.Unmarshal([]byte(strings.ReplaceAll(string(b), `"$"`, `"`+level+`"`)), &v)
}

// merge copies src over dst, object into object.
func merge(dst, src map[string]any) map[string]any {
	for k, v := range src {
		if sv, ok := v.(map[string]any); ok {
			dv, _ := dst[k].(map[string]any)
			v = merge(merge(map[string]any{}, dv), sv)
		}
		dst[k] = v
	}
	return dst
}

func raw(m map[string]any) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	for k, v := range m {
		out[k], _ = json.Marshal(v)
	}
	return out
}
