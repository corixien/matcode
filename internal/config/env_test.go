package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadDotEnvForms covers the accepted line shapes and the shell-wins
// rule (an already-set variable is never overwritten).
func TestLoadDotEnvForms(t *testing.T) {
	for _, n := range []string{"MT_ENV_T1", "MT_ENV_T2", "MT_ENV_T3", "MT_ENV_T4"} {
		t.Setenv(n, "")
	}
	t.Setenv("MT_ENV_T2", "shell")
	path := filepath.Join(t.TempDir(), ".env")
	body := "# comment\n" +
		"\n" +
		"MT_ENV_T1=plain\n" +
		"export MT_ENV_T2=file\n" +
		"MT_ENV_T3=\"quoted value\"\n" +
		"MT_ENV_T4=\n" + // empty values are skipped
		"NOT A PAIR\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := loadDotEnv(path); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("MT_ENV_T1"); got != "plain" {
		t.Errorf("MT_ENV_T1 = %q, want plain", got)
	}
	if got := os.Getenv("MT_ENV_T2"); got != "shell" {
		t.Errorf("MT_ENV_T2 = %q, want shell (shell wins)", got)
	}
	if got := os.Getenv("MT_ENV_T3"); got != "quoted value" {
		t.Errorf("MT_ENV_T3 = %q, want quoted value", got)
	}
	if got := os.Getenv("MT_ENV_T4"); got != "" {
		t.Errorf("MT_ENV_T4 = %q, want empty", got)
	}
	// A missing file is not an error.
	if err := loadDotEnv(filepath.Join(t.TempDir(), "nope.env")); err != nil {
		t.Errorf("missing file: %v", err)
	}
}

// TestSetKeyCreateReplace checks persistence: create, replace an existing
// assignment without touching comments or other keys, 0600 mode, export.
func TestSetKeyCreateReplace(t *testing.T) {
	dir := t.TempDir()
	if err := SetKey(dir, "MT_ENV_K1", "v1"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".env")
	if got := os.Getenv("MT_ENV_K1"); got != "v1" {
		t.Errorf("env = %q, want v1", got)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %o, want 600", perm)
	}
	// Seed: comment, another key, the old value (export form).
	body := "# mtc\nMT_ENV_K2=keep\nexport MT_ENV_K1=old\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SetKey(dir, "MT_ENV_K1", "v2"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	if !strings.Contains(text, "MT_ENV_K1=v2") {
		t.Errorf("missing new value:\n%s", text)
	}
	if strings.Contains(text, "MT_ENV_K1=old") || strings.Contains(text, "export MT_ENV_K1") {
		t.Errorf("old assignment not replaced:\n%s", text)
	}
	if strings.Count(text, "MT_ENV_K1=") != 1 {
		t.Errorf("duplicate assignment:\n%s", text)
	}
	if !strings.Contains(text, "# mtc") || !strings.Contains(text, "MT_ENV_K2=keep") {
		t.Errorf("comments/other keys lost:\n%s", text)
	}
	if err := SetKey(dir, "", "v"); err == nil {
		t.Error("empty env name accepted")
	}
	if err := SetKey(dir, "MT_ENV_K3", ""); err == nil {
		t.Error("empty value accepted")
	}
}

// TestLoadGlobalDotEnv proves config.Load picks up the data dir .env.
func TestLoadGlobalDotEnv(t *testing.T) {
	home := t.TempDir()
	global := filepath.Join(home, ".config", "mtc")
	if err := os.MkdirAll(global, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("MT_ENV_GLOBAL", "")
	t.Setenv("MT_ENV_SH", "shell")
	if err := os.WriteFile(filepath.Join(global, ".env"),
		[]byte("MT_ENV_GLOBAL=from-file\nMT_ENV_SH=from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("MT_ENV_GLOBAL"); got != "from-file" {
		t.Errorf("MT_ENV_GLOBAL = %q, want from-file", got)
	}
	if got := os.Getenv("MT_ENV_SH"); got != "shell" {
		t.Errorf("MT_ENV_SH = %q, want shell (shell wins)", got)
	}
}

// TestOpenRouterPreset pins the preset /key and the picker rely on:
// a full vendor/model default id and the OPENROUTER_API_KEY env var.
func TestOpenRouterPreset(t *testing.T) {
	p, ok := defaultProviders()["openrouter"]
	if !ok {
		t.Fatal("openrouter preset missing")
	}
	if p.DefaultModel != "anthropic/claude-sonnet-4.5" {
		t.Errorf("DefaultModel = %q, want anthropic/claude-sonnet-4.5", p.DefaultModel)
	}
	if p.APIKey.Env != "OPENROUTER_API_KEY" {
		t.Errorf("APIKey.Env = %q, want OPENROUTER_API_KEY", p.APIKey.Env)
	}
	if p.Dialect != "openai" {
		t.Errorf("Dialect = %q, want openai", p.Dialect)
	}
}
