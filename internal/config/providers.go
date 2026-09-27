package config

// defaultProviders is the built-in catalog: every service matcode knows how to
// talk to without any config file. A config.toml [providers.x] entry overlays
// these per key, so the file only has to exist when something differs.
//
// Each entry carries a curated Models list (best-first) so the model picker
// can offer every model a stored key unlocks without a network round trip.
// Providers without one fall back to the embedded models.dev catalog.
func defaultProviders() map[string]Provider {
	return map[string]Provider{
		"anthropic": {
			Dialect:      "anthropic",
			BaseURL:      "https://api.anthropic.com/v1",
			APIKey:       EnvRef{Env: "ANTHROPIC_API_KEY"},
			DefaultModel: "claude-sonnet-4-5",
			Models: []string{
				"claude-sonnet-4-5", "claude-sonnet-4-6", "claude-haiku-4-5",
				"claude-opus-4-5", "claude-opus-4-6", "claude-opus-4-7",
				"claude-opus-4-8", "claude-opus-5", "claude-opus-5-5",
				"claude-fable-5",
			},
		},
		"openai": {
			Dialect:      "openai",
			BaseURL:      "https://api.openai.com/v1",
			APIKey:       EnvRef{Env: "OPENAI_API_KEY"},
			DefaultModel: "gpt-5.1",
			Models: []string{
				"gpt-5.1", "gpt-5", "gpt-5.2", "gpt-5.4", "gpt-5.5",
				"gpt-5.6", "gpt-5-mini", "gpt-4.1", "gpt-4.1-mini",
				"gpt-4o", "gpt-4o-mini",
			},
		},
		"google": {
			Dialect:      "openai", // Gemini's OpenAI-compatible endpoint
			BaseURL:      "https://generativelanguage.googleapis.com/v1beta/openai/",
			APIKey:       EnvRef{Env: "GEMINI_API_KEY"},
			DefaultModel: "gemini-2.5-pro",
			Models: []string{
				"gemini-2.5-pro", "gemini-2.5-flash", "gemini-2.5-flash-lite",
				"gemini-3.1-pro-preview", "gemini-3.5-flash", "gemini-flash-latest",
			},
		},
		"openrouter": {
			Dialect: "openai",
			BaseURL: "https://openrouter.ai/api/v1",
			APIKey:  EnvRef{Env: "OPENROUTER_API_KEY"},
			// Full "vendor/model" path: OpenRouter ids keep their own
			// vendor segment (catalog splits only the first slash).
			DefaultModel: "anthropic/claude-sonnet-4.5",
			Models: []string{
				"anthropic/claude-sonnet-4.5", "anthropic/claude-sonnet-4.6",
				"anthropic/claude-opus-4.5", "anthropic/claude-opus-5",
				"openai/gpt-5", "openai/gpt-5.1", "openai/gpt-5-mini",
				"google/gemini-2.5-pro", "deepseek/deepseek-chat",
			},
		},
		"groq": {
			Dialect: "openai",
			BaseURL: "https://api.groq.com/openai/v1",
			APIKey:  EnvRef{Env: "GROQ_API_KEY"},
		},
		"xai": {
			Dialect: "openai",
			BaseURL: "https://api.x.ai/v1",
			APIKey:  EnvRef{Env: "XAI_API_KEY"},
		},
		"deepseek": {
			Dialect: "openai",
			BaseURL: "https://api.deepseek.com/v1",
			APIKey:  EnvRef{Env: "DEEPSEEK_API_KEY"},
		},
		"mistral": {
			Dialect: "openai",
			BaseURL: "https://api.mistral.ai/v1",
			APIKey:  EnvRef{Env: "MISTRAL_API_KEY"},
		},
		// OpenCode Zen — the curated OpenCode gateway. One key unlocks
		// every model below; models.dev carries an "opencode" entry but
		// these ids are the gateway's own (docs: opencode.ai/docs/zen).
		"opencode": {
			Dialect:      "openai",
			BaseURL:      "https://opencode.ai/zen/v1",
			APIKey:       EnvRef{Env: "OPENCODE_API_KEY"},
			DefaultModel: "claude-sonnet-4-5",
			Models: []string{
				"claude-sonnet-4-5", "claude-sonnet-4-6", "claude-haiku-4-5",
				"gpt-5.1", "gpt-5.5", "kimi-k3", "glm-5.3",
				"deepseek-v4-pro", "deepseek-v4-flash",
				"qwen3.8-max", "minimax-m3",
				"mimo-v2.6-flash-free", "space-bunny-free",
			},
		},
		// OpenCode Go — the flat-rate pool behind the same account
		// (docs: opencode.ai/docs/go); shares the Zen API key, separate
		// endpoint. Only chat/completions-served ids are listed.
		"opencode-go": {
			Dialect:      "openai",
			BaseURL:      "https://opencode.ai/zen/go/v1",
			APIKey:       EnvRef{Env: "OPENCODE_API_KEY"},
			DefaultModel: "glm-5.3",
			Models: []string{
				"glm-5.3", "glm-5.3-flash", "glm-5.2", "glm-5.1",
				"kimi-k3", "kimi-k2.7-code", "kimi-k2.6", "longcat-2.0",
				"deepseek-v4-pro", "deepseek-v4-flash", "deepseek-v4.1-flash",
				"mimo-v2.6-flash", "mimo-v2.6-pro", "mimo-v2.5",
				"hy4-preview", "hy3",
				"space-bunny-free", "longcat-2.5-preview-free",
			},
		},
	}
}
