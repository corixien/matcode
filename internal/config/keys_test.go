package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fullCfg exercises every scalar key plus both tables in one file.
const fullCfg = `model = "groq/llama-3.3-70b"
theme = "solar"
context_limit = 64000
api_port = 9999
formatter = true
snapshots = false
auto_compact = false

[media]
auto_resize = false
max_base64_bytes = 1048576
max_file_bytes = 2097152

[ui]
show_tokens = false
editor = "code --wait"
`

// TestLoadKeysFromConfig proves every documented key parses out of
// config.toml and lands on the resolved Config.
func TestLoadKeysFromConfig(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	writeCfg(t, filepath.Join(home, ".config", "mtc"), fullCfg)
	cfg, err := loadWithHome(t, home, cwd)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model != "groq/llama-3.3-70b" {
		t.Errorf("Model = %q", cfg.Model)
	}
	if cfg.Theme != "solar" {
		t.Errorf("Theme = %q", cfg.Theme)
	}
	if cfg.ContextLimit != 64000 || !cfg.ContextLimitSet {
		t.Errorf("ContextLimit = %d set = %v", cfg.ContextLimit, cfg.ContextLimitSet)
	}
	if cfg.APIPort != 9999 {
		t.Errorf("APIPort = %d", cfg.APIPort)
	}
	if got := cfg.APIBaseURL(); got != "http://127.0.0.1:9999" {
		t.Errorf("APIBaseURL = %q", got)
	}
	if !cfg.Formatter || cfg.Snapshots || cfg.AutoCompact {
		t.Errorf("bools: formatter=%v snapshots=%v auto_compact=%v",
			cfg.Formatter, cfg.Snapshots, cfg.AutoCompact)
	}
	if cfg.Media.AutoResize || cfg.Media.MaxBase64Bytes != 1048576 || cfg.Media.MaxFileBytes != 2097152 {
		t.Errorf("Media = %+v", cfg.Media)
	}
	if cfg.UIShowTokens || cfg.UIEditor != "code --wait" {
		t.Errorf("UI: show_tokens=%v editor=%q", cfg.UIShowTokens, cfg.UIEditor)
	}
}

// TestLoadPartialTablesKeepDefaults proves a table sets only the keys it
// names: the rest keep the built-in defaults.
func TestLoadPartialTablesKeepDefaults(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	writeCfg(t, filepath.Join(home, ".config", "mtc"),
		"[media]\nmax_file_bytes = 1000\n\n[ui]\neditor = \"nano\"\n")
	cfg, err := loadWithHome(t, home, cwd)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Media.AutoResize || cfg.Media.MaxBase64Bytes != 5<<20 {
		t.Errorf("unset media keys lost defaults: %+v", cfg.Media)
	}
	if cfg.Media.MaxFileBytes != 1000 {
		t.Errorf("MaxFileBytes = %d", cfg.Media.MaxFileBytes)
	}
	if !cfg.UIShowTokens || cfg.UIEditor != "nano" {
		t.Errorf("UI: show_tokens=%v editor=%q", cfg.UIShowTokens, cfg.UIEditor)
	}
}

// TestLoadDefaultsWhenAbsent proves a fresh install resolves to the rice
// defaults (spec §3) with no config files at all.
func TestLoadDefaultsWhenAbsent(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	cfg, err := loadWithHome(t, home, cwd)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model != DefaultModel {
		t.Errorf("Model = %q, want rice default %q", cfg.Model, DefaultModel)
	}
	if cfg.ContextLimit != 128000 || cfg.ContextLimitSet {
		t.Errorf("ContextLimit = %d set = %v", cfg.ContextLimit, cfg.ContextLimitSet)
	}
	if cfg.APIPort != 8787 || cfg.APIBaseURL() != "http://127.0.0.1:8787" {
		t.Errorf("APIPort = %d base = %q", cfg.APIPort, cfg.APIBaseURL())
	}
	if cfg.Theme != "default" || cfg.Agent != "" || cfg.UIEditor != "" {
		t.Errorf("Theme = %q Agent = %q UIEditor = %q", cfg.Theme, cfg.Agent, cfg.UIEditor)
	}
	if !cfg.Snapshots || !cfg.AutoCompact || !cfg.UIShowTokens || cfg.Formatter {
		t.Errorf("bool defaults: snapshots=%v auto_compact=%v show_tokens=%v formatter=%v",
			cfg.Snapshots, cfg.AutoCompact, cfg.UIShowTokens, cfg.Formatter)
	}
	if cfg.Media.AutoResize != true || cfg.Media.MaxBase64Bytes != 5<<20 || cfg.Media.MaxFileBytes != 20<<20 {
		t.Errorf("Media defaults = %+v", cfg.Media)
	}
	if p, ok := cfg.Provider("anthropic"); !ok || p.Dialect != "anthropic" || p.BaseURL == "" {
		t.Errorf("built-in providers missing/odd: %+v ok=%v", p, ok)
	}
	if _, ok := cfg.Provider("nope"); ok {
		t.Error("unknown provider resolved")
	}
}

