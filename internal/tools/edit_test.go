package tools

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"matcode/internal/engine"
)

// editTool seeds <wd>/f.txt with content and returns the Edit tool plus the
// workdir; inputs reference "f.txt" relative to it.
func editTool(t *testing.T, content string) (engine.Tool, string) {
	t.Helper()
	wd := t.TempDir()
	if err := os.WriteFile(filepath.Join(wd, "f.txt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return Edit(wd, false), wd
}

func TestEditReplacesExactString(t *testing.T) {
	tool, wd := editTool(t, "hello world\nsecond line\n")
	mutSchema(t, tool, "path", "old", "new")
	if _, ok := tool.Schema["properties"].(map[string]any)["replace_all"]; !ok {
		t.Error("property replace_all missing")
	}

	res, err := mutExec(t, tool, `{"path":"f.txt","old":"world","new":"there"}`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "edited f.txt (1 replacement(s))" {
		t.Errorf("result = %q", res.Text)
	}
	if got := mutRead(t, filepath.Join(wd, "f.txt")); got != "hello there\nsecond line\n" {
		t.Errorf("content = %q", got)
	}

	// Exact strings span newlines.
	if err := os.WriteFile(filepath.Join(wd, "f.txt"), []byte("a\nb\nc"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := mutExec(t, tool, `{"path":"f.txt","old":"a\nb","new":"X"}`); err != nil {
		t.Fatal(err)
	}
	if got := mutRead(t, filepath.Join(wd, "f.txt")); got != "X\nc" {
		t.Errorf("multiline edit → %q", got)
	}
}

func TestEditNoMatchIsError(t *testing.T) {
	tool, wd := editTool(t, "abc\n")

	_, err := mutExec(t, tool, `{"path":"f.txt","old":"zzz","new":"q"}`)
	if err == nil || !strings.Contains(err.Error(), "old string not found in f.txt") {
		t.Fatalf("err = %v, want the not-found error naming the path", err)
	}
	if got := mutRead(t, filepath.Join(wd, "f.txt")); got != "abc\n" {
		t.Errorf("content = %q, want the file untouched", got)
	}
}

func TestEditMultipleMatches(t *testing.T) {
	tool, wd := editTool(t, "x y x\n")

	// Ambiguity without replace_all is an error, not a guess.
	_, err := mutExec(t, tool, `{"path":"f.txt","old":"x","new":"Z"}`)
	if err == nil ||
		!strings.Contains(err.Error(), "occurs 2 times") ||
		!strings.Contains(err.Error(), "replace_all") {
		t.Fatalf("err = %v, want the ambiguity error", err)
	}
	if got := mutRead(t, filepath.Join(wd, "f.txt")); got != "x y x\n" {
		t.Errorf("content = %q, want the file untouched", got)
	}

	res, err := mutExec(t, tool, `{"path":"f.txt","old":"x","new":"Z","replace_all":true}`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "edited f.txt (2 replacement(s))" {
		t.Errorf("result = %q", res.Text)
	}
	if got := mutRead(t, filepath.Join(wd, "f.txt")); got != "Z y Z\n" {
		t.Errorf("content = %q, want every occurrence replaced", got)
	}
}

func TestEditEmptyOldRejectedBeforeRead(t *testing.T) {
	tool, _ := editTool(t, "abc")

	// The guard runs before the file read, so a missing path never masks it.
	_, err := mutExec(t, tool, `{"path":"gone.txt","old":"","new":"q"}`)
	if err == nil || !strings.Contains(err.Error(), "old string is empty") {
		t.Errorf("err = %v, want the empty-old guard", err)
	}
}

func TestEditEmptyNewDeletes(t *testing.T) {
	tool, wd := editTool(t, "keepDELETEme")

	// Current behavior: `required` is advisory — a missing new field is
	// the zero value, so the match is deleted instead of erroring.
	res, err := mutExec(t, tool, `{"path":"f.txt","old":"DELETEme"}`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "edited f.txt (1 replacement(s))" {
		t.Errorf("result = %q", res.Text)
	}
	if got := mutRead(t, filepath.Join(wd, "f.txt")); got != "keep" {
		t.Errorf("content = %q, want the match deleted", got)
	}
}

func TestEditOldEqualsNewIsSuccessfulNoOp(t *testing.T) {
	tool, wd := editTool(t, "same")

	// Current behavior: no old==new guard — the call succeeds, reports a
	// replacement, and leaves the file byte-identical.
	res, err := mutExec(t, tool, `{"path":"f.txt","old":"same","new":"same"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "(1 replacement(s))") {
		t.Errorf("result = %q", res.Text)
	}
	if got := mutRead(t, filepath.Join(wd, "f.txt")); got != "same" {
		t.Errorf("content = %q, want unchanged", got)
	}
}

func TestEditMissingAndBadInput(t *testing.T) {
	tool, _ := editTool(t, "abc")

	// Missing path resolves to the workdir (a directory): error, no panic.
	if _, err := mutExec(t, tool, `{"old":"abc","new":"x"}`); err == nil ||
		!strings.Contains(err.Error(), "is a directory") {
		t.Errorf("err = %v, want a directory error", err)
	}
	// Missing file surfaces the read error untouched.
	_, err := mutExec(t, tool, `{"path":"gone.txt","old":"a","new":"b"}`)
	if !errors.Is(err, os.ErrNotExist) || !strings.Contains(err.Error(), "gone.txt") {
		t.Errorf("err = %v, want ENOENT naming the path", err)
	}
	// Malformed JSON surfaces as an error.
	if _, err := mutExec(t, tool, "not-json"); err == nil {
		t.Error("malformed JSON must error, not panic")
	}
}
