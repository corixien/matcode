package attach

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixtureBody is the five-line text file used by the range tests.
const fixtureBody = "l1\nl2\nl3\nl4\nl5\n"

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// pngFixture renders a wide PNG; the sparse pixel pattern keeps it small.
func pngFixture(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y += 13 {
		for x := 0; x < w; x += 17 {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 0x40, A: 0xff})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

const pdfFixture = "%PDF-1.4\n1 0 obj<<>>endobj\ntrailer<<>>\n%%EOF\n"

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func TestDefaultMediaConfig(t *testing.T) {
	mc := DefaultMediaConfig()
	if !mc.AutoResize {
		t.Error("AutoResize must default to on")
	}
	if mc.MaxBase64Bytes != 5<<20 {
		t.Errorf("MaxBase64Bytes = %d, want %d", mc.MaxBase64Bytes, 5<<20)
	}
	if mc.MaxFileBytes != 20<<20 {
		t.Errorf("MaxFileBytes = %d, want %d", mc.MaxFileBytes, 20<<20)
	}
}

func TestResolvePlainTextFile(t *testing.T) {
	cwd := t.TempDir()
	mustWriteFile(t, filepath.Join(cwd, "notes.txt"), fixtureBody)

	// Relative path resolves against cwd, absolute path is used as-is.
	for _, ref := range []string{"notes.txt", filepath.Join(cwd, "notes.txt")} {
		a, err := Resolve(cwd, ref, DefaultMediaConfig())
		if err != nil {
			t.Fatalf("Resolve(%q): %v", ref, err)
		}
		if a.Name != "notes.txt" {
			t.Errorf("Name = %q, want notes.txt", a.Name)
		}
		if want := "--- notes.txt ---\n" + fixtureBody; a.Text != want {
			t.Errorf("Text = %q, want %q", a.Text, want)
		}
		if len(a.Media) != 0 {
			t.Errorf("Media = %v, want none for a text file", a.Media)
		}
	}
}

func TestResolveFileURIWithLineRange(t *testing.T) {
	cwd := t.TempDir()
	mustWriteFile(t, filepath.Join(cwd, "notes.txt"), fixtureBody)
	uri := "file://" + filepath.Join(cwd, "notes.txt")

	for _, tc := range []struct {
		query string
		want  string
	}{
		{"", "--- notes.txt ---\n" + fixtureBody},
		{"?start=2&end=4", "--- notes.txt (lines 2-4) ---\nl2\nl3\nl4"},
		{"?start=3&end=4", "--- notes.txt (lines 3-4) ---\nl3\nl4"},
		{"?start=99&end=100", "--- notes.txt (lines 99-100) ---\n"},
		// Malformed values fall back to the whole file.
		{"?start=abc&end=zz", "--- notes.txt ---\n" + fixtureBody},
		// end-only keeps a plain header (start==0), body is clamped.
		{"?end=2", "--- notes.txt ---\nl1\nl2"},
	} {
		a, err := Resolve(cwd, uri+tc.query, DefaultMediaConfig())
		if err != nil {
			t.Fatalf("Resolve(%q): %v", tc.query, err)
		}
		if a.Text != tc.want {
			t.Errorf("query %q: Text = %q, want %q", tc.query, a.Text, tc.want)
		}
	}

	// start-only slices correctly; the header echoes the raw (unclamped)
	// end value — current behavior, asserted on the body only.
	a, err := Resolve(cwd, uri+"?start=3", DefaultMediaConfig())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(a.Text, "l3\nl4\nl5\n") {
		t.Errorf("start-only body = %q, want suffix l3..l5", a.Text)
	}
	// end clamped to EOF, body sliced from line 4 on.
	a, err = Resolve(cwd, uri+"?start=4&end=99", DefaultMediaConfig())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(a.Text, "l4\nl5\n") {
		t.Errorf("clamped-end body = %q, want suffix l4..l5", a.Text)
	}
}

func TestResolveDirectoryListing(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "proj")
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(dir, "a.txt"), "x")
	mustWriteFile(t, filepath.Join(dir, "z.go"), "y")
	mustWriteFile(t, filepath.Join(dir, ".env"), "K=v")
	mustWriteFile(t, filepath.Join(dir, "sub", "inner.txt"), "z")

	a, err := Resolve(root, "proj", DefaultMediaConfig())
	if err != nil {
		t.Fatal(err)
	}
	if a.Name != "proj" {
		t.Errorf("Name = %q, want proj", a.Name)
	}
	// Non-recursive, sorted, directories get a trailing slash, no filtering.
	want := "--- proj ---\n.env\na.txt\nsub/\nz.go\n"
	if a.Text != want {
		t.Errorf("Text = %q, want %q", a.Text, want)
	}
	if len(a.Media) != 0 {
		t.Errorf("Media = %v, want none for a directory", a.Media)
	}
}

