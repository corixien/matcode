// Package catalog embeds a trimmed models.dev snapshot (spec §3 line 258):
// per model — name, context window, cost, capabilities — merged with
// whatever the provider reports. `mtc doctor` shows which source a model
// came from; the engine prices usage here when the provider itself does
// not (only OpenRouter reports cost).
//
// Regenerate models.json from https://models.dev/api.json with:
//
//	python3 -c "import json;d=json.load(open('models.json'));
//	keep=['anthropic','openai','google','openrouter','groq','xai','deepseek','mistral','opencode'];
//	t=lambda m:{k:v for k,v in {'name':m.get('name',''),'context':(m.get('limit') or {}).get('context'),
//	 'cost':{a:b for a,b in (m.get('cost') or {}).items() if a in ('input','output','cache_read','cache_write')},
//	 'caps':{a:True for a in ('attachment','tool_call','reasoning','structured_output') if m.get(a)}}.items() if v};
//	print(json.dumps({p:{'name':d[p]['name'],'models':{k:t(v) for k,v in sorted(d[p]['models'].items())}} for p in keep},sort_keys=True,separators=(',',':')))"
//
// Only the nine built-in providers are embedded (115 KB / 667 models);
// custom base-URL providers are not on models.dev and report no source.
package catalog

import (
	_ "embed"
	"encoding/json"
	"strings"
	"sync"
)

//go:embed models.json
var raw []byte

// Cost is USD per 1M tokens, as models.dev reports it.
type Cost struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cache_read,omitempty"`
	CacheWrite float64 `json:"cache_write,omitempty"`
}

// Caps are the per-model capability flags models.dev carries.
type Caps struct {
	Attachment       bool `json:"attachment,omitempty"`
	ToolCall         bool `json:"tool_call,omitempty"`
	Reasoning        bool `json:"reasoning,omitempty"`
	StructuredOutput bool `json:"structured_output,omitempty"`
}

// Model is one catalog entry.
type Model struct {
	Name    string `json:"name"`
	Context int    `json:"context,omitempty"`
	Cost    *Cost  `json:"cost,omitempty"`
	Caps    Caps   `json:"caps,omitempty"`
}

// Provider is one models.dev provider with its models keyed by the
// provider's own model id (the part after `provider/` in `provider/model`).
type Provider struct {
	Name   string           `json:"name"`
	Models map[string]Model `json:"models"`
}

var (
	once sync.Once
	data map[string]Provider
)

func load() { once.Do(func() { _ = json.Unmarshal(raw, &data) }) }

// Lookup resolves a `provider/model` reference against the catalog.
// The reference splits at the FIRST slash (openrouter model ids contain
// slashes themselves: openrouter/anthropic/claude-x).
func Lookup(ref string) (provider, model string, m Model, ok bool) {
	provider, model = Split(ref)
	if provider == "" {
		return provider, model, Model{}, false
	}
	load()
	p, found := data[provider]
	if !found {
		return provider, model, Model{}, false
	}
	m, found = p.Models[model]
	if !found {
		// models.dev is case- and punctuation-tolerant in practice.
		for k, v := range p.Models {
			if strings.EqualFold(k, model) {
				return provider, model, v, true
			}
		}
	}
	return provider, model, m, found
}

// LookupProvider returns one provider's catalog block.
func LookupProvider(name string) (Provider, bool) {
	load()
	p, ok := data[name]
	return p, ok
}

// Names lists embedded providers, sorted by embedded order (fixed).
func Names() []string {
	load()
	out := make([]string, 0, len(data))
	for k := range data {
		out = append(out, k)
	}
	return out
}

// Price computes USD for one response from catalog rates when the
// provider did not report a cost itself. ok=false without a rate.
func Price(ref string, tokensIn, tokensOut int) (usd float64, ok bool) {
	_, _, m, ok := Lookup(ref)
	if !ok || m.Cost == nil {
		return 0, false
	}
	usd = float64(tokensIn)/1e6*m.Cost.Input +
		float64(tokensOut)/1e6*m.Cost.Output
	return usd, true
}

// Split cuts `provider/model` at the first slash; a ref without one
// yields an empty provider (unresolvable).
func Split(ref string) (provider, model string) {
	i := strings.IndexByte(ref, '/')
	if i <= 0 || i == len(ref)-1 {
		return "", ""
	}
	return ref[:i], ref[i+1:]
}

// Context returns the catalog context window for a ref (0 = unknown).
func Context(ref string) int {
	_, _, m, ok := Lookup(ref)
	if !ok {
		return 0
	}
	return m.Context
}
