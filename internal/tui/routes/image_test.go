package routes

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"matcode/internal/store"
	"matcode/internal/tui/theme"
)

// envMap adapts a map to the env lookup DetectImageProtocol takes.
func envMap(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

// TestDetectImageProtocol pins the capability table: explicit
// MTC_IMAGE override first, then terminal markers, tmux/screen and bare
// xterm falling through to none (row 35).
func TestDetectImageProtocol(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want ImageProtocol
	}{
		{"override kitty", map[string]string{"MTC_IMAGE": "kitty"}, ImageKitty},
		{"override iterm alias", map[string]string{"MTC_IMAGE": "iterm"}, ImageITerm2},
		{"override none beats term", map[string]string{"MTC_IMAGE": "none", "TERM": "xterm-kitty"}, ImageNone},
		{"kitty window id", map[string]string{"KITTY_WINDOW_ID": "3"}, ImageKitty},
		{"term_program kitty", map[string]string{"TERM_PROGRAM": "kitty"}, ImageKitty},
		{"term_program ghostty", map[string]string{"TERM_PROGRAM": "Ghostty"}, ImageKitty},
		{"term_program iterm", map[string]string{"TERM_PROGRAM": "iTerm.app"}, ImageITerm2},
		{"term_program wezterm", map[string]string{"TERM_PROGRAM": "WezTerm"}, ImageITerm2},
		{"lc terminal", map[string]string{"LC_TERMINAL": "iTerm2"}, ImageITerm2},
		{"term kitty", map[string]string{"TERM": "xterm-kitty"}, ImageKitty},
		{"term sixel", map[string]string{"TERM": "xterm-256color-sixel"}, ImageSixel},
		{"tmux falls through", map[string]string{"TERM": "tmux-256color"}, ImageNone},
		{"bare xterm", map[string]string{"TERM": "xterm-256color"}, ImageNone},
		{"empty env", map[string]string{}, ImageNone},
	}
	for _, c := range cases {
		if got := DetectImageProtocol(envMap(c.env)); got != c.want {
			t.Errorf("%s: protocol = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestImgCells proves pixel→cell conversion: ceiling division, the
// maxCols clamp, and the degenerate sizes.
func TestImgCells(t *testing.T) {
	cases := []struct {
		w, h, max, wantCols, wantRows int
	}{
		{100, 40, 80, 10, 2},  // 10 cols wide → 40px tall = 2 cells
		{1000, 40, 80, 80, 2}, // clamped to maxCols, aspect keeps 2 rows
		{1, 1, 80, 1, 1},      // tiny image still one cell
		{0, 0, 80, 1, 1},      // degenerate
		{20, 1000, 80, 2, 50}, // tall image: 1000px / 20px cell = 50 rows
	}
	for _, c := range cases {
		cols, rows := imgCells(c.w, c.h, c.max)
		if cols != c.wantCols || rows != c.wantRows {
			t.Errorf("imgCells(%d,%d,%d) = %d,%d want %d,%d",
				c.w, c.h, c.max, cols, rows, c.wantCols, c.wantRows)
		}
	}
}

// testPNG returns a w×h solid-color PNG and its base64 form.
func testPNG(t *testing.T, w, h int) ([]byte, string) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 40), G: uint8(y * 40), B: 128, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png encode: %v", err)
	}
	raw := buf.Bytes()
	return raw, base64.StdEncoding.EncodeToString(raw)
}

// TestMediaLinesPlaceholder: with no protocol the transcript must show
// a readable one-line placeholder carrying name, size, and type.
func TestMediaLinesPlaceholder(t *testing.T) {
	_, b64 := testPNG(t, 4, 4)
	m := NewMessageList(theme.Default, 80)
	m.Proto = ImageNone
	lines := m.mediaLines(store.Media{Type: "image/png", Name: "shot.png", Data: b64}, 80)
	if len(lines) != 1 {
		t.Fatalf("placeholder lines = %d, want 1", len(lines))
	}
	for _, want := range []string{"◇", "shot.png", "4x4", "image/png", "no image protocol"} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("placeholder missing %q: %q", want, lines[0])
		}
	}
	// Non-image media keeps its type; no decode attempted.
	lines = m.mediaLines(store.Media{Type: "application/pdf", Name: "doc.pdf", Data: "aGVsbG8="}, 80)
	if len(lines) != 1 || !strings.Contains(lines[0], "application/pdf") {
		t.Errorf("pdf placeholder = %v", lines)
	}
}

// TestMediaLinesEscapeProtocol: with a protocol the first line is the
// graphics escape and the tail pads the cell rows.
func TestMediaLinesEscapeProtocol(t *testing.T) {
	_, b64 := testPNG(t, 40, 40)
	m := NewMessageList(theme.Default, 80)
	m.Proto = ImageKitty
	lines := m.mediaLines(store.Media{Type: "image/png", Data: b64}, 80)
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "\x1b_Ga=T,f=24,") {
		t.Fatalf("first line not a kitty escape: %q", firstRunes(lines, 30))
	}
	if !strings.HasSuffix(lines[len(lines)-1], "\x1b\\") && len(lines) > 1 {
		// Padding lines are blank; the escape itself must terminate.
		if !strings.Contains(lines[0], "\x1b\\") {
			t.Errorf("kitty escape not terminated")
		}
	}
}

// TestProtocolEscapes pins each protocol's opening byte sequence on a
// synthetic image, so a regression in the writers is caught without a
// real terminal.
func TestProtocolEscapes(t *testing.T) {
	raw, _ := testPNG(t, 8, 8)
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	m := NewMessageList(theme.Default, 80)

	kitty, err := m.imageEscape(ImageKitty, raw, img, 4, 2)
	if err != nil || !strings.HasPrefix(kitty, "\x1b_Ga=T,f=24,") {
		t.Errorf("kitty escape = %q err=%v", cut(kitty, 30), err)
	}
	it, err := m.imageEscape(ImageITerm2, raw, img, 4, 2)
	if err != nil || !strings.HasPrefix(it, "\x1b]1337;File=inline=1;") {
		t.Errorf("iterm2 escape = %q err=%v", cut(it, 30), err)
	}
	sx, err := m.imageEscape(ImageSixel, raw, img, 4, 2)
	if err != nil || !strings.HasPrefix(sx, "\x1bPq") {
		t.Errorf("sixel escape = %q err=%v", cut(sx, 30), err)
	}
	if _, err := m.imageEscape(ImageNone, raw, img, 4, 2); err == nil {
		t.Error("none protocol should error, not render")
	}
}

// cut is a short prefix for failure messages.
func cut(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// firstRunes joins the first line prefixes for failure messages.
func firstRunes(lines []string, n int) string {
	if len(lines) == 0 {
		return "<none>"
	}
	return cut(lines[0], n)
}
