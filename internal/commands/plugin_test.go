package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPluginNewScaffolds proves `mtc plugin new` writes a manifest, an
// answering main.py and a README into the project plugins tree, and that a
// second run refuses to clobber without --force.
func TestPluginNewScaffolds(t *testing.T) {
	dir := withAPIProject(t, "")
	out := captureStdout(t, func() error { return Plugin([]string{"new", "demo"}) })

	pluginDir := filepath.Join(dir, ".mtc", "plugins", "demo")
	for _, f := range []string{"plugin.toml", "main.py", "README.md"} {
		b, err := os.ReadFile(filepath.Join(pluginDir, f))
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if len(b) == 0 {
			t.Errorf("%s is empty", f)
		}
	}
	manifest, _ := os.ReadFile(filepath.Join(pluginDir, "plugin.toml"))
	for _, want := range []string{`id = "demo"`, `cmd = [`, `permissions = [`} {
		if !strings.Contains(string(manifest), want) {
			t.Errorf("plugin.toml missing %q:\n%s", want, manifest)
		}
	}
	script, _ := os.ReadFile(filepath.Join(pluginDir, "main.py"))
	for _, want := range []string{"hook.session.context", "hook.permission.evaluate", "hook.shell.create.before", "jsonrpc"} {
		if !strings.Contains(string(script), want) {
			t.Errorf("main.py missing %q", want)
		}
	}
	if !strings.Contains(out, "created plugin") {
		t.Errorf("output should report the scaffold: %q", out)
	}

	// Existing files are kept unless --force.
	if err := Plugin([]string{"new", "demo"}); err == nil {
		t.Fatal("scaffolding over an existing plugin must fail")
	}
	if err := Plugin([]string{"new", "demo", "-force"}); err != nil {
		t.Fatalf("--force: %v", err)
	}
}

// TestPluginNewRejectsBadName proves a path-shaped name never escapes the
// plugins tree.
func TestPluginNewRejectsBadName(t *testing.T) {
	withAPIProject(t, "")
	for _, name := range []string{"../evil", "a/b", ".hidden"} {
		if err := Plugin([]string{"new", name}); err == nil {
			t.Errorf("name %q accepted, want an error", name)
		}
	}
}

// TestPluginListAndCheck proves `list` reports the scaffolded plugin and
// `check` passes on it, then fails once the manifest is broken.
func TestPluginListAndCheck(t *testing.T) {
	dir := withAPIProject(t, "")
	if err := Plugin([]string{"new", "demo"}); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() error { return Plugin([]string{"list"}) })
	for _, want := range []string{"ID", "demo", "0.1.0", "hooks.*", "hook.session.context"} {
		if !strings.Contains(out, want) {
			t.Errorf("list missing %q:\n%s", want, out)
		}
	}

	checkOut := captureStdout(t, func() error { return Plugin([]string{"check"}) })
	if !strings.Contains(checkOut, "no problems") {
		t.Errorf("check on a scaffolded plugin: %q", checkOut)
	}

	// Break the manifest: no cmd and no inferable entry file.
	pluginDir := filepath.Join(dir, ".mtc", "plugins", "demo")
	_ = os.Remove(filepath.Join(pluginDir, "main.py"))
	manifest := filepath.Join(pluginDir, "plugin.toml")
	if err := os.WriteFile(manifest, []byte("id = \"demo\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := Plugin([]string{"check"})
	if err == nil {
		t.Fatal("check must fail on a plugin with no command")
	}
	if !strings.Contains(err.Error(), "problem") {
		t.Errorf("error = %v, want a problem count", err)
	}
}

// TestPluginUnknownSubcommand proves the dispatcher surfaces a typo.
func TestPluginUnknownSubcommand(t *testing.T) {
	withAPIProject(t, "")
	if err := Plugin([]string{"frobnicate"}); err == nil {
		t.Fatal("unknown subcommand must error")
	}
	if err := Plugin(nil); err == nil {
		t.Fatal("no subcommand must print usage as an error")
	}
}
