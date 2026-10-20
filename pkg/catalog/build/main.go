// Build prints the catalog: source.json, our choices, filled in with each
// model's name, limits, input and prices from models.dev. It fails on a
// preset models.dev does not have, or an effort the model does not take.
//
//	go run ./pkg/catalog/build > catalog.json
package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
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
	} `json:"providers"`
	Params  map[string]map[string]any `json:"params"` // by shape: what every preset of it sends
	Effort  map[string]map[string]any `json:"effort"` // by shape: the params an effort is, "$" the value
	Presets []sourcePreset            `json:"presets"`
}

type sourcePreset struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Effort   string `json:"effort"` // the one the provider uses when none is sent
	Tools    string `json:"tools"`
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
	envelope := provider.Shapes[p.Shape]("", "", "").Envelope
	pr := catalog.Preset{Name: m.Name, Provider: s.Provider, Model: s.Model, Tools: s.Tools, ContextLimit: min(contextLimit, m.Limit.Context), Input: []string{}, Cost: m.Cost}
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
	params := merge(map[string]any{}, src.Params[p.Shape])
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
			v, err := effort(src.Effort[p.Shape], l)
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
	pr.Params = raw(params)
	return pr, nil
}

// effort is the shape's effort params with "$" made the level.
func effort(tmpl map[string]any, level string) (map[string]any, error) {
	if tmpl == nil {
		return nil, fmt.Errorf("no effort params for a shape with effort %s", level)
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
