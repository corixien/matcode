package commands

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"matcode/internal/agents"
)

func TestDebugUnknown(t *testing.T) {
	for _, args := range [][]string{
		{"bogus"},
		{"agents", "extra"},
		{"config", "extra"},
		{"paths", "a", "b"},
		{"paths", "nope"},
		{"-badflag"},
	} {
		if err := Debug(args); err == nil {
			t.Errorf("Debug(%q) = nil, want error", args)
		}
	}
}

func TestDebugPaths(t *testing.T) {
	withAPIProject(t, "api_port = 8799\n")

	out := captureStdout(t, func() error { return Debug(nil) })
	if !strings.Contains(out, "NAME") {
		t.Errorf("table output missing header:\n%s", out)
	}
	for _, name := range []string{"home", "config", "project", "data", "sessions", "logs", "skills", "db", "bin", "tmp"} {
		if !fieldLine(out, name) {
			t.Errorf("missing selector %q in:\n%s", name, out)
		}
	}
	// project row exists (temp dir has .mtc)
	if fieldLine(out, "project") && strings.Contains(projectLine(out), " - ") {
		t.Error("project path should exist in a project tree")
	}

	// single selector prints only the path
	out = captureStdout(t, func() error { return Debug([]string{"paths", "config"}) })
	if strings.ContainsAny(out, "\t ") {
		t.Errorf("selector output should be a bare path, got %q", out)
	}
	if !strings.HasSuffix(strings.TrimSpace(out), "mtc") {
		t.Errorf("config path = %q", out)
	}

	// json form
	out = captureStdout(t, func() error { return Debug([]string{"-json", "paths", "tmp"}) })
	var got struct {
		Path   string `json:"path"`
		Exists bool   `json:"exists"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("bad json %q: %v", out, err)
	}
	if got.Path == "" || !got.Exists {
		t.Errorf("json = %+v", got)
	}
}

func TestDebugAgents(t *testing.T) {
	withAPIProject(t, "")
	out := captureStdout(t, func() error { return Debug([]string{"agents"}) })
	for _, id := range agents.IDs() {
		if !agentLine(out, id) {
			t.Errorf("missing agent %q in:\n%s", id, out)
		}
	}
	if !strings.Contains(out, "build") || !strings.Contains(out, "yes") {
		t.Errorf("build should be the default agent:\n%s", out)
	}

	out = captureStdout(t, func() error { return Debug([]string{"agents", "-json"}) })
	var rows []struct {
		ID    string   `json:"id"`
		Tools []string `json:"tools"`
	}
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("bad json %q: %v", out, err)
	}
	if len(rows) != len(agents.IDs()) {
		t.Errorf("json rows = %d, want %d", len(rows), len(agents.IDs()))
	}
	for _, r := range rows {
		if r.ID == "plan" && len(r.Tools) == 0 {
			t.Error("plan agent should list tools")
		}
	}
}

func TestDebugConfig(t *testing.T) {
	withAPIProject(t, "model = \"mockt/t\"\napi_port = 8765\nformatter = true\n")
	out := captureStdout(t, func() error { return Debug([]string{"config"}) })
	for _, want := range []string{"mockt/t", "8765", "formatter      yes", "agent          build", "providers"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// secrets never appear: only env var names
	if strings.Contains(out, "sk-") {
		t.Error("config dump leaked a key value")
	}

	out = captureStdout(t, func() error { return Debug([]string{"config", "-json"}) })
	var got struct {
		Model     string `json:"model"`
		APIPort   int    `json:"api_port"`
		Agent     string `json:"agent"`
		Providers []struct {
			Name   string `json:"name"`
			KeyEnv string `json:"key_env"`
		} `json:"providers"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("bad json %q: %v", out, err)
	}
	if got.Model != "mockt/t" || got.APIPort != 8765 || got.Agent != "build" {
		t.Errorf("json = %+v", got)
	}
	if len(got.Providers) == 0 {
		t.Error("no providers in json")
	}
}

// fieldLine reports whether any line's first whitespace-delimited field is
// exactly name (tabwriter pads columns with spaces).
func fieldLine(out, name string) bool {
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) > 0 && f[0] == name {
			return true
		}
	}
	return false
}

// projectLine returns the line whose first field is "project".
func projectLine(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if fieldLine(line, "project") {
			return line
		}
	}
	return ""
}

// agentLine reports whether the roster table has a row starting with id.
func agentLine(out, id string) bool { return fieldLine(out, id) }

// TestDebugAgentsSourceColumn proves `debug agents` reports where each agent
// came from (row 32: file overlays vs the compiled roster) and that a
// disabled agent leaves the listing entirely.
func TestDebugAgentsSourceColumn(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// The package registry is global: clear it when the temp dirs vanish.
	t.Cleanup(func() { _ = agents.Load() })
	dir := withAPIProject(t, "")
	agentsDir := filepath.Join(dir, ".mtc", "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentsDir, "build.md"),
		[]byte("---\nmodel: mockt/t\nsteps: 3\n---\nOverlay prompt."), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentsDir, "hidden-agent.md"),
		[]byte("---\nhidden: true\n---\nh"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentsDir, "gone.md"),
		[]byte("---\ndisabled: true\n---\ng"), 0o644); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() error { return Debug([]string{"agents", "-json"}) })
	var rows []struct {
		ID     string `json:"id"`
		Source string `json:"source"`
		Model  string `json:"model"`
		Steps  int    `json:"steps"`
	}
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("bad json %q: %v", out, err)
	}
	byID := map[string]struct {
		Source string
		Model  string
		Steps  int
	}{}
	for _, r := range rows {
		byID[r.ID] = struct {
			Source string
			Model  string
			Steps  int
		}{r.Source, r.Model, r.Steps}
	}
	if b, ok := byID["build"]; !ok || b.Source != "file" || b.Model != "mockt/t" || b.Steps != 3 {
		t.Errorf("build = %+v (want file override)", byID["build"])
	}
	if p, ok := byID["plan"]; !ok || p.Source != "builtin" {
		t.Errorf("plan = %+v (want builtin)", byID["plan"])
	}
	if _, ok := byID["gone"]; ok {
		t.Error("disabled agent must not appear")
	}
	if _, ok := byID["hidden-agent"]; ok {
		t.Error("hidden agent must not appear")
	}
}