// TestLoadInvalidValues covers both rejection (decode errors) and
// written-around behavior (out-of-range numbers silently keep defaults).
func TestLoadInvalidValues(t *testing.T) {
	bad := []struct{ name, cfg string }{
		{"type mismatch", "context_limit = \"sixty-four\"\n"},
		{"syntax error", "model = \n"},
		{"wrong table type", "media = 5\n"},
	}
	for _, c := range bad {
		t.Run(c.name, func(t *testing.T) {
			home, cwd := t.TempDir(), t.TempDir()
			writeCfg(t, filepath.Join(home, ".config", "mtc"), c.cfg)
			if _, err := loadWithHome(t, home, cwd); err == nil {
				t.Fatal("expected a decode error")
			}
		})
	}

	t.Run("out of range keeps defaults", func(t *testing.T) {
		home, cwd := t.TempDir(), t.TempDir()
		writeCfg(t, filepath.Join(home, ".config", "mtc"),
			"context_limit = -5\napi_port = -1\nmedia = { max_base64_bytes = -1 }\n")
		cfg, err := loadWithHome(t, home, cwd)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.ContextLimit != 128000 || cfg.ContextLimitSet {
			t.Errorf("ContextLimit = %d set = %v, want default unset", cfg.ContextLimit, cfg.ContextLimitSet)
		}
		if cfg.APIPort != 8787 {
			t.Errorf("APIPort = %d, want default", cfg.APIPort)
		}
		if cfg.Media.MaxBase64Bytes != 5<<20 {
			t.Errorf("MaxBase64Bytes = %d, want default", cfg.Media.MaxBase64Bytes)
		}
	})
}

// TestProjectOverridesGlobalKeys proves the project layer wins per key and
// merges into the global layer, not replaces it.
func TestProjectOverridesGlobalKeys(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	writeCfg(t, filepath.Join(home, ".config", "mtc"),
		"model = \"global/m\"\ntheme = \"global\"\ncontext_limit = 1000\n\n[media]\nmax_file_bytes = 777\n")
	writeCfg(t, filepath.Join(cwd, ".mtc"), "theme = \"project\"\n\n[media]\nauto_resize = false\n")
	cfg, err := loadWithHome(t, home, cwd)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model != "global/m" || cfg.Theme != "project" || cfg.ContextLimit != 1000 {
		t.Errorf("merge: model=%q theme=%q context=%d", cfg.Model, cfg.Theme, cfg.ContextLimit)
	}
	if cfg.Media.MaxFileBytes != 777 || cfg.Media.AutoResize {
		t.Errorf("media merge = %+v", cfg.Media)
	}
	if cfg.DataDir() != filepath.Join(cwd, ".mtc") {
		t.Errorf("DataDir = %q", cfg.DataDir())
	}
}

// TestDataTreeHelpers pins the exported path helpers: project first for
// Find/DataDir, global first for the source-root lists (precedence order).
func TestDataTreeHelpers(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	global := filepath.Join(home, ".config", "mtc")
	writeCfg(t, global, "")
	if err := os.MkdirAll(filepath.Join(cwd, ".mtc"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadWithHome(t, home, cwd)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DataDir() != filepath.Join(cwd, ".mtc") {
		t.Fatalf("DataDir = %q", cfg.DataDir())
	}
	if got := cfg.SessionsDir(); got != filepath.Join(cwd, ".mtc", "sessions") {
		t.Errorf("SessionsDir = %q", got)
	}
	assertDirs(t, "ToolsDirs", cfg.ToolsDirs(), []string{
		filepath.Join(global, "tools"),
		filepath.Join(cwd, ".mtc", "tools"),
	})
	assertDirs(t, "PluginsDirs", cfg.PluginsDirs(), []string{
		filepath.Join(global, "plugins"),
		filepath.Join(cwd, ".mtc", "plugins"),
	})

	// Find: project file wins over the global one; missing stays missing.
	if err := os.WriteFile(filepath.Join(global, "findme.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, ok := cfg.Find("findme.txt"); !ok || got != filepath.Join(global, "findme.txt") {
		t.Fatalf("Find global = %q ok=%v", got, ok)
	}
	if err := os.WriteFile(filepath.Join(cwd, ".mtc", "findme.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, ok := cfg.Find("findme.txt"); !ok || got != filepath.Join(cwd, ".mtc", "findme.txt") {
		t.Fatalf("Find project = %q ok=%v", got, ok)
	}
	if got, ok := cfg.Find("nope.txt"); ok || got != "" {
		t.Fatalf("Find missing = %q ok=%v", got, ok)
	}

	// Without a project tree everything collapses onto the global dir.
	bare, err := loadWithHome(t, home, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if bare.DataDir() != global {
		t.Errorf("DataDir = %q, want %q", bare.DataDir(), global)
	}
	assertDirs(t, "ToolsDirs", bare.ToolsDirs(), []string{filepath.Join(global, "tools")})
	assertDirs(t, "PluginsDirs", bare.PluginsDirs(), []string{filepath.Join(global, "plugins")})
}

// TestResolveKey proves the key is read from the named env var only — the
// file never carries the secret itself.
func TestResolveKey(t *testing.T) {
	t.Setenv("MTC_TEST_DUMMY_KEY", "dummy-value")
	p := Provider{APIKey: EnvRef{Env: "MTC_TEST_DUMMY_KEY"}}
	got, err := p.ResolveKey()
	if err != nil || got != "dummy-value" {
		t.Fatalf("ResolveKey = %q err=%v", got, err)
	}
	if got, err := (Provider{}).ResolveKey(); err != nil || got != "" {
		t.Fatalf("no env ref: %q err=%v", got, err)
	}
	_, err = (Provider{APIKey: EnvRef{Env: "MTC_TEST_UNSET_KEY_XYZ"}}).ResolveKey()
	if err == nil || !strings.Contains(err.Error(), "MTC_TEST_UNSET_KEY_XYZ") {
		t.Fatalf("unset env error = %v", err)
	}
}

func assertDirs(t *testing.T, name string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s = %v, want %v", name, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%s[%d] = %q, want %q", name, i, got[i], want[i])
		}
	}
}
