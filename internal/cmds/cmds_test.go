package cmds

import (
	"os"
	"path/filepath"
	"testing"
)

// write puts a file in dir, creating it (path is relative to dir).
func write(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}

// TestLoadMergesDirs proves a project file replaces its global twin
// file-by-file while unrelated files from both dirs survive (§3).
func TestLoadMergesDirs(t *testing.T) {
	global, project := t.TempDir(), t.TempDir()
	write(t, global, "review.md", "GLOBAL body")
	write(t, global, "global-only.md", "only global")
	write(t, project, "review.md", "PROJECT body")

	got, err := Load(global, project)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("loaded %d commands, want 2: %+v", len(got), got)
	}
	c, ok := Find(got, "review")
	if !ok {
		t.Fatal("review not found")
	}
	if c.Body != "PROJECT body" {
		t.Errorf("review body = %q, want the project file", c.Body)
	}
	if _, ok := Find(got, "global-only"); !ok {
		t.Error("global-only.md must survive the merge")
	}
}

// TestLoadFrontmatter covers the fields a command may pin, and the empty
// body guard that keeps `mtc doctor` able to name a broken file.
func TestLoadFrontmatter(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "fix.md", "---\ndescription: fix a bug\nagent: plan\nmodel: mockt/t\n---\nFix $ARGUMENTS.")

	got, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	c := got[0]
	if c.ID != "fix" || c.Path != "fix.md" {
		t.Errorf("id/path = %q/%q", c.ID, c.Path)
	}
	if c.Description != "fix a bug" || c.Agent != "plan" || c.Model != "mockt/t" {
		t.Errorf("frontmatter = %+v", c)
	}
	if c.Body != "Fix $ARGUMENTS." {
		t.Errorf("body = %q", c.Body)
	}

	write(t, dir, "empty.md", "---\ndescription: nothing\n---\n   \n")
	if _, err := Load(dir); err == nil {
		t.Error("an empty body must fail the load")
	}
}

// TestLoadSkipsMissingDir: a fresh install has no commands/ yet.
func TestLoadSkipsMissingDir(t *testing.T) {
	got, err := Load(filepath.Join(t.TempDir(), "nope"))
	if err != nil {
		t.Fatalf("missing dir must not fail: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d commands", len(got))
	}
}

func TestFindUnknown(t *testing.T) {
	if _, ok := Find(nil, "x"); ok {
		t.Error("empty list must not resolve")
	}
}

// TestExpand: every placeholder expands, and a body without one still
// receives the arguments instead of dropping them.
func TestExpand(t *testing.T) {
	c := Command{Body: "a $ARGUMENTS b $ARGUMENTS"}
	if got := Expand(c, "X"); got != "a X b X" {
		t.Errorf("Expand = %q", got)
	}
	if got := Expand(Command{Body: "plain"}, " src/x.go "); got != "plain\n\nsrc/x.go" {
		t.Errorf("append form = %q", got)
	}
	if got := Expand(Command{Body: "plain"}, "  "); got != "plain" {
		t.Errorf("no args = %q", got)
	}
}
