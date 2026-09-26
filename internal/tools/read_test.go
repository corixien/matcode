package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"matcode/internal/engine"
)

func execRead(t *testing.T, wd, input string) (engine.Result, error) {
	t.Helper()
	return Read(wd).Execute(context.Background(), json.RawMessage(input))
}

// readFixture writes body at wd/name (creating parents) and returns its path.
func readFixture(t *testing.T, wd, name, body string) string {
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

// readNumbered builds n lines L0001…L000n with no trailing newline.
func readNumbered(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		if i > 1 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "L%04d", i)
	}
	return b.String()
}

// readWide builds n lines of exactly w bytes each ("0000" + padding).
func readWide(n, w int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "%04d", i)
		b.WriteString(strings.Repeat("y", w-4))
	}
	return b.String()
}

// TestReadOffsetLimitPaging pins the 1-based offset window and the footer
// that tells the model where to continue.
func TestReadOffsetLimitPaging(t *testing.T) {
	wd := t.TempDir()
	readFixture(t, wd, "f.txt", readNumbered(10))

	res, err := execRead(t, wd, `{"path":"f.txt"}`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != readNumbered(10) {
		t.Errorf("full read = %q, want the whole file with no footer", res.Text)
	}
	if len(res.Media) != 0 {
		t.Errorf("text read attached %d media items", len(res.Media))
	}

	res, err = execRead(t, wd, `{"path":"f.txt","offset":4,"limit":2}`)
	if err != nil {
		t.Fatal(err)
	}
	want := "L0004\nL0005\n[page ends at line 5 of 10; continue with offset=6]"
	if res.Text != want {
		t.Errorf("paged read = %q, want %q", res.Text, want)
	}

	res, err = execRead(t, wd, `{"path":"f.txt","offset":-3,"limit":1}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(res.Text, "L0001") {
		t.Errorf("offset < 1 must clamp to 1, got %q", res.Text)
	}

	// A short file pages to its end without inventing lines.
	res, err = execRead(t, wd, `{"path":"f.txt","offset":9,"limit":50}`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "L0009\nL0010" {
		t.Errorf("tail read = %q, want the last two lines and no footer", res.Text)
	}
}

// TestReadOffsetPastEnd: one line past the end yields an empty page (no
// error), two past yields the explanatory message.
func TestReadOffsetPastEnd(t *testing.T) {
	wd := t.TempDir()
	readFixture(t, wd, "f.txt", readNumbered(10))

	res, err := execRead(t, wd, `{"path":"f.txt","offset":11}`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "" {
		t.Errorf("offset == last+1 = %q, want empty page", res.Text)
	}

	res, err = execRead(t, wd, `{"path":"f.txt","offset":12}`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "offset 12 is past the end (10 lines)" {
		t.Errorf("offset past end = %q", res.Text)
	}
}

// TestReadLineCap: one read answers at most 2000 lines, whatever limit asks.
func TestReadLineCap(t *testing.T) {
	wd := t.TempDir()
	readFixture(t, wd, "big.txt", readNumbered(2500))
	const footer = "[page ends at line 2000 of 2500; continue with offset=2001]"

	for _, tc := range []struct {
		name, input string
	}{
		{"default", `{"path":"big.txt"}`},
		{"limit over cap", `{"path":"big.txt","limit":5000}`},
		{"negative limit", `{"path":"big.txt","limit":-10}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, err := execRead(t, wd, tc.input)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(res.Text, "L0001\n") {
				t.Errorf("page must start at line 1: %.20q", res.Text)
			}
			if !strings.Contains(res.Text, "\nL2000\n") {
				t.Error("line 2000 missing from the page")
			}
			if strings.Contains(res.Text, "L2001") {
				t.Error("line 2001 leaked past the cap")
			}
			if !strings.HasSuffix(res.Text, footer) {
				t.Errorf("page must end with %q, got tail %q", footer,
					res.Text[max(0, len(res.Text)-len(footer)-5):])
			}
		})
	}
}

// TestReadByteCap: a page also stops at 50KiB, cutting at a line boundary
// and still reporting the resume point.
func TestReadByteCap(t *testing.T) {
	wd := t.TempDir()
	readFixture(t, wd, "wide.txt", readWide(100, 1000))

	res, err := execRead(t, wd, `{"path":"wide.txt"}`)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(res.Text, "\n")
	if len(lines) != 52 {
		t.Fatalf("got %d lines (51 content + footer), want 52", len(lines))
	}
	if lines[0][:4] != "0000" || lines[50][:4] != "0050" {
		t.Errorf("page spans %q..%q, want 0000..0050", lines[0][:4], lines[50][:4])
	}
	if lines[51] != "[page ends at line 51 of 100; continue with offset=52]" {
		t.Errorf("footer = %q", lines[51])
	}
	body := strings.Join(lines[:51], "\n")
	if len(body) > 50*1024 {
		t.Errorf("page body = %d bytes, cap is 50KiB", len(body))
	}
}

// TestReadMissingFileAndDirectory: both are hard errors, not empty pages.
func TestReadMissingFileAndDirectory(t *testing.T) {
	wd := t.TempDir()
	if _, err := execRead(t, wd, `{"path":"nope.txt"}`); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing file err = %v, want a not-exist error", err)
	}
	if _, err := execRead(t, wd, `{"path":"."}`); err == nil {
		t.Error("reading a directory must fail")
	}
}

// TestReadAttachesImageAndPDF: sniffed media rides as base64 with the bare
// file name, and the size cap refuses oversized attachments.
func TestReadAttachesImageAndPDF(t *testing.T) {
	wd := t.TempDir()
	png := "\x89PNG\r\n\x1a\n" + strings.Repeat("x", 32)
	readFixture(t, wd, "sub/pic.png", png)

	res, err := execRead(t, wd, `{"path":"sub/pic.png"}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Media) != 1 {
		t.Fatalf("media = %v, want one attachment", res.Media)
	}
	m := res.Media[0]
	if m.Type != "image/png" || m.Name != "pic.png" {
		t.Errorf("media = %+v, want image/png named pic.png", m)
	}
	if m.Data != base64.StdEncoding.EncodeToString([]byte(png)) {
		t.Error("media data is not the base64 of the file")
	}
	if want := fmt.Sprintf("attached image/png (pic.png, %d bytes)", len(png)); res.Text != want {
		t.Errorf("text = %q, want %q", res.Text, want)
	}

	readFixture(t, wd, "doc.pdf", "%PDF-1.4\n%%EOF\n")
	res, err = execRead(t, wd, `{"path":"doc.pdf"}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Media) != 1 || res.Media[0].Type != "application/pdf" {
		t.Errorf("pdf media = %v, want one application/pdf attachment", res.Media)
	}

	if _, err := readMedia("big.png", "image/png", make([]byte, mediaMaxBytes+1)); err == nil ||
		!strings.Contains(err.Error(), "20MiB") {
		t.Errorf("oversized media err = %v, want the 20MiB refusal", err)
	}
}

// TestReadSniffsNotExtension: content type comes from the bytes, so a text
// file named .png is still text — and offset/limit still apply to media? No:
// media wins over paging.
func TestReadSniffsNotExtension(t *testing.T) {
	wd := t.TempDir()
	readFixture(t, wd, "notes.png", "just words\nsecond line\n")

	res, err := execRead(t, wd, `{"path":"notes.png"}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Media) != 0 {
		t.Errorf("text bytes named .png attached media: %v", res.Media)
	}
	if res.Text != "just words\nsecond line\n" {
		t.Errorf("text = %q", res.Text)
	}
}
