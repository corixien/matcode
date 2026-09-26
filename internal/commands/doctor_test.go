package commands

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"matcode/internal/agents"
)

// TestDoctorHumanOutput proves `mtc doctor` prints every check and exits
// clean on a well-formed project tree (spec row 31).
func TestDoctorHumanOutput(t *testing.T) {
	withAPIProject(t, "model = \"mockt/t\"\n\n[providers.mockt]\nbase_url = \"http://127.0.0.1:1/v1\"\napi_key = { env = \"MOCK_KEY\" }\ndefault_model = \"t\"\n")
	t.Setenv("MOCK_KEY", "x")

	out := captureStdout(t, func() error { return Doctor(context.Background(), nil) })
	for _, want := range []string{"matcode doctor", "config file", "data dir", "sessions dir", "skills", "go version", "✓"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

// TestDoctorJSONOutput proves -json emits a parseable check array.
func TestDoctorJSONOutput(t *testing.T) {
	withAPIProject(t, "")
	t.Setenv("MOCK_KEY", "x")

	out := captureStdout(t, func() error {
		return Doctor(context.Background(), []string{"-json"})
	})
	var checks []struct {
		Name   string `json:"name"`
		Status string `json:"status"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal([]byte(out), &checks); err != nil {
		t.Fatalf("not JSON (%v):\n%s", err, out)
	}
	if len(checks) < 5 {
		t.Fatalf("only %d checks reported", len(checks))
	}
	seen := map[string]bool{}
	for _, c := range checks {
		seen[c.Name] = true
	}
	for _, want := range []string{"config file", "data dir", "sessions dir", "themes dir", "go version"} {
		if !seen[want] {
			t.Errorf("missing check %q", want)
		}
	}
}

// TestDoctorUnknownFlag proves flag errors surface to the caller.
func TestDoctorUnknownFlag(t *testing.T) {
	withAPIProject(t, "")
	if err := Doctor(context.Background(), []string{"-nope"}); err == nil {
		t.Error("expected an error for an unknown flag")
	}
}

// TestDoctorAgentsCheck proves the agents check (row 32) reports the roster
// and flags a default agent that cannot resolve.
func TestDoctorAgentsCheck(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // isolate the global agents dir
	dir := withAPIProject(t, "model = \"mockt/t\"\nagent = \"build\"\n")
	t.Setenv("MOCK_KEY", "x")
	if err := os.MkdirAll(filepath.Join(dir, ".mtc", "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".mtc", "agents", "review.md"),
		[]byte("---\nmodel: mockt/t\n---\nReview."), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = agents.Load() })

	out := captureStdout(t, func() error { return Doctor(context.Background(), []string{"-json"}) })
	var checks []struct {
		Name   string `json:"name"`
		Status string `json:"status"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal([]byte(out), &checks); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	byName := map[string]struct {
		Status string
		Detail string
	}{}
	for _, c := range checks {
		byName[c.Name] = struct {
			Status string
			Detail string
		}{c.Status, c.Detail}
	}
	a, ok := byName["agents"]
	if !ok {
		t.Fatalf("no agents check in %v", out)
	}
	// 5 builtin agents + the review.md file agent.
	if a.Status != "ok" || !strings.Contains(a.Detail, "6 selectable") {
		t.Errorf("agents check = %+v", a)
	}

	// A default agent that does not resolve must warn, not crash.
	dir2 := withAPIProject(t, "model = \"mockt/t\"\nagent = \"ghost\"\n")
	_ = dir2
	out = captureStdout(t, func() error { return Doctor(context.Background(), []string{"-json"}) })
	checks = nil
	if err := json.Unmarshal([]byte(out), &checks); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	for _, c := range checks {
		if c.Name == "agents" {
			if c.Status != "warn" || !strings.Contains(c.Detail, "ghost") {
				t.Errorf("unresolvable default agent: %+v", c)
			}
			return
		}
	}
	t.Error("no agents check on the second run")
}
