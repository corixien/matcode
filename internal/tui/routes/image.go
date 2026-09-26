package routes

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/color/palette"
	"image/draw"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"strings"

	xdraw "golang.org/x/image/draw"

	"matcode/internal/store"
)

// ImageProtocol names a terminal inline-image protocol (row 35).
type ImageProtocol string

// The protocols the spec targets, plus none (placeholder text).
const (
	ImageNone   ImageProtocol = ""
	ImageKitty  ImageProtocol = "kitty"
	ImageSixel  ImageProtocol = "sixel"
	ImageITerm2 ImageProtocol = "iterm2"
)

// cellW/cellH are the assumed cell size in pixels when an image is
// converted to terminal cells (kitty/iterm2 have no size query we can
// do synchronously inside a frame render).
const (
	cellW = 10
	cellH = 20
)

// DetectImageProtocol picks the inline-image protocol from the terminal
// environment. MTC_IMAGE=kitty|sixel|iterm2|none forces an answer (and
// is what tests and unusual terminals use).
func DetectImageProtocol(env func(string) string) ImageProtocol {
	switch strings.ToLower(strings.TrimSpace(env("MTC_IMAGE"))) {
	case "kitty":
		return ImageKitty
	case "sixel":
		return ImageSixel
	case "iterm", "iterm2":
		return ImageITerm2
	case "none":
		return ImageNone
	}
	// Terminals advertising themselves first: tmux/screen set no
	// capability marker, so they fall through to none (their graphics
	// passthrough is off by default).
	if env("KITTY_WINDOW_ID") != "" {
		return ImageKitty
	}
	prog := strings.ToLower(env("TERM_PROGRAM"))
	term := strings.ToLower(env("TERM"))
	switch prog {
	case "kitty":
		return ImageKitty
	case "ghostty":
		return ImageKitty // ghostty speaks the kitty graphics protocol
	case "iterm.app":
		return ImageITerm2
	case "wezterm":
		return ImageITerm2 // wezterm implements the iterm2 inline protocol
	}
	if strings.EqualFold(env("LC_TERMINAL"), "iTerm2") {
		return ImageITerm2
	}
	switch {
	case strings.Contains(term, "kitty"):
		return ImageKitty
	case strings.Contains(term, "ghostty"):
		return ImageKitty
	case strings.Contains(term, "sixel"), strings.Contains(term, "mlterm"):
		return ImageSixel
	}
	return ImageNone
}

// imgCells scales pixel dimensions to terminal cells: cols wide (never
// over maxCols), rows tall at the assumed cell aspect ratio.
func imgCells(w, h, maxCols int) (cols, rows int) {
	if w <= 0 || h <= 0 {
		return 1, 1
	}
	cols = (w + cellW - 1) / cellW
	if cols > maxCols && maxCols > 0 {
		cols = maxCols
	}
	// The scaled image is cols*cellW pixels wide; its height in pixels
	// follows from the aspect ratio, then divides by the cell height.
	rows = (h*cols*cellW + w*cellH - 1) / (w * cellH)
	if rows < 1 {
		rows = 1
	}
	return cols, rows
}

// mediaLines renders one attachment into transcript lines: the protocol
// escape (when the terminal supports one) plus padding lines for the
// cells the image occupies, or a one-line placeholder. width bounds the
// image to the transcript column.
func (m *MessageList) mediaLines(md store.Media, width int) []string {
	if md.Data == "" {
		return nil
	}
	label := md.Name
	if label == "" {
		label = "attachment"
	}
	raw, err := base64.StdEncoding.DecodeString(md.Data)
	if err != nil {
		return []string{fg(m.Theme.Colors.Muted).Render("◇ " + label + " (unreadable media)")}
	}
	if !strings.HasPrefix(md.Type, "image/") {
		return []string{fg(m.Theme.Colors.Muted).Render("◇ " + label + " (" + md.Type + ")")}
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return []string{fg(m.Theme.Colors.Muted).Render("◇ " + label + " (" + md.Type + ")")}
	}
	b := img.Bounds()
	maxCols := width - 2
	if maxCols < 8 {
		maxCols = 8
	}
	cols, rows := imgCells(b.Dx(), b.Dy(), maxCols)
	if m.Proto == ImageNone {
		return []string{fg(m.Theme.Colors.Muted).Render(
			fmt.Sprintf("◇ %s (%dx%d, %s: no image protocol)", label, b.Dx(), b.Dy(), md.Type))}
	}
	esc, err := m.imageEscape(m.Proto, raw, img, cols, rows)
	if err != nil {
		return []string{fg(m.Theme.Colors.Muted).Render("◇ " + label + " (render failed)")}
	}
	// The escape line occupies `rows` terminal rows of cells; pad with
	// blank lines so the transcript viewport stays in step with what the
	// terminal actually draws.
	out := []string{esc}
	for i := 1; i < rows; i++ {
		out = append(out, "")
	}
	return out
}

