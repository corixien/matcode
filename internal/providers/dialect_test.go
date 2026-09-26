package providers

import (
	"encoding/json"
	"strings"
	"testing"

	"matcode/internal/config"
)

// TestForSelectsDialect proves the registry maps "provider/model" onto the
// configured dialect, trims the base URL, and reports each failure mode.
func TestForSelectsDialect(t *testing.T) {
	t.Setenv("MTC_TEST_PROVIDERS_KEY", "dummy-key")
	cfg := &config.Config{Providers: map[string]config.Provider{
		"ant": {
			Dialect:      "anthropic",
			BaseURL:      "https://api.example/v1/",
			APIKey:       config.EnvRef{Env: "MTC_TEST_PROVIDERS_KEY"},
			DefaultModel: "claude-x",
		},
		"oai": {
			Dialect:      "openai",
			BaseURL:      "https://api.example/v1",
			APIKey:       config.EnvRef{Env: "MTC_TEST_PROVIDERS_KEY"},
			DefaultModel: "gpt-x",
		},
		"nokey": {Dialect: "openai", BaseURL: "https://api.example/v1"},
		"unset": {Dialect: "anthropic", BaseURL: "https://api.example/v1",
			APIKey: config.EnvRef{Env: "MTC_TEST_PROVIDERS_MISSING_XYZ"}},
	}}

	// "provider/" with an empty model half falls back to default_model.
	p, model, err := For(cfg, "ant/")
	if err != nil {
		t.Fatal(err)
	}
	if a, ok := p.(*anthropic); !ok {
		t.Fatalf("anthropic dialect gave %T", p)
	} else if a.baseURL != "https://api.example/v1" {
		t.Errorf("baseURL = %q, trailing slash must be trimmed", a.baseURL)
	}
	if model != "claude-x" {
		t.Errorf("model = %q, want the configured default", model)
	}

	p, model, err = For(cfg, "oai/m2")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.(*openAICompat); !ok {
		t.Fatalf("openai dialect gave %T", p)
	}
	if model != "m2" {
		t.Errorf("model = %q, want ref-supplied id", model)
	}
	p, _, err = For(cfg, "oai/")
	if err != nil || p == nil {
		t.Fatalf("default model: %v", err)
	}

	if _, _, err := For(cfg, "nope/m"); err == nil || !strings.Contains(err.Error(), "unknown provider") {
		t.Errorf("unknown provider error = %v", err)
	}
	if _, _, err := For(cfg, "plainmodel"); err == nil || !strings.Contains(err.Error(), "provider prefix") {
		t.Errorf("unprefixed ref error = %v", err)
	}
	// No api_key.env configured → no key, but resolution still succeeds.
	if p, _, err := For(cfg, "nokey/m"); err != nil {
		t.Errorf("env-less provider: %v", err)
	} else if a, ok := p.(*openAICompat); !ok || a.apiKey != "" {
		t.Errorf("apiKey = %q, want empty", a.apiKey)
	}
	// Named env var missing → the error names the variable, never its value.
	if _, _, err := For(cfg, "unset/m"); err == nil || !strings.Contains(err.Error(), "MTC_TEST_PROVIDERS_MISSING_XYZ") {
		t.Errorf("missing env error = %v", err)
	}
}

// TestBuildOpenAIDialect pins the OpenAI wire shaping: bare string content
// when there is no media, content-part arrays when there is, and the
// tool-result + follow-up-user split the tool role forces.
func TestBuildOpenAIDialect(t *testing.T) {
	out := buildOpenAIMessages([]Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "yo"},
		{Role: "user", Content: "look", Media: []Media{
			{Type: "image/png", Data: "QUJD", Name: "a.png"},
			{Type: "application/pdf", Data: "UEZE", Name: "doc.pdf"},
		}},
		{Role: "tool", Content: "res", ToolCallID: "call_9"},
		{Role: "tool", Content: "img", ToolCallID: "call_10",
			Media: []Media{{Type: "image/jpeg", Data: "RkxPQQ=="}}},
		{Role: "compaction", Content: "checkpoint"},
	})
	// compaction events never reach the provider.
	if len(out) != 6 {
		t.Fatalf("messages = %d, want 6 (compaction dropped)", len(out))
	}
	if s, ok := out[0].Content.(string); !ok || s != "hi" {
		t.Errorf("plain content = %#v, want string", out[0].Content)
	}
	parts, ok := out[2].Content.([]openAIContent)
	if !ok {
		t.Fatalf("media content = %T, want []openAIContent", out[2].Content)
	}
	// A PDF is unsupported by the dialect: it degrades into a text note on
	// the kept text part, while the image becomes an image_url data URI.
	if parts[0].Text != "look\n[attached application/pdf (doc.pdf)]" {
		t.Errorf("text part = %q", parts[0].Text)
	}
	if len(parts) != 2 || parts[1].ImageURL == nil ||
		parts[1].ImageURL.URL != "data:image/png;base64,QUJD" {
		t.Fatalf("image part = %+v", parts)
	}
	if out[3].Role != "tool" || out[3].Content != "res" || out[3].ToolCallID != "call_9" {
		t.Errorf("tool result = %+v", out[3])
	}
	// Tool messages carry no media support: the images ride in a follow-up
	// user message right after the plain tool message.
	if out[4].Role != "tool" || out[4].ToolCallID != "call_10" {
		t.Errorf("media tool result = %+v", out[4])
	}
	follow, ok := out[5].Content.([]openAIContent)
	if !ok || out[5].Role != "user" {
		t.Fatalf("follow-up = %+v", out[5])
	}
	if len(follow) != 2 || follow[0].Text != "" || follow[1].ImageURL == nil ||
		follow[1].ImageURL.URL != "data:image/jpeg;base64,RkxPQQ==" {
		t.Fatalf("follow-up parts = %+v", follow)
	}
}

