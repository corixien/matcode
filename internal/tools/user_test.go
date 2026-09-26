package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"matcode/internal/engine"
)

// mkTool writes `<root>/<name>/{tool.toml,exec.sh}` and returns the folder.
func mkTool(t *testing.T, root, name, tomlBody, script string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tool.toml"), []byte(tomlBody), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "exec.sh"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func run(t *testing.T, list []engine.Tool, id, input string) (engine.Result, error) {
	t.Helper()
	for _, tool := range list {
		if tool.ID == id {
			return tool.Execute(context.Background(), json.RawMessage(input))
		}
	}
	t.Fatalf("tool %q not registered (have %d tools)", id, len(list))
	return engine.Result{}, nil
}

// TestUserExecutesContract is the spec §registry round trip: JSON in on
// stdin, {"output": …} out on stdout.
func TestUserExecutesContract(t *testing.T) {
	root := t.TempDir()
	mkTool(t, root, "echo",
		"id = \"echo\"\ndescription = \"echo the input\"\n[input]\ntype = \"object\"\nproperties = { text = { type = \"string\" } }\n",
		`input=$(cat); printf '{"output":%s}' "$input"`)

	list := User(t.TempDir(), root)
	if len(list) != 1 {
		t.Fatalf("registered %d tools", len(list))
	}
	tool := list[0]
	if tool.Description != "echo the input" {
		t.Errorf("description = %q", tool.Description)
	}
	if tool.Schema["type"] != "object" {
		t.Errorf("schema = %v, want type=object", tool.Schema)
	}
	if _, ok := tool.Schema["properties"]; !ok {
		t.Errorf("declared properties were dropped: %v", tool.Schema)
	}

	res, err := run(t, list, "echo", `{"text":"hi"}`)
	if err != nil {
		t.Fatal(err)
	}
	// The output key carried an object, not a string: it is passed through
	// as-is rather than stringified twice.
	if res.Text != `{"text":"hi"}` {
		t.Errorf("output = %q", res.Text)
	}
}

// TestUserFolderNameIsIDAndRawStdout: no `id` in tool.toml means the
// folder name, and a script that prints plain text still works.
func TestUserFolderNameIsIDAndRawStdout(t *testing.T) {
	root := t.TempDir()
	mkTool(t, root, "shout", "description = \"yell\"\n", `echo HELLO`)

	list := User(t.TempDir(), root)
	res, err := run(t, list, "shout", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "HELLO" {
		t.Errorf("output = %q, want HELLO", res.Text)
	}
}

// TestUserProjectWins: the same id in a later dir replaces the global one
// (§3 resolution, and the reason a project tool can be a stub).
func TestUserProjectWins(t *testing.T) {
	global, project := t.TempDir(), t.TempDir()
	mkTool(t, global, "probe", "description = \"global\"\n", `printf '{"output":"global"}'`)
	mkTool(t, project, "probe", "description = \"project\"\n", `printf '{"output":"project"}'`)

	list := User(t.TempDir(), global, project)
	if len(list) != 1 {
		t.Fatalf("registered %d tools, want 1 (last wins)", len(list))
	}
	if list[0].Description != "project" {
		t.Errorf("description = %q, want the project one", list[0].Description)
	}
	res, err := run(t, list, "probe", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "project" {
		t.Errorf("output = %q", res.Text)
	}
}

// TestUserFailureSurfaces: a non-zero exit reaches the model as an error
// carrying stderr, and a vanished script does not panic.
func TestUserFailureSurfaces(t *testing.T) {
	root := t.TempDir()
	mkTool(t, root, "boom", "description = \"fail\"\n", `echo "nope" >&2; exit 3`)
	mkTool(t, root, "gone", "description = \"missing\"\n", `echo hi`)

	list := User(t.TempDir(), root)
	if _, err := run(t, list, "boom", `{}`); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("err = %v, want the exit status plus stderr", err)
	}
	if err := os.Remove(filepath.Join(root, "gone", "exec.sh")); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, list, "gone", `{}`); err == nil {
		t.Fatal("a missing exec.sh must fail the call")
	}
}

// TestUserToleratesBrokenFolders: one bad folder never takes the registry
// down; Validate reports exactly those problems for `mtc doctor`.
func TestUserToleratesBrokenFolders(t *testing.T) {
	root := t.TempDir()
	mkTool(t, root, "good", "description = \"fine\"\n", `echo hi`)
	if err := os.MkdirAll(filepath.Join(root, "bare"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "bad"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "bad", "tool.toml"), []byte("id ="), 0644); err != nil {
		t.Fatal(err)
	}

	if list := User(t.TempDir(), root); len(list) != 1 || list[0].ID != "good" {
		t.Fatalf("User = %+v, want only the good tool", list)
	}
	n, problems := Validate(root)
	if n != 1 {
		t.Errorf("Validate count = %d, want 1", n)
	}
	if len(problems) != 2 {
		t.Fatalf("problems = %v, want 2 (bare + bad)", problems)
	}
	joined := strings.Join(problems, " ")
	if !strings.Contains(joined, "no tool.toml") || !strings.Contains(joined, "bad") {
		t.Errorf("problems = %v", problems)
	}
}

// TestMergeLastWins is the registry rule of §registry: a data-tree tool
// shadows a builtin id, and an unknown id simply joins the list.
func TestMergeLastWins(t *testing.T) {
	base := []engine.Tool{
		{ID: "read", Description: "builtin read"},
		{ID: "write", Description: "builtin write"},
	}
	extra := []engine.Tool{
		{ID: "read", Description: "mine"},
		{ID: "probe", Description: "new"},
	}
	out := Merge(base, extra)
	if len(out) != 3 {
		t.Fatalf("merged len = %d, want 3", len(out))
	}
	byID := map[string]string{}
	for _, tool := range out {
		byID[tool.ID] = tool.Description
	}
	if byID["read"] != "mine" {
		t.Errorf("read = %q, want the later registration", byID["read"])
	}
	if byID["write"] != "builtin write" || byID["probe"] != "new" {
		t.Errorf("merged = %v", byID)
	}
	if base[0].Description != "builtin read" {
		t.Error("Merge must not mutate its inputs")
	}
	if Merge(base, nil)[0].ID != "read" {
		t.Error("Merge with nothing extra must be a no-op")
	}
}
