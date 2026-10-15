package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/mainplane-ai/mainplane/pkg/statefile"
)

// The harness keeps no prices: the TUI reads LiteLLM's price list, which is
// data, not code, so prices follow new models without a release. It is kept
// in ~/.mainplane/prices.json and fetched again after a day.
const (
	pricesURL = "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"
	pricesAge = 24 * time.Hour
	pricesGet = 30 * time.Second
)

// prices is the list by model, each entry left raw until it is asked for:
// some entries are not prices.
type prices map[string]json.RawMessage

type price struct {
	In         float64 `json:"input_cost_per_token"`
	Out        float64 `json:"output_cost_per_token"`
	CacheRead  float64 `json:"cache_read_input_token_cost"`
	CacheWrite float64 `json:"cache_creation_input_token_cost"`
}

// loadPrices is the kept list when it is fresh and reads, else the list
// fetched now, else the kept list however old.
func loadPrices() (prices, error) {
	h, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(h, ".mainplane", "prices.json")
	kept, kerr := os.ReadFile(path)
	if st, err := os.Stat(path); kerr == nil && err == nil && time.Since(st.ModTime()) < pricesAge {
		if p, err := parsePrices(kept); err == nil {
			return p, nil
		}
	}
	b, err := fetchPrices()
	if err != nil {
		if kerr == nil {
			return parsePrices(kept)
		}
		return nil, err
	}
	p, err := parsePrices(b)
	if err == nil {
		_ = os.WriteFile(path, b, 0o644) // a list that is not kept is fetched again next start
	}
	return p, err
}

func fetchPrices() ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), pricesGet)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", pricesURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("prices: %s", resp.Status)
	}
	return io.ReadAll(resp.Body)
}

func parsePrices(b []byte) (prices, error) {
	var p prices
	return p, json.Unmarshal(b, &p)
}

// cost is what one step's usage cost, and whether the model has a price:
// LiteLLM names it provider/model or plain model.
func (p prices) cost(provider, model string, u statefile.Usage) (float64, bool) {
	for _, k := range []string{provider + "/" + model, model} {
		var pr price
		if raw, ok := p[k]; ok && json.Unmarshal(raw, &pr) == nil && (pr.In > 0 || pr.Out > 0) {
			return float64(u.Input)*pr.In + float64(u.Output)*pr.Out + float64(u.CacheRead)*pr.CacheRead + float64(u.CacheWrite)*pr.CacheWrite, true
		}
	}
	return 0, false
}