// TestBuildAnthropicDialect pins the Anthropic content-block shaping:
// text/image/document blocks, tool results mapped onto the user role, and
// media-only messages that never emit an empty text block.
func TestBuildAnthropicDialect(t *testing.T) {
	out, err := buildAnthropicMessages([]Message{
		{Role: "user", Content: "hi"},
		{Role: "user", Content: "look", Media: []Media{
			{Type: "image/png", Data: "QUJD", Name: "a.png"},
			{Type: "application/pdf", Data: "UEZE", Name: "doc.pdf"},
			{Type: "video/mp4", Data: "dmlk", Name: "clip.mp4"},
		}},
		{Role: "user", Content: "", Media: []Media{{Type: "image/gif", Data: "R0lGOD"}}},
		{Role: "tool", Content: "res", ToolCallID: "call_9"},
		{Role: "tool", Content: "img", ToolCallID: "call_10",
			Media: []Media{{Type: "image/png", Data: "dG9vbQ=="}}},
		{Role: "compaction", Content: "checkpoint"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Unlike the OpenAI dialect, a media-carrying tool result nests its
	// blocks instead of adding a follow-up message.
	if len(out) != 5 {
		t.Fatalf("messages = %d, want 5 (compaction dropped, tool media nested)", len(out))
	}
	blocks := decodeBlocks(t, out[0].Content)
	if len(blocks) != 1 || blocks[0]["type"] != "text" || blocks[0]["text"] != "hi" {
		t.Errorf("plain blocks = %v", blocks)
	}

	blocks = decodeBlocks(t, out[1].Content)
	// Only the unsupported type degrades to a text note: a PDF became a
	// document block below, so the note here carries just the video.
	if blocks[0]["type"] != "text" || blocks[0]["text"] != "look\n[attached video/mp4 (clip.mp4)]" {
		t.Errorf("first block = %v", blocks[0])
	}
	if blocks[1]["type"] != "image" || sourceData(t, blocks[1]) != "QUJD" {
		t.Errorf("image block = %v", blocks[1])
	}
	if blocks[2]["type"] != "document" || sourceData(t, blocks[2]) != "UEZE" {
		t.Errorf("pdf block = %v", blocks[2])
	}
	if len(blocks) != 3 {
		t.Errorf("unsupported media must degrade to a text note, blocks = %v", blocks)
	}

	// Media-only message: no empty text block (the API rejects one).
	blocks = decodeBlocks(t, out[2].Content)
	if len(blocks) != 1 || blocks[0]["type"] != "image" {
		t.Fatalf("media-only blocks = %v", blocks)
	}

	// Tool results travel as user messages holding a tool_result block.
	if out[3].Role != "user" {
		t.Errorf("tool result role = %q, want user", out[3].Role)
	}
	blocks = decodeBlocks(t, out[3].Content)
	if len(blocks) != 1 || blocks[0]["type"] != "tool_result" || blocks[0]["id"] != "call_9" ||
		blocks[0]["text"] != "res" {
		t.Errorf("tool_result = %v", blocks[0])
	}
	// A tool result with media nests the blocks inside the tool_result.
	blocks = decodeBlocks(t, out[4].Content)
	inner, ok := blocks[0]["content"].([]any)
	if !ok || len(inner) != 2 {
		t.Fatalf("tool_result content = %#v", blocks[0]["content"])
	}
	im := inner[1].(map[string]any)
	if im["type"] != "image" || sourceData(t, im) != "dG9vbQ==" {
		t.Errorf("nested image = %v", im)
	}
}

func decodeBlocks(t *testing.T, raw json.RawMessage) []map[string]any {
	t.Helper()
	var blocks []map[string]any
	if err := json.Unmarshal(raw, &blocks); err != nil {
		t.Fatalf("content blocks: %v (%s)", err, raw)
	}
	return blocks
}

func sourceData(t *testing.T, block map[string]any) string {
	t.Helper()
	src, ok := block["source"].(map[string]any)
	if !ok {
		t.Fatalf("no source on %v", block)
	}
	if src["type"] != "base64" {
		t.Errorf("source type = %v", src["type"])
	}
	data, _ := src["data"].(string)
	return data
}
