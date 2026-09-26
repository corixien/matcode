package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"matcode/internal/engine"
)

func execGlob(t *testing.T, wd, input string) (engine.Result, error) {
	t.Helper()
	return Glob(wd).Execute(context.Background(), json.RawMessage(input))
}

// globFixture creates name (relative to wd, parents included).
func globFixture(t *testing.T, wd, name string) {
	t.Helper()
	p := filepath.Join(wd, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// globCase pairs a pattern with the exact expected output.
type globCase struct{ pattern, want string }

func (tc globCase) patternJSON() string {
	b, _ := json.Marshal(tc.pattern)
	return string(b)
}

// TestGlobPatternExpansion: matches come back workdir-relative, one per
// line, in filepath.Glob's lexical order.
func TestGlobPatternExpansion(t *testing.T) {
	wd := t.TempDir()
	for _, n := range []string{"a.go", "b.txt", "sub/c.go", "sub/d.go", "sub/deep/e.go"} {
		globFixture(t, wd, n)
	}

	for _, tc := range []globCase{
		{"*.go", "a.go"},
		{"*.txt", "b.txt"},
		{"sub/*.go", "sub/c.go\nsub/d.go"},
		{"*/*.go", "sub/c.go\nsub/d.go"},
		{"*.rs", "no matches"},
	} {
		res, err := execGlob(t, wd, `{"pattern":`+tc.patternJSON()+`}`)
		if err != nil {
			t.Fatalf("pattern %q: %v", tc.pattern, err)
		}
		if res.Text != tc.want {
			t.Errorf("pattern %q =\n%s\nwant\n%s", tc.pattern, res.Text, tc.want)
		}
	}
}

// TestGlobStarIsNotRecursive: filepath.Glob has no ** semantics, so a deep
// file stays unreachable — current behaviour, documented here.
func TestGlobStarIsNotRecursive(t *testing.T) {
	wd := t.TempDir()
	for _, n := range []string{"a.go", "sub/c.go", "sub/deep/e.go"} {
		globFixture(t, wd, n)
	}
	res, err := execGlob(t, wd, `{"pattern":"**/*.go"}`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Text, "deep") {
		t.Errorf("** reached a nested dir: %q", res.Text)
	}
	if res.Text != "sub/c.go" {
		t.Errorf("**/*.go = %q, want sub/c.go", res.Text)
	}
}

// TestGlobIncludesHidden: dotfiles are matched like any other file — the
// pattern, not the tool, decides visibility.
func TestGlobIncludesHidden(t *testing.T) {
	wd := t.TempDir()
	globFixture(t, wd, ".hidden.go")
	globFixture(t, wd, "visible.go")

	res, err := execGlob(t, wd, `{"pattern":"*.go"}`)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(res.Text, "\n")
	if len(got) != 2 || got[0] != ".hidden.go" || got[1] != "visible.go" {
		t.Errorf("matches = %q, want both dotfile and visible file", res.Text)
	}
}

// TestGlobAbsolutePattern: absolute patterns resolve, output stays relative
// to the workdir.
func TestGlobAbsolutePattern(t *testing.T) {
	wd := t.TempDir()
	globFixture(t, wd, "a.go")
	res, err := execGlob(t, wd, `{"pattern":`+absPatternJSON(t, wd, "*.go")+`}`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "a.go" {
		t.Errorf("absolute pattern = %q, want a.go", res.Text)
	}
}

func absPatternJSON(t *testing.T, wd, suffix string) string {
	t.Helper()
	b, err := json.Marshal(filepath.Join(wd, suffix))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestGlobCap: at most 200 paths come back from a wide expansion.
func TestGlobCap(t *testing.T) {
	wd := t.TempDir()
	for i := 0; i < 250; i++ {
		globFixture(t, wd, fmt.Sprintf("f%03d.txt", i))
	}
	res, err := execGlob(t, wd, `{"pattern":"*.txt"}`)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(res.Text, "\n")
	if len(got) != 200 {
		t.Fatalf("got %d paths, want the 200 cap", len(got))
	}
	if got[0] != "f000.txt" || got[199] != "f199.txt" {
		t.Errorf("window = %q..%q, want the first 200 lexically", got[0], got[199])
	}
}

// TestGlobBadPattern: malformed patterns surface as errors, not panics.
func TestGlobBadPattern(t *testing.T) {
	wd := t.TempDir()
	if _, err := execGlob(t, wd, `{"pattern":"["}`); err == nil {
		t.Fatal("ErrBadPattern must propagate")
	}
}
