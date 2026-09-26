// Package attach resolves prompt attachments: file:// URIs with optional
// ?start=&end= line ranges, plain paths, directories (non-recursive listing),
// and data: base64 URIs, honoring the [media] config for images.
package attach

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	xdraw "golang.org/x/image/draw"

	"matcode/internal/store"
)

// MediaConfig mirrors the [media] config table.
type MediaConfig struct {
	AutoResize     bool
	MaxBase64Bytes int
	MaxFileBytes   int
}

// Defaults matches OpenCode: resize big images, 5 MiB base64, 20 MiB per item.
func DefaultMediaConfig() MediaConfig {
	return MediaConfig{AutoResize: true, MaxBase64Bytes: 5 << 20, MaxFileBytes: 20 << 20}
}

// maxDim is the longest edge images are scaled to when AutoResize is on.
const maxDim = 2000

// Attachment is one resolved prompt input: text (files, listings) and/or
// media (images/PDF) for the user message.
type Attachment struct {
	Name  string
	Text  string
	Media []store.Media
}

// Resolve turns one reference into an attachment. References:
//
//	file:///abs/path?start=3&end=10 — file with 1-based inclusive line range
//	<relative or absolute path>     — file or directory (listing)
//	data:<mime>;base64,<payload>    — inline media or text
func Resolve(cwd, ref string, mc MediaConfig) (Attachment, error) {
	if strings.HasPrefix(ref, "data:") {
		return fromDataURI(ref, mc)
	}
	path, query := ref, ""
	if p, ok := strings.CutPrefix(ref, "file://"); ok {
		path = p
		if i := strings.IndexByte(path, '?'); i >= 0 {
			path, query = path[:i], path[i+1:]
		}
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(cwd, path)
	}
	st, err := os.Stat(path)
	if err != nil {
		return Attachment{}, fmt.Errorf("attachment %s: %w", ref, err)
	}
	name := filepath.Base(path)
	if st.IsDir() {
		return directory(path, name)
	}
	b, err := readFile(path, mc.MaxFileBytes)
	if err != nil {
		return Attachment{}, err
	}
	if isMedia(b) {
		m, err := media(name, b, mc)
		if err != nil {
			return Attachment{}, err
		}
		return Attachment{Name: name, Media: m}, nil
	}
	start, end := parseRange(query)
	return Attachment{Name: name, Text: namedText(name, sliceLines(string(b), start, end), start, end)}, nil
}

// directory lists a folder non-recursively: dirs get a trailing slash.
func directory(path, name string) (Attachment, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return Attachment{}, fmt.Errorf("attachment %s: %w", path, err)
	}
	var sb strings.Builder
	for _, e := range entries {
		sb.WriteString(e.Name())
		if e.IsDir() {
			sb.WriteString("/")
		}
		sb.WriteString("\n")
	}
	return Attachment{Name: name, Text: namedText(name, sb.String(), 0, 0)}, nil
}

// fromDataURI decodes data:<mime>[;base64],<payload>.
func fromDataURI(ref string, mc MediaConfig) (Attachment, error) {
	rest := strings.TrimPrefix(ref, "data:")
	i := strings.IndexByte(rest, ',')
	if i < 0 {
		return Attachment{}, fmt.Errorf("attachment: malformed data URI (no comma)")
	}
	meta, payload := rest[:i], rest[i+1:]
	typ, isB64 := "text/plain", false
	for _, p := range strings.Split(meta, ";") {
		if p == "base64" {
			isB64 = true
		} else if strings.Contains(p, "/") {
			typ = p
		}
	}
	var b []byte
	var err error
	if isB64 {
		if b, err = base64.StdEncoding.DecodeString(payload); err != nil {
			return Attachment{}, fmt.Errorf("attachment: bad base64: %w", err)
		}
	} else {
		b = []byte(payload)
	}
	if len(b) > mc.MaxFileBytes {
		return Attachment{}, fmt.Errorf("attachment: %d bytes exceeds %d cap", len(b), mc.MaxFileBytes)
	}
	if !isMedia(b) {
		typ = http.DetectContentType(b)
		if strings.HasPrefix(typ, "text/") || typ == "" {
			return Attachment{Name: "data", Text: namedText("data", string(b), 0, 0)}, nil
		}
	}
	if !strings.HasPrefix(typ, "image/") && typ != "application/pdf" {
		typ = http.DetectContentType(b)
	}
	name := dataName(typ)
	m, err := media(name, b, mc)
	if err != nil {
		return Attachment{}, err
	}
	return Attachment{Name: name, Media: m}, nil
}

