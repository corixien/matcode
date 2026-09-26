package plugin

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// answerScript is the reference plugin: a newline-delimited JSON-RPC loop.
// It is also the shape `mtc plugin new` scaffolds, so the tests document
// the protocol every plugin must speak (§8).
const answerScript = `import json, sys
for line in sys.stdin:
    line = line.strip()
    if not line:
        continue
    try:
        req = json.loads(line)
    except Exception:
        continue
    if "method" not in req:
        continue
    mid, method, params = req.get("id"), req["method"], req.get("params") or {}
    result = None
    if method == "hook.session.context":
        result = {"lines": "[plugin] always follow the house rules"}
    elif method == "hook.tool.execute.before" and params.get("tool") == "bash":
        result = {"input": {"command": "echo rewritten"}}
    elif method == "hook.tool.execute.after":
        result = {"output": params.get("output", "") + " [+plugin]"}
    elif method == "hook.permission.evaluate":
        result = {"decision": "deny"}
    elif method == "hook.shell.create.before":
        result = {"deny": True, "reason": "plugins veto shell"}
    elif method == "hook.session.compaction":
        result = {"checkpoint": "## Objective\nfrom the plugin"}
    else:
        sys.stdout.write(json.dumps({"jsonrpc": "2.0", "id": mid,
                                     "error": {"code": -32601, "message": "unhandled"}}) + "\n")
        sys.stdout.flush()
        continue
    sys.stdout.write(json.dumps({"jsonrpc": "2.0", "id": mid, "result": result}) + "\n")
    sys.stdout.flush()
`

