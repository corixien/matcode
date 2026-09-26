package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"matcode/internal/engine"
)

// Page caps for text reads: one read answers at most this much, the model
// pages on with offset (opencode parity).
const (
	readMaxLines = 2000
	readMaxBytes = 50 * 1024
	// mediaMaxBytes is the size cap for images and PDFs read as attachments.
	mediaMaxBytes = 20 * 1024 * 1024
)

// Read returns a file's contents: images and PDFs ride as base64 media for
// vision models, text is paged by line offset with a byte cap.
func Read(workdir string) engine.Tool {
	return engine.Tool{
		ID:          "read",
		Description: "Read a text file (paged by lines) or attach an image/PDF for viewing.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":   map[string]any{"type": "string", "description": "File path."},
				"offset": map[string]any{"type": "integer", "description": "1-based line to start reading at (default 1)."},
				"limit":  map[string]any{"type": "integer", "description": "Max lines to return (default 2000, cap 50KB)."},
			},
			"required": []string{"path"},
		},
		Execute: func(ctx context.Context, input json.RawMessage) (engine.Result, error) {
			var in struct {
				Path   string `json:"path"`
				Offset int    `json:"offset"`
				Limit  int    `json:"limit"`
			}
			if err := json.Unmarshal(input, &in); err != nil {
				return engine.Result{}, err
			}
			p := resolve(workdir, in.Path)
			b, err := os.ReadFile(p)
			if err != nil {
				return engine.Result{}, err
			}
			// Sniff the real content type; mime by extension alone lies
			// (text files named .png must not ship as images).
			mime := http.DetectContentType(b)
			if strings.HasPrefix(mime, "image/") || mime == "application/pdf" {
				return readMedia(in.Path, mime, b)
			}
			return readText(string(b), in.Offset, in.Limit), nil
		},
	}
}

// readMedia base64s an image/PDF attachment, enforcing the size cap.
func readMedia(path, mime string, b []byte) (engine.Result, error) {
	if len(b) > mediaMaxBytes {
		return engine.Result{}, fmt.Errorf("%s is %d bytes; media attachments cap at 20MiB", path, len(b))
	}
	name := path
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		name = name[i+1:]
	}
	return engine.Result{
		Text: fmt.Sprintf("attached %s (%s, %d bytes)", mime, name, len(b)),
		Media: []engine.Media{{
			Type: mime,
			Data: base64.StdEncoding.EncodeToString(b),
			Name: name,
		}},
	}, nil
}

// readText pages a text file: skip to offset, take limit lines, and stop at
// the byte cap — the footer tells the model where to continue.
func readText(content string, offset, limit int) engine.Result {
	if offset < 1 {
		offset = 1
	}
	if limit <= 0 || limit > readMaxLines {
		limit = readMaxLines
	}
	lines := strings.Split(content, "\n")
	start := offset - 1
	if start > len(lines) {
		return engine.Result{Text: fmt.Sprintf("offset %d is past the end (%d lines)", offset, len(lines))}
	}
	end := start + limit
	if end > len(lines) {
		end = len(lines)
	}
	page := lines[start:end]
	truncated := false
	// Byte cap: cut the page where it passes 50KB, at a line boundary.
	total := 0
	for i, l := range page {
		total += len(l) + 1
		if total > readMaxBytes {
			page = page[:i]
			truncated = true
			break
		}
	}
	out := strings.Join(page, "\n")
	if start+len(page) < len(lines) || truncated {
		out += fmt.Sprintf("\n[page ends at line %d of %d; continue with offset=%d]",
			start+len(page), len(lines), start+len(page)+1)
	}
	return engine.Result{Text: out}
}
