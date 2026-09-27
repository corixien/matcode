package providers

import (
	"fmt"
	"strings"

	"matcode/internal/config"
)

// For resolves a "provider/modelid" reference into a live provider plus the
// bare model id to send upstream.
func For(cfg *config.Config, ref string) (Provider, string, error) {
	name, model := splitRef(ref)
	if name == "" {
		return nil, "", fmt.Errorf("model %q needs a provider prefix, e.g. anthropic/claude-sonnet-4-5", ref)
	}
	spec, ok := cfg.Provider(name)
	if !ok {
		return nil, "", fmt.Errorf("unknown provider %q (known: %s)", name, strings.Join(names(cfg), ", "))
	}
	key, err := spec.ResolveKey()
	if err != nil {
		return nil, "", fmt.Errorf("%s: %w (set it in your shell, in the data dir .env, or with /provider in the TUI)", name, err)
	}
	if model == "" {
		model = spec.DefaultModel
	}
	if model == "" {
		return nil, "", fmt.Errorf("provider %q has no default model; use provider/model", name)
	}

	base := strings.TrimRight(spec.BaseURL, "/")
	if spec.Dialect == "anthropic" {
		return &anthropic{name: name, baseURL: base, apiKey: key}, model, nil
	}
	return &openAICompat{name: name, baseURL: base, apiKey: key}, model, nil
}

func splitRef(ref string) (name, model string) {
	if i := strings.Index(ref, "/"); i > 0 {
		return ref[:i], ref[i+1:]
	}
	return "", ref
}

func names(cfg *config.Config) []string {
	out := make([]string, 0, len(cfg.Providers))
	for n := range cfg.Providers {
		out = append(out, n)
	}
	return out
}
