package agents

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, dir, name, text string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// loadDirs is the shared setup: every test loads explicit dirs so the
// package state never leaks in from a developer's real config tree.
func loadDirs(t *testing.T, dirs ...string) {
	t.Helper()
	if err := Load(dirs...); err != nil {
		t.Fatalf("Load: %v", err)
	}
	t.Cleanup(func() {
		overlays = map[string]Agent{}
		ovOrder = nil
		ovBaseIDs = map[string]bool{}
		ovLoaded = false
	})
}

// First-class Anthropic thinking keys (§4) fold into request.body.thinking.
func TestOverlayThinkingBudgetKeys(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "plan.md", `---
thinking: enabled
budget_tokens: 100000
---
Plan prompt.`)
	loadDirs(t, dir)

	a, err := Get("plan")
	if err != nil {
		t.Fatal(err)
	}
	th, ok := a.ReqBody["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("thinking = %#v", a.ReqBody["thinking"])
	}
	if th["type"] != "enabled" {
		t.Errorf("type = %#v", th["type"])
	}
	if n, ok := th["budget_tokens"].(int); !ok || n != 100000 {
		t.Errorf("budget_tokens = %#v", th["budget_tokens"])
	}
}

func TestOverlayOntoBuiltinKeepsUnstatedFields(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "build.md", `---
model: mockt/t
tools: read, write
steps: 5
---
Custom build prompt.`)
	loadDirs(t, dir)

	a, err := Get("build")
	if err != nil {
		t.Fatal(err)
	}
	if a.System != "Custom build prompt." {
		t.Errorf("system = %q", a.System)
	}
	if a.Model != "mockt/t" {
		t.Errorf("model = %q", a.Model)
	}
	if len(a.ToolIDs) != 2 || a.ToolIDs[0] != "read" || a.ToolIDs[1] != "write" {
		t.Errorf("tools = %v", a.ToolIDs)
	}
	if a.Steps != 5 {
		t.Errorf("steps = %d", a.Steps)
	}
	// Untouched by the file:
	if !a.BypassPermissions {
		t.Error("BypassPermissions should keep the builtin true")
	}
	if !a.UseInstructions {
		t.Error("UseInstructions should keep the builtin true")
	}
}

func TestProjectOverridesGlobalFileByFile(t *testing.T) {
	g, p := t.TempDir(), t.TempDir()
	writeFile(t, g, "build.md", "---\nmodel: global/m\n---\nglobal prompt")
	writeFile(t, g, "plan.md", "---\nmodel: global/p\n---\nglobal plan")
	writeFile(t, p, "build.md", "---\nmodel: project/m\n---\nproject prompt")
	loadDirs(t, g, p)

	build, err := Get("build")
	if err != nil {
		t.Fatal(err)
	}
	if build.Model != "project/m" || build.System != "project prompt" {
		t.Errorf("build = %+v", build)
	}
	// plan.md exists only globally: the project must not shadow it away.
	plan, err := Get("plan")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Model != "global/p" {
		t.Errorf("plan model = %q", plan.Model)
	}
}

func TestNewAgentFileAddsSelectableAgent(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "reviewer.md", `---
tools: read, grep
permissions: honors
color: "#ff8800"
description: reviews diffs
---
Review the change.`)
	loadDirs(t, dir)

	a, err := Get("reviewer")
	if err != nil {
		t.Fatal(err)
	}
	if a.System != "Review the change." {
		t.Errorf("system = %q", a.System)
	}
	if a.BypassPermissions {
		t.Error("permissions: honors must not bypass")
	}
	if a.Color != "#ff8800" || a.Description != "reviews diffs" {
		t.Errorf("color=%q description=%q", a.Color, a.Description)
	}
	if !a.UseInstructions {
		t.Error("new agents should use AGENTS.md instructions by default")
	}
	ids := IDs()
	found := false
	for _, id := range ids {
		if id == "reviewer" {
			found = true
		}
	}
	if !found {
		t.Errorf("IDs() = %v, want reviewer", ids)
	}
	// Builtin order still leads.
	if ids[0] != "build" || ids[1] != "plan" {
		t.Errorf("roster prefix = %v", ids[:2])
	}
}

