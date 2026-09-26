package tools

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"matcode/internal/engine"
)

// patchTool seeds <wd>/f.txt with content and returns the Patch tool plus
// the workdir; inputs reference "f.txt" relative to it.
func patchTool(t *testing.T, content string) (engine.Tool, string) {
	t.Helper()
	wd := t.TempDir()
	if err := os.WriteFile(filepath.Join(wd, "f.txt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return Patch(wd, false), wd
}

// patchInput embeds a diff into the tool's JSON input.
func patchInput(diff string) string {
	return fmt.Sprintf(`{"path":"f.txt","diff":%q}`, diff)
}

func TestPatchAppliesUnifiedDiff(t *testing.T) {
	tool, wd := patchTool(t, "line1\nline2\nline3")
	mutSchema(t, tool, "path", "diff")

	// Git-style headers, one hunk, and a no-newline marker.
	diff := "--- a/f.txt\n+++ b/f.txt\n@@ -1,3 +1,3 @@\n line1\n-line2\n+two\n line3\n" +
		"\\ No newline at end of file"
	res, err := mutExec(t, tool, patchInput(diff))
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "patched f.txt" {
		t.Errorf("result = %q", res.Text)
	}
	if got := mutRead(t, filepath.Join(wd, "f.txt")); got != "line1\ntwo\nline3" {
		t.Errorf("content = %q", got)
	}

	// Pure insertion with context only.
	if err := os.WriteFile(filepath.Join(wd, "f.txt"), []byte("a\nb"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := mutExec(t, tool, patchInput("@@ -1,2 +1,3 @@\n a\n+inserted\n b")); err != nil {
		t.Fatal(err)
	}
	if got := mutRead(t, filepath.Join(wd, "f.txt")); got != "a\ninserted\nb" {
		t.Errorf("insertion → %q", got)
	}

	// A trailing newline after the last hunk line is read as an empty
	// context line (current behavior) and matches the file's own empty
	// final line, so the trailing newline survives the patch.
	if err := os.WriteFile(filepath.Join(wd, "f.txt"), []byte("a\nb\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := mutExec(t, tool, patchInput("@@ -1,2 +1,2 @@\n a\n-b\n+B\n")); err != nil {
		t.Fatal(err)
	}
	if got := mutRead(t, filepath.Join(wd, "f.txt")); got != "a\nB\n" {
		t.Errorf("trailing-newline patch → %q", got)
	}
}

func TestPatchMultipleHunks(t *testing.T) {
	tool, wd := patchTool(t, "a\nb\nc\nd\ne\nf\n")

	diff := "@@ -1,2 +1,2 @@\n a\n-b\n+B\n@@ -5,2 +5,2 @@\n e\n-f\n+F"
	if _, err := mutExec(t, tool, patchInput(diff)); err != nil {
		t.Fatal(err)
	}
	// Both hunks splice in; the lines between and after stay untouched.
	if got := mutRead(t, filepath.Join(wd, "f.txt")); got != "a\nB\nc\nd\ne\nF\n" {
		t.Errorf("content = %q, want both hunks applied", got)
	}
}

func TestPatchHunkContextMismatch(t *testing.T) {
	tool, wd := patchTool(t, "line1\nline2\nline3")

	diff := "@@ -1,3 +1,3 @@\n nope\n-line2\n+two\n line3"
	_, err := mutExec(t, tool, patchInput(diff))
	if err == nil ||
		!strings.Contains(err.Error(), "f.txt: hunk context mismatch at line 1") {
		t.Fatalf("err = %v, want the mismatch error naming file and line", err)
	}
	if got := mutRead(t, filepath.Join(wd, "f.txt")); got != "line1\nline2\nline3" {
		t.Errorf("content = %q, want the file untouched on mismatch", got)
	}
}

func TestPatchHunkDoesNotFit(t *testing.T) {
	tool, wd := patchTool(t, "line1\nline2\nline3")

	_, err := mutExec(t, tool, patchInput("@@ -10,2 +10,2 @@\n x\n-y\n+z"))
	if err == nil || !strings.Contains(err.Error(), "f.txt: hunk at line 10 does not fit the file") {
		t.Fatalf("err = %v, want the does-not-fit error", err)
	}
	if got := mutRead(t, filepath.Join(wd, "f.txt")); got != "line1\nline2\nline3" {
		t.Errorf("content = %q, want the file untouched", got)
	}
}

func TestPatchFileNotFound(t *testing.T) {
	tool, _ := patchTool(t, "x")

	_, err := mutExec(t, tool, fmt.Sprintf(`{"path":"gone.txt","diff":%q}`,
		"@@ -1,1 +1,1 @@\n-a\n+b"))
	if !errors.Is(err, os.ErrNotExist) || !strings.Contains(err.Error(), "gone.txt") {
		t.Errorf("err = %v, want ENOENT naming the path", err)
	}
}

func TestPatchDiffShapeErrors(t *testing.T) {
	tool, wd := patchTool(t, "abc")

	// Missing diff field: zero hunks, reported with the path prefix.
	if _, err := mutExec(t, tool, `{"path":"f.txt"}`); err == nil ||
		!strings.Contains(err.Error(), "f.txt: no @@ hunks in diff") {
		t.Errorf("err = %v, want the no-hunks error", err)
	}
	// A header hunkStart cannot parse.
	bad := "@@ -0,1 +0,1 @@\n-a\n+b"
	if _, err := mutExec(t, tool, patchInput(bad)); err == nil ||
		!strings.Contains(err.Error(), "bad hunk header") {
		t.Errorf("err = %v, want the bad-header error", err)
	}
	// Missing path resolves to the workdir (a directory).
	if _, err := mutExec(t, tool, fmt.Sprintf(`{"diff":%q}`,
		"@@ -1,1 +1,1 @@\n-a\n+b")); err == nil ||
		!strings.Contains(err.Error(), "is a directory") {
		t.Errorf("err = %v, want a directory error for a missing path", err)
	}
	// Malformed JSON surfaces as an error.
	if _, err := mutExec(t, tool, "{"); err == nil {
		t.Error("malformed JSON must error, not panic")
	}
	// Every failure above must leave the file untouched.
	if got := mutRead(t, filepath.Join(wd, "f.txt")); got != "abc" {
		t.Errorf("content = %q, want the file untouched", got)
	}
}
