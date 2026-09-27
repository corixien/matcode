package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"matcode/internal/config"
	"matcode/internal/tui/theme"
)

// keyFixture builds an App whose project config declares one provider with
// its own credential env var (unset), like models_test.
func keyFixture(t *testing.T) *App {
	t.Helper()
	cwd := t.TempDir()
	mtc := filepath.Join(cwd, ".mtc")
	if err := os.MkdirAll(mtc, 0o755); err != nil {
		t.Fatal(err)
	}
	toml := `
[providers.mock]
dialect = "openai"
base_url = "http://127.0.0.1:9"
api_key = { env = "MTC_TEST_SAVE_KEY" }
models = ["t1"]
`
	if err := os.WriteFile(filepath.Join(mtc, "config.toml"), []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MTC_TEST_SAVE_KEY", "")
	cfg, err := config.Load(cwd)
	if err != nil {
		t.Fatal(err)
	}
	return &App{cfg: cfg, cwd: cwd, theme: theme.Default}
}

func TestParseKeyInput(t *testing.T) {
	cases := []struct {
		in, fb, name, key string
		wantErr           bool
	}{
		{in: "sk-1", fb: "openrouter", name: "openrouter", key: "sk-1"},
		{in: "openrouter sk-2", fb: "anthropic", name: "openrouter", key: "sk-2"},
		{in: "", fb: "openrouter", wantErr: true},
		{in: "sk-3", fb: "", wantErr: true},
		{in: "a b c", fb: "openrouter", wantErr: true},
	}
	for _, c := range cases {
		name, key, err := parseKeyInput(c.in, c.fb)
		if c.wantErr {
			if err == nil {
				t.Errorf("%q: want error", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if name != c.name || key != c.key {
			t.Errorf("%q → (%q,%q), want (%q,%q)", c.in, name, key, c.name, c.key)
		}
	}
}

// TestBuiltinSlashHasProvider: /provider is discoverable in the slash
// list and the old /key id still resolves as its alias.
func TestBuiltinSlashHasProvider(t *testing.T) {
	var found *SlashCommand
	for _, c := range builtinSlashCommands() {
		if c.ID == "provider" {
			cc := c
			found = &cc
		}
	}
	if found == nil {
		t.Fatal("builtin slash list has no provider command")
	}
	alias := false
	for _, al := range found.Aliases {
		if al == "key" {
			alias = true
		}
	}
	if !alias {
		t.Error("provider command should keep the key alias")
	}
	if !builtinSlashID("key") {
		t.Error("builtinSlashID(key) should resolve via the alias")
	}
}

// TestSaveProviderKeyPersists: saveProviderKey writes the data dir .env
// (0600) and exports the var; an unknown provider reports instead.
func TestSaveProviderKeyPersists(t *testing.T) {
	a := keyFixture(t)
	a.saveProviderKey("mock", "sk-test-123")

	path := filepath.Join(a.cfg.DataDir(), ".env")
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "MTC_TEST_SAVE_KEY=sk-test-123") {
		t.Errorf(".env = %q, want the assignment", string(got))
	}
	if !strings.Contains(a.status, "saved") {
		t.Errorf("status = %q, want saved", a.status)
	}
	if env := os.Getenv("MTC_TEST_SAVE_KEY"); env != "sk-test-123" {
		t.Errorf("env = %q, want sk-test-123", env)
	}
	// Unknown provider reports instead of panicking.
	a.saveProviderKey("nope", "sk-x")
	if !strings.Contains(a.status, "unknown provider") {
		t.Errorf("status = %q, want unknown provider", a.status)
	}
}

// TestOpenProviderMenu: bare /provider opens the searchable provider
// menu; with arguments it starts key verification without a menu.
func TestOpenProviderMenu(t *testing.T) {
	a := keyFixture(t)
	if _, _ = a.openProvider(""); a.overlay == nil || a.overlay.kind != "provider" {
		t.Fatalf("overlay = %+v, want kind provider", a.overlay)
	}
	found := false
	for _, it := range a.overlay.items {
		if strings.HasPrefix(it, "mock ") {
			found = true
		}
	}
	if !found {
		t.Errorf("provider menu lacks mock: %v", a.overlay.items)
	}
	// With arguments it verifies directly and leaves no overlay behind.
	a.overlay = nil
	if _, _ = a.openProvider("mock sk-direct"); a.overlay != nil {
		t.Error("args should verify without opening the menu")
	}
	if !strings.Contains(a.status, "checking") {
		t.Errorf("status = %q, want checking", a.status)
	}
}

// TestOpenProviderKey: picking a menu row switches to the masked input
// bound to that provider; unknown rows report instead.
func TestOpenProviderKey(t *testing.T) {
	a := keyFixture(t)
	row := ""
	for _, it := range a.providerItems() {
		if strings.HasPrefix(it, "mock ") {
			row = it
		}
	}
	if row == "" {
		t.Fatal("providerItems lacks mock")
	}
	a.openProviderKey(row)
	if a.overlay == nil || a.overlay.kind != "key" {
		t.Fatalf("overlay = %+v, want kind key", a.overlay)
	}
	if a.overlay.keyProvider != "mock" {
		t.Errorf("keyProvider = %q, want mock", a.overlay.keyProvider)
	}
	a.openProviderKey("nope ENV X")
	if !strings.Contains(a.status, "unknown provider") {
		t.Errorf("status = %q, want unknown provider", a.status)
	}
}

// TestOpenRouterInPicker: once the key exists (via /provider or .env),
// the openrouter preset becomes a model-pickable choice.
func TestOpenRouterInPicker(t *testing.T) {
	a := keyFixture(t)
	t.Setenv("OPENROUTER_API_KEY", "")
	hasOpenRouter := func() bool {
		for _, c := range a.modelChoices() {
			if strings.HasPrefix(c, "openrouter/") {
				return true
			}
		}
		return false
	}
	if hasOpenRouter() {
		t.Fatal("openrouter offered without a key")
	}
	a.saveProviderKey("openrouter", "sk-or-test")
	if !hasOpenRouter() {
		t.Fatalf("openrouter missing from choices: %v", a.modelChoices())
	}
}

// TestKeyOverlayMasks: the typed key is never rendered back, and the
// bound provider is named in the mask line.
func TestKeyOverlayMasks(t *testing.T) {
	o := newOverlay("key", "api key for mock", nil, theme.Default)
	o.keyProvider = "mock"
	o.query = "sk-secret-xyz"
	line := o.queryLine()
	if strings.Contains(line, "sk-secret-xyz") {
		t.Errorf("key leaked into the view: %q", line)
	}
	if !strings.Contains(line, "•") {
		t.Errorf("no mask bullets: %q", line)
	}
	if !strings.Contains(line, "mock") {
		t.Errorf("provider name missing from mask line: %q", line)
	}
}
