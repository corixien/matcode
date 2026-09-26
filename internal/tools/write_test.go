package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteCreatesFileAndParentDirs(t *testing.T) {
	wd := t.TempDir()
	tool := Write(wd, false)
	mutSchema(t, tool, "path", "content")

	res, err := mutExec(t, tool, `{"path":"a/b/c.txt","content":"hello\nworld"}`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "wrote 11 bytes to a/b/c.txt" {
		t.Errorf("result = %q, want the byte count and the path as given", res.Text)
	}
	if got := mutRead(t, filepath.Join(wd, "a", "b", "c.txt")); got != "hello\nworld" {
		t.Errorf("content = %q, want it byte-exact", got)
	}
}

func TestWriteOverwritesExistingContent(t *testing.T) {
	wd := t.TempDir()
	tool := Write(wd, false)
	path := filepath.Join(wd, "f.txt")

	if _, err := mutExec(t, tool, `{"path":"f.txt","content":"first draft"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := mutExec(t, tool, `{"path":"f.txt","content":"second"}`); err != nil {
		t.Fatal(err)
	}
	if got := mutRead(t, path); got != "second" {
		t.Errorf("content = %q, want the last write to win", got)
	}

	// Empty content truncates the file to zero bytes.
	if _, err := mutExec(t, tool, `{"path":"f.txt","content":""}`); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path); err != nil {
		t.Fatal(err)
	} else if fi.Size() != 0 {
		t.Errorf("size = %d, want 0", fi.Size())
	}

	// Current behavior: `required` is advisory — a missing content field
	// writes an empty file instead of erroring.
	if _, err := mutExec(t, tool, `{"path":"nofield.txt"}`); err != nil {
		t.Fatalf("missing content must be a successful empty write: %v", err)
	}
	if got := mutRead(t, filepath.Join(wd, "nofield.txt")); got != "" {
		t.Errorf("content = %q, want empty", got)
	}
}

func TestWritePathEscapesWorkdirAsImplemented(t *testing.T) {
	root := t.TempDir()
	wd := filepath.Join(root, "proj")
	if err := os.MkdirAll(wd, 0o755); err != nil {
		t.Fatal(err)
	}
	tool := Write(wd, false)

	// ".." segments are cleaned, not blocked: resolve() lets relative
	// paths leave the workdir.
	if _, err := mutExec(t, tool, `{"path":"../escape.txt","content":"out"}`); err != nil {
		t.Fatal(err)
	}
	if got := mutRead(t, filepath.Join(root, "escape.txt")); got != "out" {
		t.Errorf("../escape.txt → %q, want a file in the parent of the workdir", got)
	}

	// Absolute paths are taken verbatim, also outside the workdir.
	abs := filepath.Join(root, "abs.txt")
	if _, err := mutExec(t, tool, `{"path":"`+abs+`","content":"abs"}`); err != nil {
		t.Fatal(err)
	}
	if got := mutRead(t, abs); got != "abs" {
		t.Errorf("absolute path → %q, want it written as given", got)
	}
}

func TestWriteErrors(t *testing.T) {
	wd := t.TempDir()
	tool := Write(wd, false)

	// Writing onto a directory fails instead of clobbering it.
	if err := os.MkdirAll(filepath.Join(wd, "adir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := mutExec(t, tool, `{"path":"adir","content":"x"}`); err == nil ||
		!strings.Contains(err.Error(), "is a directory") {
		t.Errorf("err = %v, want a directory error", err)
	}

	// A file where a parent directory belongs fails the MkdirAll.
	if err := os.WriteFile(filepath.Join(wd, "blocker"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := mutExec(t, tool, `{"path":"blocker/inner.txt","content":"x"}`); err == nil ||
		!strings.Contains(err.Error(), "not a directory") {
		t.Errorf("err = %v, want a not-a-directory error", err)
	}

	// Missing path resolves to the workdir itself (a directory): error, no panic.
	if _, err := mutExec(t, tool, `{"content":"x"}`); err == nil ||
		!strings.Contains(err.Error(), "is a directory") {
		t.Errorf("err = %v, want a directory error for a missing path", err)
	}

	// Malformed JSON surfaces as an error.
	if _, err := mutExec(t, tool, `{"path":`); err == nil {
		t.Error("malformed JSON must error, not panic")
	}
}
