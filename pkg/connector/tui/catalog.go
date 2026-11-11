package tui

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/mainplane-ai/mainplane/pkg/catalog"
	"github.com/mainplane-ai/mainplane/pkg/harness"
	"github.com/mainplane-ai/mainplane/pkg/statefile"
)

const (
	// group is the catalog's list of presets this app shows as models.
	group = "self-hosted-all"
	// defaults is the catalog's list of the model a new session takes when none
	// is chosen and no session names one this harness serves: one per provider,
	// its middle one, so the first answer comes quickly.
	defaults = "self-hosted-default"
	// catalogGet bounds the fetch at start; the kept copy shows meanwhile.
	catalogGet = 30 * time.Second
	// customLimit is the context limit of a model the catalog does not have
	// when its provider sets none: under the 32k window docs/local-llms.md
	// asks a local server for.
	customLimit = 30_000
)

// loadCatalog gives the list the catalog kept in ~/.mainplane at once, then
// the one fetched now, which it keeps. The catalog is online only: with no
// kept copy and no fetch, every command that needs it fails.
func loadCatalog(a *App, l *sessionList) {
	h, err := os.UserHomeDir()
	if err != nil {
		a.SayError(err)
		return
	}
	path := filepath.Join(h, ".mainplane", "catalog.json")
	a.Go(func() {
		var kept catalog.Catalog
		if b, err := os.ReadFile(path); err == nil && json.Unmarshal(b, &kept) == nil {
			a.Post(func() { l.cat = &kept })
		}
		ctx, cancel := context.WithTimeout(context.Background(), catalogGet)
		defer cancel()
		c, err := catalog.Fetch(ctx)
		if err != nil {
			a.Post(func() { a.SayError(fmt.Errorf("catalog: %w", err)) })
			return
		}
		a.Post(func() { l.cat = &c })
		if err := keep(path, c); err != nil {
			a.Post(func() { a.SayError(fmt.Errorf("catalog: %w", err)) })
		}
	})
}

// keep writes c to path whole or not at all: a write cut short leaves the
// copy kept before.
func keep(path string, c catalog.Catalog) error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	tmp := path + ".new"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// presetConf is the config a session made from preset id has, but its
// workers.
func presetConf(id string, p catalog.Preset) statefile.Conf {
	return statefile.Conf{Model: p.Provider + "/" + p.Model, Tools: p.Tools, ContextLimit: p.ContextLimit, Input: p.Input, Params: p.Params, Preset: id}
}

// custom is the openai-chat providers the harness serves that the catalog
// does not have, such as a local server: their servers list their models.
func custom(cat *catalog.Catalog, ps []harness.ProviderInfo) []harness.ProviderInfo {
	var out []harness.ProviderInfo
	for _, p := range ps {
		if _, ok := cat.Providers[p.Name]; !ok && p.Shape == "openai-chat" {
			out = append(out, p)
		}
	}
	return out
}

// customConf is the config a session on model of custom provider p has, but
// its workers.
func customConf(p harness.ProviderInfo, model string) statefile.Conf {
	return statefile.Conf{Model: p.Name + "/" + model, Tools: "default", ContextLimit: cmp.Or(p.ContextLimit, customLimit), Input: []string{}}
}

// connected is the presets of the group whose provider the harness serves,
// in the group's order.
func connected(cat *catalog.Catalog, ps []harness.ProviderInfo) []string {
	var ids []string
	for _, id := range cat.Groups[group] {
		p := cat.Presets[id]
		if slices.ContainsFunc(ps, func(x harness.ProviderInfo) bool { return x.Name == p.Provider }) {
			ids = append(ids, id)
		}
	}
	return ids
}

// defaultConf is the config of a session made with nothing chosen: the
// newest session's when its provider is custom, else the preset of the
// newest session, else the first of defaults, else the first one
// connected, each only when the harness serves its provider. The workers are
// the newest session's.
func defaultConf(cat *catalog.Catalog, ps []harness.ProviderInfo, newest []harness.Info) (statefile.Conf, error) {
	if len(newest) > 0 {
		cf := newest[0].Config
		name, _, _ := strings.Cut(cf.Model, "/")
		if slices.ContainsFunc(custom(cat, ps), func(p harness.ProviderInfo) bool { return p.Name == name }) {
			return cf, nil
		}
	}
	ids := connected(cat, ps)
	if len(ids) == 0 {
		return statefile.Conf{}, errors.New("no provider of the catalog is connected: /providers, or pick a model of your own server: /model")
	}
	want := cat.Groups[defaults]
	var workers []statefile.Worker
	if len(newest) > 0 {
		cf := newest[0].Config
		want, workers = append([]string{cf.Preset, cf.Model}, want...), cf.Workers
	}
	pick := ids[0]
	for _, id := range want {
		if slices.Contains(ids, id) {
			pick = id
			break
		}
	}
	cf := presetConf(pick, cat.Presets[pick])
	cf.Workers = workers
	return cf, nil
}

// price is the cost of a config: its preset's, else the preset of its
// model's; unknown is free.
func price(cat *catalog.Catalog, cf statefile.Conf) catalog.Cost {
	for _, id := range []string{cf.Preset, cf.Model} {
		if p, ok := cat.Presets[id]; ok {
			return p.Cost
		}
	}
	return catalog.Cost{}
}

// costOf is what usage cost at c, in USD.
func costOf(c catalog.Cost, u statefile.Usage) float64 {
	return (float64(u.Input)*c.Input + float64(u.Output)*c.Output + float64(u.CacheRead)*c.CacheRead + float64(u.CacheWrite)*c.CacheWrite) / 1e6
}

// merge is patch over params: an object into an object, key by key; any
// other value replaces.
func merge(params, patch map[string]json.RawMessage) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	for k, v := range params {
		out[k] = v
	}
	for k, v := range patch {
		var a, b map[string]json.RawMessage
		if json.Unmarshal(out[k], &a) == nil && json.Unmarshal(v, &b) == nil && a != nil && b != nil {
			m, err := json.Marshal(merge(a, b))
			if err != nil {
				panic(err) // raw values that unmarshaled marshal again
			}
			v = m
		}
		out[k] = v
	}
	return out
}

// holds is whether params has every value of sub, objects compared key by
// key.
func holds(params, sub map[string]json.RawMessage) bool {
	for k, v := range sub {
		have, ok := params[k]
		if !ok {
			return false
		}
		var a, b map[string]json.RawMessage
		if json.Unmarshal(have, &a) == nil && json.Unmarshal(v, &b) == nil && a != nil && b != nil {
			if !holds(a, b) {
				return false
			}
			continue
		}
		var x, y bytes.Buffer
		if json.Compact(&x, have) != nil || json.Compact(&y, v) != nil || x.String() != y.String() {
			return false
		}
	}
	return true
}