func TestResolveMissingPath(t *testing.T) {
	cwd := t.TempDir()
	_, err := Resolve(cwd, "nope.txt", DefaultMediaConfig())
	if err == nil || !strings.Contains(err.Error(), "attachment nope.txt") {
		t.Fatalf("err = %v, want attachment nope.txt: ...", err)
	}
}

func TestResolveFileByteCap(t *testing.T) {
	cwd := t.TempDir()
	mustWriteFile(t, filepath.Join(cwd, "notes.txt"), fixtureBody)
	mc := DefaultMediaConfig()
	mc.MaxFileBytes = 4

	_, err := Resolve(cwd, "notes.txt", mc)
	if err == nil || !strings.Contains(err.Error(), "notes.txt") ||
		!strings.Contains(err.Error(), "exceeds 4 byte cap") {
		t.Fatalf("err = %v, want per-item byte cap error", err)
	}
}

func TestResolveDataURIText(t *testing.T) {
	for _, ref := range []string{
		"data:,hello world",
		"data:text/plain;base64," + b64([]byte("hello world")),
	} {
		a, err := Resolve("", ref, DefaultMediaConfig())
		if err != nil {
			t.Fatalf("Resolve(%q): %v", ref, err)
		}
		if a.Name != "data" || a.Text != "--- data ---\nhello world" {
			t.Errorf("%q: got name %q text %q", ref, a.Name, a.Text)
		}
		if len(a.Media) != 0 {
			t.Errorf("%q: Media = %v, want none for text", ref, a.Media)
		}
	}

	// HTML stays on the text path (content-sniffed as text/*).
	a, err := Resolve("", "data:text/html,<html><body>x</body></html>", DefaultMediaConfig())
	if err != nil {
		t.Fatal(err)
	}
	if want := "--- data ---\n<html><body>x</body></html>"; a.Text != want {
		t.Errorf("Text = %q, want %q", a.Text, want)
	}
}

func TestResolveDataURIErrors(t *testing.T) {
	if _, err := Resolve("", "data:text/plain", DefaultMediaConfig()); err == nil ||
		!strings.Contains(err.Error(), "malformed data URI") {
		t.Errorf("no-comma URI: err = %v, want malformed data URI", err)
	}
	if _, err := Resolve("", "data:image/png;base64,!!!!", DefaultMediaConfig()); err == nil ||
		!strings.Contains(err.Error(), "bad base64") {
		t.Errorf("bad payload: err = %v, want bad base64", err)
	}

	mc := DefaultMediaConfig()
	mc.MaxFileBytes = 4
	_, err := Resolve("", "data:,hello world", mc)
	if err == nil || !strings.Contains(err.Error(), "11 bytes exceeds 4 cap") {
		t.Errorf("err = %v, want size cap error", err)
	}
}

func TestResolveImageFileMedia(t *testing.T) {
	cwd := t.TempDir()
	src := pngFixture(t, 40, 20)
	mustWriteFile(t, filepath.Join(cwd, "img.png"), string(src))

	// AutoResize off: payload is the untouched file, base64-encoded.
	off := MediaConfig{AutoResize: false, MaxBase64Bytes: 5 << 20, MaxFileBytes: 20 << 20}
	a, err := Resolve(cwd, "img.png", off)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Media) != 1 {
		t.Fatalf("Media = %v, want exactly one item", a.Media)
	}
	if a.Media[0].Type != "image/png" || a.Media[0].Name != "img.png" {
		t.Errorf("media = %+v, want image/png img.png", a.Media[0])
	}
	if a.Media[0].Data != b64(src) {
		t.Error("payload must be the exact file bytes when AutoResize is off")
	}
	if a.Text != "" {
		t.Errorf("Text = %q, want empty for media", a.Text)
	}

	// AutoResize on but the image is within maxDim: unchanged.
	a, err = Resolve(cwd, "img.png", DefaultMediaConfig())
	if err != nil {
		t.Fatal(err)
	}
	if a.Media[0].Data != b64(src) {
		t.Error("small image must pass through untouched")
	}
}

