package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"matcode/internal/config"
)

// fixture writes one agent .md and returns a Config whose global tree is a
// fresh temp dir with model + provider configured.
func fixture(t *testing.T, agentFile string) *config.Config {
	t.Helper()
	home, cwd := t.TempDir(), t.TempDir()
	global := filepath.Join(home, ".config", "mtc")
	if err := os.MkdirAll(global, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgText := "model = \"mockt/t\"\nagent = \"build\"\n\n[providers.mockt]\n" +
		"base_url = \"http://127.0.0.1:9/v1\"\n" +
		"api_key = { env = \"MT_TEST_KEY\" }\n" +
		"default_model = \"t\"\n"
	if err := os.WriteFile(filepath.Join(global, "config.toml"), []byte(cfgText), 0o644); err != nil {
		t.Fatal(err)
	}
	if agentFile != "" {
		if err := os.MkdirAll(filepath.Join(global, "agents"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(global, "agents", "build.md"), []byte(agentFile), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("MT_TEST_KEY", "x")
	cfg, err := config.Load(cwd)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// TestAgentFrontmatterModelWins proves the resolution chain
// flag > agent frontmatter model > config default (spec row 32).
func TestAgentFrontmatterModelWins(t *testing.T) {
	cfg := fixture(t, "---\nmodel: mockt/overlay\n---\nOverlay prompt.")

	built, err := New(context.Background(), Options{Cwd: t.TempDir(), Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	// providers.For resolves to the provider's short model name.
	if built.Model != "overlay" {
		t.Errorf("Model = %q, want overlay", built.Model)
	}
	if built.Engine.Model != "overlay" {
		t.Errorf("Engine.Model = %q", built.Engine.Model)
	}

	// An explicit flag beats the agent.
	b2, err := New(context.Background(), Options{Cwd: t.TempDir(), Config: cfg, Model: "mockt/flag"})
	if err != nil {
		t.Fatal(err)
	}
	defer b2.Close()
	if b2.Model != "flag" {
		t.Errorf("flagged Model = %q", b2.Model)
	}
}

// TestNoOverlayFallsBackToConfigModel is the control case.
func TestNoOverlayFallsBackToConfigModel(t *testing.T) {
	cfg := fixture(t, "")
	built, err := New(context.Background(), Options{Cwd: t.TempDir(), Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	if built.Model != "t" {
		t.Errorf("Model = %q, want t (config default)", built.Model)
	}
	if built.Engine.MaxRounds != 0 {
		t.Errorf("MaxRounds = %d, want 0 (default)", built.Engine.MaxRounds)
	}
}

// TestStepsReachEngine proves frontmatter `steps` caps tool rounds.
func TestStepsReachEngine(t *testing.T) {
	cfg := fixture(t, "---\nsteps: 6\n---\nstep prompt")
	built, err := New(context.Background(), Options{Cwd: t.TempDir(), Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	if built.Engine.MaxRounds != 6 {
		t.Errorf("MaxRounds = %d, want 6", built.Engine.MaxRounds)
	}
}

// TestRequestOverlayReachesEngine proves `request.*` lands on the engine so
// the provider merges it into every payload.
func TestRequestOverlayReachesEngine(t *testing.T) {
	cfg := fixture(t, `---
request:
  headers:
    X-Mtc-Agent: build
  body:
    temperature: 0.1
---
p`)
	built, err := New(context.Background(), Options{Cwd: t.TempDir(), Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	if built.Engine.RequestHeaders["X-Mtc-Agent"] != "build" {
		t.Errorf("headers = %v", built.Engine.RequestHeaders)
	}
	if v, ok := built.Engine.RequestBody["temperature"].(float64); !ok || v != 0.1 {
		t.Errorf("body = %#v", built.Engine.RequestBody)
	}
}

// TestOverlaySystemAndPermissions proves the overlay replaces the system
// prompt while the builtin's permission behavior stays unless overridden.
func TestOverlaySystemAndPermissions(t *testing.T) {
	cfg := fixture(t, "---\npermissions: honors\n---\nOverridden system.")
	built, err := New(context.Background(), Options{Cwd: t.TempDir(), Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	// §11 payload order: agent body first, then env line, then AGENTS.md
	// (instructions) when the agent asks for them — never prepended over
	// the body.
	if !strings.HasPrefix(built.Engine.System, "Overridden system.\n\nWorking directory: ") {
		t.Errorf("system prefix = %q", built.Engine.System)
	}
	if built.Agent.UseInstructions && !strings.Contains(built.Engine.System, "You are matcode, a coding agent operating in a terminal.") {
		t.Errorf("instructions missing from system = %q", built.Engine.System)
	}
	if built.Agent.BypassPermissions {
		t.Error("permissions: honors must disable the build bypass")
	}
	// Permissions are loaded for a non-bypassing agent.
	if built.Engine.Permissions == nil {
		t.Error("permissions set should be loaded")
	}
}

// TestNewBootsWithoutAPIKey proves the missing-credential path: app.New
// still assembles the build (so the TUI can open and /key can fix it),
// carrying the reason in ProviderErr and leaving Engine.Provider nil for
// the engine guard to report at turn time.
func TestNewBootsWithoutAPIKey(t *testing.T) {
	cfg := fixture(t, "")
	t.Setenv("MT_TEST_KEY", "")

	built, err := New(context.Background(), Options{Cwd: t.TempDir(), Config: cfg})
	if err != nil {
		t.Fatalf("New without API key: %v", err)
	}
	defer built.Close()
	if built.Engine.Provider != nil {
		t.Error("Engine.Provider should be nil without a key")
	}
	if built.ProviderErr == nil {
		t.Error("ProviderErr should carry the cause")
	}
	if built.Model == "" {
		t.Error("model ref should still be resolved")
	}
}
