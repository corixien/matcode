package providers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"matcode/internal/config"
)

// TestOpenRouterFromDotEnv: an OpenRouter key lives in the data dir .env.
// For resolves the preset default model (empty model part) and keeps the
// full vendor path when one is passed — openrouter/anthropic/... sends
// anthropic/claude-sonnet-4.5 because the catalog splits only the first
// slash.
func TestOpenRouterFromDotEnv(t *testing.T) {
	home := t.TempDir()
	global := filepath.Join(home, ".config", "mtc")
	if err := os.MkdirAll(global, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(global, ".env"),
		[]byte("OPENROUTER_API_KEY=sk-or-v1-test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("OPENROUTER_API_KEY", "") // the .env provides it
	cfg, err := config.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	// Empty model part → provider preset default (the picker's fallback).
	p, model, err := For(cfg, "openrouter/")
	if err != nil {
		t.Fatalf("For(openrouter/): %v", err)
	}
	if p == nil {
		t.Fatal("nil provider")
	}
	if model != "anthropic/claude-sonnet-4.5" {
		t.Errorf("model = %q, want anthropic/claude-sonnet-4.5", model)
	}

	_, model, err = For(cfg, "openrouter/anthropic/claude-sonnet-4.5")
	if err != nil {
		t.Fatalf("For(full ref): %v", err)
	}
	if model != "anthropic/claude-sonnet-4.5" {
		t.Errorf("model = %q, want anthropic/claude-sonnet-4.5", model)
	}
}

// TestOpenRouterNoKey: without a credential the error names the fix (/provider).
func TestOpenRouterNoKey(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // no global .env
	t.Setenv("OPENROUTER_API_KEY", "")
	cfg, err := config.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = For(cfg, "openrouter/anthropic/claude-sonnet-4.5")
	if err == nil {
		t.Fatal("want error without key")
	}
	if !strings.Contains(err.Error(), "/provider") {
		t.Errorf("error should mention /provider, got: %v", err)
	}
}
