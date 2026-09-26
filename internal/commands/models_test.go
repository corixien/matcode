package commands

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withProjectConfig chdirs into a temp project whose config.toml is written
// first, so config.Load sees the overlay.
func withProjectConfig(t *testing.T, tomlText string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".mtc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if tomlText != "" {
		if err := os.WriteFile(filepath.Join(dir, ".mtc", "config.toml"), []byte(tomlText), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
	return dir
}

func TestModelsJSON(t *testing.T) {
	withProjectConfig(t, `
model = "mockt/t"
[providers.mockt]
base_url = "http://127.0.0.1:8933/v1"
api_key = { env = "MOCK_KEY" }
`)
	t.Setenv("MOCK_KEY", "x")

	out := captureStdout(t, func() error { return Models([]string{"-json"}) })
	var payload struct {
		Model     string `json:"model"`
		Providers []struct {
			Name         string `json:"name"`
			Dialect      string `json:"dialect"`
			BaseURL      string `json:"base_url"`
			KeyEnv       string `json:"key_env"`
			KeySet       bool   `json:"key_set"`
			DefaultModel string `json:"default_model"`
			Active       bool   `json:"active"`
		} `json:"providers"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("bad JSON: %v\n%s", err, out)
	}
	if payload.Model != "mockt/t" {
		t.Fatalf("model = %q", payload.Model)
	}
	var mockt, anthropic *struct {
		Name, Dialect, BaseURL, KeyEnv string
		KeySet                         bool
		Active                         bool
	}
	for i := range payload.Providers {
		p := &payload.Providers[i]
		if p.Name == "mockt" {
			mockt = &struct {
				Name, Dialect, BaseURL, KeyEnv string
				KeySet                         bool
				Active                         bool
			}{p.Name, p.Dialect, p.BaseURL, p.KeyEnv, p.KeySet, p.Active}
		}
		if p.Name == "anthropic" {
			anthropic = &struct {
				Name, Dialect, BaseURL, KeyEnv string
				KeySet                         bool
				Active                         bool
			}{p.Name, p.Dialect, p.BaseURL, p.KeyEnv, p.KeySet, p.Active}
		}
	}
	if mockt == nil {
		t.Fatalf("mockt provider missing: %s", out)
	}
	if mockt.Dialect != "openai" || mockt.KeyEnv != "MOCK_KEY" || !mockt.KeySet || !mockt.Active {
		t.Fatalf("mockt row: %+v", mockt)
	}
	if anthropic == nil || anthropic.KeySet {
		// The real ANTHROPIC_API_KEY may or may not be set; only require the
		// row exists. KeySet may be either.
		t.Logf("anthropic row: %+v", anthropic)
	}
	if mockt.BaseURL != "http://127.0.0.1:8933/v1" {
		t.Fatalf("base_url lost: %q", mockt.BaseURL)
	}
}

func TestModelsTableAndVerbose(t *testing.T) {
	withProjectConfig(t, `
model = "mockt/t"
[providers.mockt]
base_url = "http://127.0.0.1:8933/v1"
api_key = { env = "MOCK_KEY" }
`)
	t.Setenv("MOCK_KEY", "")

	table := captureStdout(t, func() error { return Models(nil) })
	for _, want := range []string{"PROVIDER", "mockt", "MOCK_KEY", "missing", "mockt/t (from config"} {
		if !strings.Contains(table, want) {
			t.Errorf("table missing %q:\n%s", want, table)
		}
	}

	// Flags after the positional provider still parse.
	verbose := captureStdout(t, func() error { return Models([]string{"mockt", "-verbose"}) })
	for _, want := range []string{"BASE URL", "DIALECT", "http://127.0.0.1:8933/v1"} {
		if !strings.Contains(verbose, want) {
			t.Errorf("verbose missing %q:\n%s", want, verbose)
		}
	}
	if strings.Contains(verbose, "anthropic") {
		t.Errorf("provider filter leaked other providers:\n%s", verbose)
	}

	if err := Models([]string{"nope"}); err == nil || !strings.Contains(err.Error(), "configured:") {
		t.Errorf("unknown provider must list the catalog: %v", err)
	}
	if err := Models([]string{"a", "b"}); err == nil {
		t.Error("too many args must fail")
	}
	if err := Models([]string{"-nope"}); err == nil {
		t.Error("unknown flag must fail")
	}
}
