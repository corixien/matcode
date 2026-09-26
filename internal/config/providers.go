package config

// defaultProviders is the built-in catalog: every service matcode knows how to
// talk to without any config file. A config.toml [providers.x] entry overlays
// these per key, so the file only has to exist when something differs.
func defaultProviders() map[string]Provider {
	return map[string]Provider{
		"anthropic": {
			Dialect:      "anthropic",
			BaseURL:      "https://api.anthropic.com/v1",
			APIKey:       EnvRef{Env: "ANTHROPIC_API_KEY"},
			DefaultModel: "claude-sonnet-4-5",
		},
		"openai": {
			Dialect: "openai",
			BaseURL: "https://api.openai.com/v1",
			APIKey:  EnvRef{Env: "OPENAI_API_KEY"},
		},
		"google": {
			Dialect: "openai", // Gemini's OpenAI-compatible endpoint
			BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai/",
			APIKey:  EnvRef{Env: "GEMINI_API_KEY"},
		},
		"openrouter": {
			Dialect: "openai",
			BaseURL: "https://openrouter.ai/api/v1",
			APIKey:  EnvRef{Env: "OPENROUTER_API_KEY"},
			// Full "vendor/model" path: OpenRouter ids keep their own
			// vendor segment (catalog splits only the first slash).
			DefaultModel: "anthropic/claude-sonnet-4.5",
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
		"opencode": {
			Dialect: "openai",
			BaseURL: "https://opencode.ai/zen/v1",
			APIKey:  EnvRef{Env: "OPENCODE_API_KEY"},
		},
	}
}