func TestAutoResizeScalesLargeImage(t *testing.T) {
	cwd := t.TempDir()
	src := pngFixture(t, 2400, 800)
	mustWriteFile(t, filepath.Join(cwd, "big.png"), string(src))

	a, err := Resolve(cwd, "big.png", DefaultMediaConfig())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.StdEncoding.DecodeString(a.Media[0].Data)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("resized payload must still decode as PNG: %v", err)
	}
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	if w > 2000 || h > 2000 {
		t.Errorf("resized bounds = %dx%d, longest edge must be <= 2000", w, h)
	}
	if w >= 2400 || h >= 800 {
		t.Errorf("resized bounds = %dx%d, want strictly smaller than 2400x800", w, h)
	}

	// AutoResize off keeps the original bytes even above maxDim.
	off := MediaConfig{AutoResize: false, MaxBase64Bytes: 5 << 20, MaxFileBytes: 20 << 20}
	b, err := Resolve(cwd, "big.png", off)
	if err != nil {
		t.Fatal(err)
	}
	if b.Media[0].Data != b64(src) {
		t.Error("AutoResize off must not touch the payload")
	}
}

func TestResolvePDFMedia(t *testing.T) {
	cwd := t.TempDir()
	mustWriteFile(t, filepath.Join(cwd, "doc.pdf"), pdfFixture)

	a, err := Resolve(cwd, "doc.pdf", DefaultMediaConfig())
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Media) != 1 || a.Media[0].Type != "application/pdf" {
		t.Fatalf("Media = %+v, want one application/pdf", a.Media)
	}
	if a.Media[0].Name != "doc.pdf" {
		t.Errorf("Name = %q, want doc.pdf", a.Media[0].Name)
	}
	// PDFs never decode as images, so AutoResize passes them through.
	if a.Media[0].Data != b64([]byte(pdfFixture)) {
		t.Error("PDF payload must be untouched")
	}
}

func TestDataURIMediaSniffsContent(t *testing.T) {
	pngSrc := pngFixture(t, 10, 10)

	// Declared text/plain but the bytes are a PNG: sniffing wins.
	a, err := Resolve("", "data:text/plain;base64,"+b64(pngSrc), DefaultMediaConfig())
	if err != nil {
		t.Fatal(err)
	}
	if a.Name != "data.png" || len(a.Media) != 1 || a.Media[0].Type != "image/png" {
		t.Fatalf("name %q media %+v, want data.png image/png", a.Name, a.Media)
	}
	if a.Media[0].Data != b64(pngSrc) {
		t.Error("tiny image payload must pass through")
	}

	// Explicit PDF data URI becomes media with an invented .pdf name.
	p, err := Resolve("", "data:application/pdf;base64,"+b64([]byte(pdfFixture)), DefaultMediaConfig())
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "data.pdf" || len(p.Media) != 1 || p.Media[0].Type != "application/pdf" {
		t.Fatalf("name %q media %+v, want data.pdf application/pdf", p.Name, p.Media)
	}
}

func TestMediaBase64Cap(t *testing.T) {
	pngSrc := pngFixture(t, 10, 10)
	ref := "data:image/png;base64," + b64(pngSrc)

	mc := MediaConfig{AutoResize: false, MaxBase64Bytes: 8, MaxFileBytes: 1 << 20}
	_, err := Resolve("", ref, mc)
	if err == nil || !strings.Contains(err.Error(), "max_base64_bytes 8") {
		t.Fatalf("err = %v, want max_base64_bytes cap error", err)
	}

	// Zero disables the base64 cap.
	mc.MaxBase64Bytes = 0
	a, err := Resolve("", ref, mc)
	if err != nil {
		t.Fatalf("unlimited base64 cap: %v", err)
	}
	if len(a.Media) != 1 {
		t.Fatalf("Media = %v, want one item", a.Media)
	}
}
