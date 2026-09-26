package tui

import (
	"os"
	"path/filepath"
	"testing"

	"matcode/internal/agents"
	"matcode/internal/config"
	"matcode/internal/store"
	"matcode/internal/tui/routes"
	"matcode/internal/tui/theme"
)

// TestOnReloadRefreshesTheme proves a config change on disk is picked up
// by the running app when the watcher fires (spec row 31). config.Load
// reads `<cwd>/.mtc/config.toml`, so the test tree mirrors that layout.
func TestOnReloadRefreshesTheme(t *testing.T) {
	cwd := t.TempDir()
	mtc := filepath.Join(cwd, ".mtc")
	if err := os.MkdirAll(filepath.Join(mtc, "themes"), 0755); err != nil {
		t.Fatal(err)
	}
	solar := `{"name":"solar","colors":{"primary":"#f38ba8"}}`
	if err := os.WriteFile(filepath.Join(mtc, "themes", "solar.json"), []byte(solar), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mtc, "config.toml"),
		[]byte("theme = \"default\"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(cwd)
	if err != nil {
		t.Fatal(err)
	}
	a := &App{cfg: cfg, opts: Options{Config: cfg}, cwd: cwd, theme: theme.Default}
	a.tabs = []*tab{{chat: routes.NewChat(theme.Default, store.Meta{})}}

	// The user edits config.toml; the watcher would send reloadMsg.
	if err := os.WriteFile(filepath.Join(mtc, "config.toml"),
		[]byte("theme = \"solar\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	a.onReload()

	if got := a.theme.Name; got != "solar" {
		t.Fatalf("theme after reload = %q, want solar", got)
	}
	if a.status != "config reloaded" {
		t.Fatalf("status = %q, want %q", a.status, "config reloaded")
	}
}

// TestOnReloadKeepsCurrentThemeWhenUnchanged proves a no-op reload does
// not disturb the theme.
func TestOnReloadKeepsCurrentThemeWhenUnchanged(t *testing.T) {
	cwd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwd, ".mtc"), 0755); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cwd)
	if err != nil {
		t.Fatal(err)
	}
	a := &App{cfg: cfg, opts: Options{Config: cfg}, cwd: cwd, theme: theme.Default}
	before := a.theme
	a.onReload()
	if a.theme != before {
		t.Fatal("theme changed by a no-op reload")
	}
	if a.status != "config reloaded" {
		t.Fatalf("status = %q", a.status)
	}
}

// TestMiniReloadNoteAndTheme proves `mtc mini` hot-reloads config too
// (spec row 31: all data trees, every TUI entry point).
func TestMiniReloadNoteAndTheme(t *testing.T) {
	cwd := t.TempDir()
	mtc := filepath.Join(cwd, ".mtc")
	if err := os.MkdirAll(filepath.Join(mtc, "themes"), 0755); err != nil {
		t.Fatal(err)
	}
	solar := `{"name":"solar","colors":{"primary":"#f38ba8"}}`
	if err := os.WriteFile(filepath.Join(mtc, "themes", "solar.json"), []byte(solar), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mtc, "config.toml"),
		[]byte("theme = \"solar\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cwd)
	if err != nil {
		t.Fatal(err)
	}
	mm := &miniApp{m: NewMini(theme.Default, "s1", "mockt/t", "build"), cwd: cwd, cfg: cfg}
	mm.reload()

	if got := mm.m.theme.Name; got != "solar" {
		t.Fatalf("mini theme after reload = %q, want solar", got)
	}
	lines := mm.m.Lines()
	if len(lines) == 0 || lines[len(lines)-1] != "· config reloaded" {
		t.Fatalf("mini lines = %q, want a config-reloaded note", lines)
	}
}

// TestOnReloadLoadsAgents proves the watcher-driven reload also re-reads the
// agent `.md` overlays (row 32), so a picker edit takes effect without a
// restart, and that hidden/disabled agents leave the picker.
func TestOnReloadLoadsAgents(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cwd := t.TempDir()
	mtc := filepath.Join(cwd, ".mtc")
	agentsDir := filepath.Join(mtc, "agents")
	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mtc, "config.toml"), []byte("theme = \"default\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentsDir, "reviewer.md"),
		[]byte("---\nmodel: mockt/t\ndescription: reviews\n---\nReview."), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentsDir, "summary.md"),
		[]byte("---\nhidden: true\n---\n"), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(cwd)
	if err != nil {
		t.Fatal(err)
	}
	a := &App{cfg: cfg, opts: Options{Config: cfg}, cwd: cwd, theme: theme.Default}
	a.tabs = []*tab{{chat: routes.NewChat(theme.Default, store.Meta{})}}
	t.Cleanup(func() { _ = agents.Load() })

	if _, err := a.onReload(); err != nil {
		t.Fatal(err)
	}
	if !agents.Loaded() {
		t.Fatal("onReload must load the agent overlays")
	}
	choices := a.agentChoices()
	seen := map[string]bool{}
	for _, c := range choices {
		seen[c] = true
	}
	if !seen["reviewer"] {
		t.Errorf("choices = %v, want reviewer", choices)
	}
	if seen["summary"] {
		t.Errorf("choices = %v, summary must be hidden", choices)
	}
	if seen[""] {
		t.Errorf("choices = %v, empty id leaked", choices)
	}
	if r, err := agents.Get("reviewer"); err != nil || r.Model != "mockt/t" {
		t.Errorf("reviewer = %+v, %v", r, err)
	}
}