func TestDisabledAndHiddenSemantics(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "plan.md", "---\ndisabled: true\n---")
	writeFile(t, dir, "summary.md", "---\nhidden: true\n---")
	loadDirs(t, dir)

	if _, err := Get("plan"); err == nil {
		t.Error("Get(plan) should fail: it is disabled")
	}
	if _, err := Get("summary"); err != nil {
		t.Errorf("Get(summary) must still work when hidden: %v", err)
	}
	for _, id := range IDs() {
		if id == "plan" || id == "summary" {
			t.Errorf("IDs() must exclude disabled/hidden, got %v", IDs())
		}
	}
}

func TestToolsStarAndNone(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "all.md", "---\ntools: *\n---")
	writeFile(t, dir, "none.md", "---\ntools: none\n---")
	loadDirs(t, dir)

	all, _ := Get("all")
	if len(all.ToolIDs) != 1 || all.ToolIDs[0] != "*" {
		t.Errorf("tools=* → %v", all.ToolIDs)
	}
	none, _ := Get("none")
	if len(none.ToolIDs) != 0 {
		t.Errorf("tools=none → %v", none.ToolIDs)
	}
	// A no-tools agent advertises nothing but still exists.
	if _, err := Get("none"); err != nil {
		t.Fatal(err)
	}
}

func TestPermissionsValues(t *testing.T) {
	cases := map[string]bool{
		"bypass": true, "allow-all": true, "all": true, "true": true, "YES": true,
		"honors": false, "default": false, "enforce": false, "false": false,
		"deny": false, "ask": false, "garbage": false,
	}
	for in, want := range cases {
		if got := permsBypass(in); got != want {
			t.Errorf("permsBypass(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestStepsValidAndInvalid(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "build.md", "---\nsteps: 7\n---")
	writeFile(t, dir, "plan.md", "---\nsteps: lots\n---")
	loadDirs(t, dir)

	b, _ := Get("build")
	if b.Steps != 7 {
		t.Errorf("steps = %d", b.Steps)
	}
	p, err := Get("plan")
	if err != nil {
		t.Fatal(err)
	}
	if p.Steps != 0 {
		t.Errorf("unparsable steps must be ignored, got %d", p.Steps)
	}
}

func TestRequestOverlayParsing(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "build.md", `---
request:
  headers:
    X-Trace: on
  body:
    temperature: 0.2
    stream: true
    label: "hi"
---
x`)
	loadDirs(t, dir)

	a, err := Get("build")
	if err != nil {
		t.Fatal(err)
	}
	if a.ReqHeaders["X-Trace"] != "on" {
		t.Errorf("headers = %v", a.ReqHeaders)
	}
	if v, ok := a.ReqBody["temperature"].(float64); !ok || v != 0.2 {
		t.Errorf("temperature = %#v", a.ReqBody["temperature"])
	}
	if v, ok := a.ReqBody["stream"].(bool); !ok || !v {
		t.Errorf("stream = %#v", a.ReqBody["stream"])
	}
	if v, ok := a.ReqBody["label"].(string); !ok || v != "hi" {
		t.Errorf("label = %#v", a.ReqBody["label"])
	}
}

func TestMissingDirIsNotAnError(t *testing.T) {
	if err := Load(filepath.Join(t.TempDir(), "nope")); err != nil {
		t.Fatalf("missing dir must be skipped, got %v", err)
	}
	if len(IDs()) == 0 {
		t.Fatal("IDs() must stay populated from the roster")
	}
}

func TestModelForFallsBack(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "build.md", "---\nmodel: mockt/t\n---")
	loadDirs(t, dir)
	if got := ModelFor("build", "cfg/m"); got != "mockt/t" {
		t.Errorf("ModelFor(build) = %q", got)
	}
	if got := ModelFor("plan", "cfg/m"); got != "cfg/m" {
		t.Errorf("ModelFor(plan) = %q", got)
	}
	if got := ModelFor("missing", "cfg/m"); got != "cfg/m" {
		t.Errorf("ModelFor(missing) = %q", got)
	}
}

func TestExploreIsReadOnlyBuiltin(t *testing.T) {
	a, err := Get("explore")
	if err != nil {
		t.Fatal(err)
	}
	if a.BypassPermissions {
		t.Error("explore must honor permissions.toml")
	}
	for _, id := range a.ToolIDs {
		switch id {
		case "write", "edit", "patch", "bash", "todowrite", "question":
			t.Errorf("explore must not carry %q", id)
		}
	}
	if len(a.ToolIDs) != 5 {
		t.Errorf("tools = %v", a.ToolIDs)
	}
}