// writePlugin scaffolds plugins/<name>/{plugin.toml,main.py} in a temp tree.
func writePlugin(t *testing.T, root, name, tomlBody, script string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.toml"), []byte(tomlBody), 0o644); err != nil {
		t.Fatal(err)
	}
	if script != "" {
		if err := os.WriteFile(filepath.Join(dir, "main.py"), []byte(script), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func needPython(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
}

const demoToml = `id = "demo"
version = "0.1.0"
permissions = ["hooks.session", "hooks.tool", "hooks.permission", "hooks.shell"]
cmd = ["python3", "main.py"]
timeout = 5
`

func TestLoadPrecedenceAndDefaults(t *testing.T) {
	glob := t.TempDir()
	proj := t.TempDir()
	writePlugin(t, glob, "alpha", `id = "alpha"
cmd = ["python3", "main.py"]
`, "")
	// Same id in the project tree: the project copy wins, enabled by default.
	writePlugin(t, proj, "alpha", `id = "alpha"
version = "2.0"
cmd = ["python3", "main.py"]
`, "")
	// A disabled plugin with an inferred entry and no id.
	writePlugin(t, proj, "off", `enabled = false
cmd = ["bash", "run.sh"]
`, "")

	loaded := Load(glob, proj)
	if len(loaded) != 2 {
		t.Fatalf("loaded %d plugins, want 2 (alpha, off)", len(loaded))
	}
	if loaded[0].ID != "alpha" || loaded[1].ID != "off" {
		t.Fatalf("ids = %q,%q", loaded[0].ID, loaded[1].ID)
	}
	if loaded[0].Version != "2.0" {
		t.Errorf("project manifest did not win: version %q", loaded[0].Version)
	}
	if !loaded[0].Enabled {
		t.Error("alpha must default to enabled")
	}
	if loaded[1].Enabled {
		t.Error("off must stay disabled")
	}
	if loaded[0].Timeout != 5*time.Second {
		t.Errorf("timeout = %s, want 5s", loaded[0].Timeout)
	}
}

func TestValidateReportsProblems(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "good", demoToml, answerScript)
	writePlugin(t, root, "noid", `cmd = ["bash", "run.sh"]`, "")
	writePlugin(t, root, "noentry", `id = "noentry"
cmd = ["bash", "missing.sh"]
`, "")
	writePlugin(t, root, "badperm", `id = "badperm"
permissions = ["hooks.session", "root.everything"]
cmd = ["bash", "run.sh"]
`, "")

	count, problems := Validate(root)
	if count != 4 {
		t.Fatalf("count = %d, want 4", count)
	}
	want := []string{"no id", "missing.sh", "root.everything"}
	for _, w := range want {
		found := false
		for _, p := range problems {
			if strings.Contains(p, w) {
				found = true
			}
		}
		if !found {
			t.Errorf("no problem mentions %q: %v", w, problems)
		}
	}
	for _, p := range problems {
		if strings.Contains(p, "good") {
			t.Errorf("valid plugin reported: %s", p)
		}
	}
}

func TestAllowsMatchesPermissionGrants(t *testing.T) {
	p := Plugin{Permissions: []string{"hooks.*", "tools.register"}}
	for _, perm := range []string{PermSession, PermTool, PermPermission, PermShell} {
		if !p.Allows(perm) {
			t.Errorf("hooks.* must grant %s", perm)
		}
	}
	if !p.Allows(PermToolsRegister) {
		t.Error("tools.register must be granted verbatim")
	}
	// The §8 surface is 5 hooks over 6 method names (before/after are two).
	if got := p.Hooks(); len(got) != 6 {
		t.Errorf("Hooks() = %v, want the 6 hook methods", got)
	}
	limited := Plugin{Permissions: []string{PermSession}}
	if got := limited.Hooks(); len(got) != 2 {
		t.Errorf("hooks.session must grant exactly the two session hooks, got %v", got)
	}
	if limited.Allows(PermShell) {
		t.Error("hooks.session must not grant hooks.shell")
	}
}

func TestHookRoundTrip(t *testing.T) {
	needPython(t)
	root := t.TempDir()
	writePlugin(t, root, "demo", demoToml, answerScript)

	h := Open([]string{root}, "")
	defer h.Close()
	ctx := context.Background()

	if st := h.Status(); len(st) != 1 || st[0].State != "running" {
		t.Fatalf("status = %+v, want one running plugin", st)
	}

	lines, ok := h.SessionContext(ctx, "ses_1")
	if !ok || !strings.Contains(lines, "house rules") {
		t.Errorf("SessionContext = %q/%v", lines, ok)
	}

	in, ok := h.ToolBefore(ctx, "bash", json.RawMessage(`{"command":"echo hi"}`))
	if !ok || !strings.Contains(string(in), "rewritten") {
		t.Errorf("ToolBefore = %s/%v", in, ok)
	}
	// A tool the plugin ignores keeps its input.
	if _, ok := h.ToolBefore(ctx, "read", json.RawMessage(`{"path":"x"}`)); ok {
		t.Error("ToolBefore must be unhandled for read")
	}

	out, ok := h.ToolAfter(ctx, "read", json.RawMessage(`{"path":"x"}`), "body")
	if !ok || out != "body [+plugin]" {
		t.Errorf("ToolAfter = %q/%v", out, ok)
	}

	if d, ok := h.PermissionEvaluate(ctx, "bash", json.RawMessage(`{"command":"ls"}`)); !ok || d != Deny {
		t.Errorf("PermissionEvaluate = %q/%v, want deny", d, ok)
	}

	sd := h.ShellBefore(ctx, "ls -la")
	if !sd.Handled || !sd.Deny || sd.Reason != "plugins veto shell" {
		t.Errorf("ShellBefore = %+v", sd)
	}

	cp, ok := h.SessionCompaction(ctx, "ses_1", []string{"a", "b"})
	if !ok || !strings.Contains(cp, "from the plugin") {
		t.Errorf("SessionCompaction = %q/%v", cp, ok)
	}
}

func TestPluginFailureDegrades(t *testing.T) {
	needPython(t)
	root := t.TempDir()

	// A plugin that dies on startup: every hook must come back untouched.
	writePlugin(t, root, "boom", `id = "boom"
permissions = ["hooks.session"]
cmd = ["python3", "main.py"]
timeout = 1
`, "import sys\nsys.exit(1)\n")
	// A plugin that never answers: the call must respect the timeout.
	writePlugin(t, root, "hang", `id = "hang"
permissions = ["hooks.session"]
cmd = ["python3", "main.py"]
timeout = 1
`, "import sys, time\nfor line in sys.stdin:\n    time.sleep(60)\n")

	h := Open([]string{root}, "")
	defer h.Close()
	ctx := context.Background()

	start := time.Now()
	lines, ok := h.SessionContext(ctx, "ses_1")
	if ok || lines != "" {
		t.Errorf("SessionContext = %q/%v, want untouched", lines, ok)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("hooks took %s; a dead or hanging plugin must degrade fast", d)
	}
	if st := h.Status(); len(st) != 2 {
		t.Fatalf("status = %+v, want both plugins", st)
	}
}

func TestPermissionGateRefusesUndeclaredHooks(t *testing.T) {
	needPython(t)
	root := t.TempDir()
	// The script answers every hook, but the manifest only grants session.
	writePlugin(t, root, "limited", `id = "limited"
permissions = ["hooks.session"]
cmd = ["python3", "main.py"]
`, answerScript)

	h := Open([]string{root}, "")
	defer h.Close()
	ctx := context.Background()

	if _, ok := h.SessionContext(ctx, "ses_1"); !ok {
		t.Error("session hook must be granted")
	}
	if d, ok := h.PermissionEvaluate(ctx, "bash", json.RawMessage(`{"command":"ls"}`)); ok {
		t.Errorf("permission hook must be refused, got %q", d)
	}
	if sd := h.ShellBefore(ctx, "ls"); sd.Handled {
		t.Error("shell hook must be refused")
	}
}

func TestReloadPicksUpManifestChanges(t *testing.T) {
	needPython(t)
	root := t.TempDir()
	writePlugin(t, root, "demo", demoToml, answerScript)

	h := Open([]string{root}, "")
	defer h.Close()
	ctx := context.Background()
	if _, ok := h.SessionContext(ctx, "ses_1"); !ok {
		t.Fatal("first hook must be handled")
	}

	// Disabling the plugin and reloading must drop every hook.
	if err := os.WriteFile(filepath.Join(root, "demo", "plugin.toml"),
		[]byte(`id = "demo"
enabled = false
permissions = ["hooks.session"]
cmd = ["python3", "main.py"]
`), 0o644); err != nil {
		t.Fatal(err)
	}
	h.Reload(root)
	if _, ok := h.SessionContext(ctx, "ses_1"); ok {
		t.Error("a disabled plugin must stop answering")
	}
	if st := h.Status(); len(st) != 1 || st[0].State != "disabled" {
		t.Errorf("status = %+v", st)
	}
}

func TestNilHostDisablesHooks(t *testing.T) {
	var h *Host
	ctx := context.Background()
	if _, ok := h.SessionContext(ctx, "s"); ok {
		t.Error("nil host must not handle session.context")
	}
	if in := json.RawMessage(`{}`); func() bool {
		_, ok := h.ToolBefore(ctx, "bash", in)
		return ok
	}() {
		t.Error("nil host must not handle tool.execute.before")
	}
	if sd := h.ShellBefore(ctx, "ls"); sd.Handled {
		t.Error("nil host must not handle shell.create.before")
	}
}
