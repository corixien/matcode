package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"matcode/internal/engine"
)

// Patch applies a unified diff (git style) to one file. Hunks are verified
// against the file before splicing: a wrong context is an error, not a guess.
func Patch(workdir string, formatOnWrite bool) engine.Tool {
	return engine.Tool{
		ID:          "patch",
		Description: "Apply a unified diff to a file.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string", "description": "File to patch."},
				"diff": map[string]any{"type": "string", "description": "Unified diff with @@ hunks."},
			},
			"required": []string{"path", "diff"},
		},
		Execute: func(ctx context.Context, input json.RawMessage) (engine.Result, error) {
			var in struct {
				Path string `json:"path"`
				Diff string `json:"diff"`
			}
			if err := json.Unmarshal(input, &in); err != nil {
				return engine.Result{}, err
			}
			p := resolve(workdir, in.Path)
			b, err := os.ReadFile(p)
			if err != nil {
				return engine.Result{}, err
			}
			out, err := applyDiff(string(b), in.Diff)
			if err != nil {
				return engine.Result{}, fmt.Errorf("%s: %w", in.Path, err)
			}
			if err := os.WriteFile(p, []byte(out), 0o644); err != nil {
				return engine.Result{}, err
			}
			format(workdir, formatOnWrite, p)
			return engine.Result{Text: fmt.Sprintf("patched %s", in.Path)}, nil
		},
	}
}

// applyDiff splices every hunk of a single-file unified diff into content.
func applyDiff(content, diff string) (string, error) {
	lines := strings.Split(content, "\n")
	var out []string
	pos := 0 // cursor into lines

	dl := strings.Split(diff, "\n")
	for i := 0; i < len(dl); i++ {
		if !strings.HasPrefix(dl[i], "@@") {
			continue // ---/+++ headers, index lines
		}
		oldStart, err := hunkStart(dl[i])
		if err != nil {
			return "", err
		}
		var old, neu []string
		for i++; i < len(dl) && !strings.HasPrefix(dl[i], "@@") &&
			!strings.HasPrefix(dl[i], "---") && !strings.HasPrefix(dl[i], "+++"); i++ {
			l := dl[i]
			if strings.HasPrefix(l, "\\") { // "\ No newline at end of file"
				continue
			}
			if l == "" { // some tools drop the context-space prefix
				l = " "
			}
			switch l[0] {
			case ' ':
				old = append(old, l[1:])
				neu = append(neu, l[1:]) // context is verified AND kept
			case '-':
				old = append(old, l[1:])
			case '+':
				neu = append(neu, l[1:])
			}
		}
		i-- // the loop advanced past the hunk; outer i++ moves on

		start := oldStart - 1
		if start < pos || start+len(old) > len(lines) {
			return "", fmt.Errorf("hunk at line %d does not fit the file", oldStart)
		}
		for j := range old {
			if lines[start+j] != old[j] {
				return "", fmt.Errorf("hunk context mismatch at line %d", start+j+1)
			}
		}
		out = append(out, lines[pos:start]...)
		out = append(out, neu...)
		pos = start + len(old)
	}
	if pos == 0 {
		return "", fmt.Errorf("no @@ hunks in diff")
	}
	out = append(out, lines[pos:]...)
	return strings.Join(out, "\n"), nil
}

// hunkStart parses the 1-based start line of "@@ -a,b +c,d @@".
func hunkStart(header string) (int, error) {
	f := strings.Fields(header)
	if len(f) < 2 || !strings.HasPrefix(f[1], "-") {
		return 0, fmt.Errorf("bad hunk header %q", header)
	}
	span := strings.TrimPrefix(f[1], "-")
	if i := strings.Index(span, ","); i >= 0 {
		span = span[:i]
	}
	n, err := strconv.Atoi(span)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("bad hunk header %q", header)
	}
	return n, nil
}