// media base64-encodes b as one store.Media, resizing images first.
func media(name string, b []byte, mc MediaConfig) ([]store.Media, error) {
	if len(b) > mc.MaxFileBytes {
		return nil, fmt.Errorf("attachment %s: %d bytes exceeds %d cap", name, len(b), mc.MaxFileBytes)
	}
	typ := http.DetectContentType(b)
	if b2, err := resize(b, mc); err == nil {
		b = b2
	}
	payload := base64.StdEncoding.EncodeToString(b)
	if mc.MaxBase64Bytes > 0 && len(payload) > mc.MaxBase64Bytes {
		return nil, fmt.Errorf("attachment %s: base64 size %d exceeds media.max_base64_bytes %d", name, len(payload), mc.MaxBase64Bytes)
	}
	return []store.Media{{Type: typ, Data: payload, Name: name}}, nil
}

// resize downscales PNG/JPEG images beyond maxDim to fit; other bytes pass
// through unchanged (GIF animations, PDFs, undecodable data).
func resize(b []byte, mc MediaConfig) ([]byte, error) {
	if !mc.AutoResize {
		return b, nil
	}
	img, format, err := image.Decode(bytes.NewReader(b))
	if err != nil || (format != "png" && format != "jpeg") {
		return b, nil
	}
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	if w <= maxDim && h <= maxDim {
		return b, nil
	}
	scale := float64(maxDim) / float64(max(w, h))
	nw, nh := int(float64(w)*scale), int(float64(h)*scale)
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), img, img.Bounds(), xdraw.Over, nil)
	var buf bytes.Buffer
	if format == "png" {
		if err := png.Encode(&buf, dst); err != nil {
			return b, err
		}
	} else {
		if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 85}); err != nil {
			return b, err
		}
	}
	return buf.Bytes(), nil
}

// namedText wraps content with a filename header so the model knows what
// it is looking at.
func namedText(name, content string, start, end int) string {
	head := name
	if start > 0 {
		head = fmt.Sprintf("%s (lines %d-%d)", name, start, end)
	}
	return fmt.Sprintf("--- %s ---\n%s", head, content)
}

// sliceLines extracts 1-based inclusive lines; 0 = whole file.
func sliceLines(s string, start, end int) string {
	if start <= 0 && end <= 0 {
		return s
	}
	lines := strings.Split(s, "\n")
	if start <= 0 {
		start = 1
	}
	if end <= 0 || end > len(lines) {
		end = len(lines)
	}
	if start > len(lines) {
		return ""
	}
	return strings.Join(lines[start-1:end], "\n")
}

// parseRange reads ?start=&end= (1-based, inclusive).
func parseRange(query string) (int, int) {
	vals := map[string]int{}
	for _, kv := range strings.Split(query, "&") {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if n, err := strconv.Atoi(v); err == nil {
			vals[k] = n
		}
	}
	return vals["start"], vals["end"]
}

// isMedia sniffs images and PDFs regardless of extension.
func isMedia(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	if len(b) > 512 {
		b = b[:512]
	}
	t := http.DetectContentType(b)
	return strings.HasPrefix(t, "image/") || t == "application/pdf"
}

// readFile loads a file under the per-item byte cap.
func readFile(path string, cap int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if cap <= 0 {
		return io.ReadAll(f)
	}
	b, err := io.ReadAll(io.LimitReader(f, int64(cap)+1))
	if err != nil {
		return nil, err
	}
	if len(b) > cap {
		return nil, fmt.Errorf("attachment %s: exceeds %d byte cap", filepath.Base(path), cap)
	}
	return b, nil
}

// dataName invents a filename for data: URIs from their mime type.
func dataName(typ string) string {
	if ext, err := mime.ExtensionsByType(typ); err == nil && len(ext) > 0 {
		return "data" + ext[0]
	}
	return "data"
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
