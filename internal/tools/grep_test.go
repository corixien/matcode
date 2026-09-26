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

func execGrep(t *testing.T, wd, input string) (engine.Result, error) {
	t.Helper()
	return Grep(wd).Execute(context.Background(), json.RawMessage(input))
}

// grepFixture writes body at wd/name (creating parents).
func grepFixture(t *testing.T, wd, name, body string) string {
	t.Helper()
	p := filepath.Join(wd, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestGrepMatchesPathLineText pins the hit format: workdir-relative path,
// 1-based line number, the line itself.
func TestGrepMatchesPathLineText(t *testing.T) {
	wd := t.TempDir()
	grepFixture(t, wd, "a.txt", "alpha\nneedle one\nbeta\nneedle two\n")
	grepFixture(t, wd, filepath.Join("sub", "b.txt"), "needle three\n")

	res, err := execGrep(t, wd, `{"pattern":"needle"}`)
	if err != nil {
		t.Fatal(err)
	}
	want := "a.txt:2:needle one\na.txt:4:needle two\nsub/b.txt:1:needle three"
	if res.Text != want {
		t.Errorf("hits =\n%s\nwant\n%s", res.Text, want)
	}
}

func TestGrepNoMatches(t *testing.T) {
	wd := t.TempDir()
	grepFixture(t, wd, "a.txt", "nothing here\n")
	res, err := execGrep(t, wd, `{"pattern":"absent"}`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "no matches" {
		t.Errorf("text = %q, want %q", res.Text, "no matches")
	}
}

// TestGrepRegexNotLiteral: the pattern is a Go regexp — unescaped dots match
// any byte, and case only folds with (?i).
func TestGrepRegexNotLiteral(t *testing.T) {
	wd := t.TempDir()
	grepFixture(t, wd, "c.txt",
		"hello world\nHELLO THERE\na.c\nabc\nfoo42\nfooZZ\n")

	for _, tc := range []struct {
		pattern string
		lines   []string // expected line numbers
	}{
		{"hello", []string{"1"}},
		{"(?i)hello", []string{"1", "2"}},
		{"a.c", []string{"3", "4"}},
		{`a\.c`, []string{"3"}},
		{`foo[0-9]+`, []string{"5"}},
		{`^HELLO`, []string{"2"}},
	} {
		payload, err := json.Marshal(map[string]string{"pattern": tc.pattern})
		if err != nil {
			t.Fatal(err)
		}
		res, err := execGrep(t, wd, string(payload))
		if err != nil {
			t.Fatalf("pattern %q: %v", tc.pattern, err)
		}
		var got []string
		if res.Text != "no matches" {
			for _, ln := range strings.Split(res.Text, "\n") {
				f := strings.SplitN(ln, ":", 3)
				if len(f) != 3 {
					t.Fatalf("malformed hit %q", ln)
				}
				got = append(got, f[1])
			}
		}
		if strings.Join(got, ",") != strings.Join(tc.lines, ",") {
			t.Errorf("pattern %q hit lines %v, want %v", tc.pattern, got, tc.lines)
		}
	}
}

// TestGrepHasNoIncludeFilter documents current behaviour: only pattern and
// path are read, so extra filters in the input are silently ignored.
func TestGrepHasNoIncludeFilter(t *testing.T) {
	wd := t.TempDir()
	grepFixture(t, wd, "x.go", "needle in go\n")
	grepFixture(t, wd, "y.txt", "needle in text\n")

	props, _ := Grep(wd).Schema["properties"].(map[string]any)
	if len(props) != 2 {
		t.Errorf("schema properties = %v, want exactly pattern and path", props)
	}
	if _, ok := props["include"]; ok {
		t.Error("an include filter is advertised but not implemented")
	}

	res, err := execGrep(t, wd, `{"pattern":"needle","include":"*.go","case_insensitive":true}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "x.go:") || !strings.Contains(res.Text, "y.txt:") {
		t.Errorf("include/case fields were honoured:\n%s\nwant both files searched", res.Text)
	}
}

// TestGrepHitCap: one read reports at most 200 lines.
func TestGrepHitCap(t *testing.T) {
	wd := t.TempDir()
	var b strings.Builder
	for i := 1; i <= 300; i++ {
		b.WriteString("needle\n")
	}
	grepFixture(t, wd, "many.txt", b.String())

	res, err := execGrep(t, wd, `{"pattern":"needle"}`)
	if err != nil {
		t.Fatal(err)
	}
	hits := strings.Split(res.Text, "\n")
	if len(hits) != 200 {
		t.Fatalf("got %d hits, want the 200 cap", len(hits))
	}
	if hits[0] != "many.txt:1:needle" || hits[199] != "many.txt:200:needle" {
		t.Errorf("first=%q last=%q", hits[0], hits[199])
	}
}

// TestGrepTruncatesLongLines: a hit never carries more than 300 bytes.
func TestGrepTruncatesLongLines(t *testing.T) {
	wd := t.TempDir()
	grepFixture(t, wd, "long.txt", "needle"+strings.Repeat("z", 400)+"\n")

	res, err := execGrep(t, wd, `{"pattern":"needle"}`)
	if err != nil {
		t.Fatal(err)
	}
	hit := strings.TrimPrefix(res.Text, "long.txt:1:")
	if len(hit) != 300 {
		t.Errorf("hit line = %d bytes, want 300", len(hit))
	}
	if !strings.HasPrefix(hit, "needle") {
		t.Errorf("hit = %.40q, want the match at the head", hit)
	}
}

// TestGrepSkipsNoise: hidden dirs, node_modules, vendor, binaries, media
// extensions and oversized files never reach the output.
func TestGrepSkipsNoise(t *testing.T) {
	wd := t.TempDir()
	grepFixture(t, wd, filepath.Join(".git", "g.txt"), "needle\n")
	grepFixture(t, wd, filepath.Join("node_modules", "n.txt"), "needle\n")
	grepFixture(t, wd, filepath.Join("vendor", "v.txt"), "needle\n")
	grepFixture(t, wd, "keep.txt", "needle keep\n")
	grepFixture(t, wd, "bin.dat", "needle\x00binary\n")
	grepFixture(t, wd, "pic.png", "needle\n")
	big := append([]byte("needle"), make([]byte, 1024*1024)...)
	grepFixture(t, wd, "big.txt", string(big))

	res, err := execGrep(t, wd, `{"pattern":"needle"}`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "keep.txt:1:needle keep" {
		t.Errorf("hits =\n%s\nwant only keep.txt", res.Text)
	}
}

// TestGrepSubdirectoryScope: path narrows the walk; rel paths stay relative
// to the workdir.
func TestGrepSubdirectoryScope(t *testing.T) {
	wd := t.TempDir()
	grepFixture(t, wd, "root.txt", "needle root\n")
	grepFixture(t, wd, filepath.Join("sub", "leaf.txt"), "needle leaf\n")

	res, err := execGrep(t, wd, `{"pattern":"needle","path":"sub"}`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != filepath.Join("sub", "leaf.txt")+":1:needle leaf" {
		t.Errorf("hits = %q, want only the sub file", res.Text)
	}
}

func TestGrepInvalidPattern(t *testing.T) {
	wd := t.TempDir()
	if _, err := execGrep(t, wd, `{"pattern":"["}`); err == nil {
		t.Fatal("an invalid regexp must surface as an error")
	}
}