// imageEscape builds the raw escape sequence for one protocol.
func (m *MessageList) imageEscape(p ImageProtocol, raw []byte, img image.Image, cols, rows int) (string, error) {
	switch p {
	case ImageITerm2:
		return iterm2Escape(raw, cols), nil
	case ImageKitty:
		return kittyEscape(img, cols, rows)
	case ImageSixel:
		return sixelEscape(img, cols, rows)
	}
	return "", fmt.Errorf("unknown image protocol %q", p)
}

// kittyEscape renders img with the kitty graphics protocol: pixel data
// chunked into 4 KiB base64 payloads (m=1 continuation, last m=0).
func kittyEscape(img image.Image, cols, rows int) (string, error) {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return "", fmt.Errorf("empty image")
	}
	// Downscale to the cell grid so the payload stays small.
	dst := image.NewRGBA(image.Rect(0, 0, cols*cellW, rows*cellH))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), img, b, xdraw.Over, nil)

	pix := make([]byte, 0, dst.Bounds().Dx()*dst.Bounds().Dy()*3)
	for y := dst.Bounds().Min.Y; y < dst.Bounds().Max.Y; y++ {
		for x := dst.Bounds().Min.X; x < dst.Bounds().Max.X; x++ {
			r, g, bl, _ := dst.At(x, y).RGBA()
			pix = append(pix, byte(r>>8), byte(g>>8), byte(bl>>8))
		}
	}
	payload := base64.StdEncoding.EncodeToString(pix)

	const chunk = 4096
	var b2 strings.Builder
	first := true
	for len(payload) > 0 || first {
		n := min(len(payload), chunk)
		part, rest := payload[:n], payload[n:]
		control := ""
		switch {
		case first && len(rest) > 0:
			control = fmt.Sprintf("a=T,f=24,s=%d,v=%d,c=%d,r=%d,m=1,", dst.Bounds().Dx(), dst.Bounds().Dy(), cols, rows)
		case first: // single chunk
			control = fmt.Sprintf("a=T,f=24,s=%d,v=%d,c=%d,r=%d,m=0,", dst.Bounds().Dx(), dst.Bounds().Dy(), cols, rows)
		case len(rest) > 0:
			control = "m=1,"
		default:
			control = "m=0,"
		}
		b2.WriteString("\x1b_G" + control + part + "\x1b\\")
		payload = rest
		first = false
	}
	return b2.String(), nil
}

// iterm2Escape renders raw image bytes with the iTerm2 inline-image
// OSC (also understood by WezTerm and several others).
func iterm2Escape(raw []byte, cols int) string {
	return "\x1b]1337;File=inline=1;width=" + fmt.Sprintf("%d", cols) +
		"C;preserveAspectRatio=1:" + base64.StdEncoding.EncodeToString(raw) + "\x07"
}

// sixelEscape renders img as a sixel sequence on the Plan9 palette.
func sixelEscape(img image.Image, cols, rows int) (string, error) {
	dst := image.NewRGBA(image.Rect(0, 0, cols*cellW, rows*cellH))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), img, img.Bounds(), xdraw.Over, nil)
	p := image.NewPaletted(dst.Bounds(), palette.Plan9)
	draw.FloydSteinberg.Draw(p, p.Bounds(), dst, dst.Bounds().Min)
	return "\x1bPq" + string(sixelEncode(p)) + "\x1b\\", nil
}

// sixelEncode encodes a paletted image as bare sixel data: raster
// attributes, then bands of six rows, one color register at a time with
// run-length encoding.
func sixelEncode(p *image.Paletted) []byte {
	b := p.Bounds()
	w, h := b.Dx(), b.Dy()
	var out bytes.Buffer
	fmt.Fprintf(&out, "\"1;1;%d;%d", w, h)
	for by := 0; by < h; by += 6 {
		// Color registers used in this band (ascending, deterministic).
		used := [256]bool{}
		for y := by; y < h && y < by+6; y++ {
			for x := 0; x < w; x++ {
				used[p.ColorIndexAt(b.Min.X+x, b.Min.Y+y)] = true
			}
		}
		for c := 0; c < 256; c++ {
			if !used[c] {
				continue
			}
			out.WriteString("$" + itoa(c))
			prev, run := byte('!'), 0
			emit := func(ch byte, n int) {
				switch {
				case n <= 3:
					out.WriteString(strings.Repeat(string(ch), n))
				default:
					fmt.Fprintf(&out, "!%d%s", n, string(ch))
				}
			}
			run = 0
			for x := 0; x < w; x++ {
				bits := byte(0)
				for dy := 0; dy < 6 && by+dy < h; dy++ {
					if p.ColorIndexAt(b.Min.X+x, b.Min.Y+by+dy) == uint8(c) {
						bits |= 1 << uint(dy)
					}
				}
				ch := byte(63 + bits)
				if ch == prev && run > 0 {
					run++
				} else {
					if run > 0 {
						emit(prev, run)
					}
					prev, run = ch, 1
				}
			}
			if run > 0 {
				emit(prev, run)
			}
		}
		out.WriteString("-")
	}
	return out.Bytes()
}

// itoa is strconv.Itoa for the color-register writes.
func itoa(n int) string { return fmt.Sprintf("%d", n) }
