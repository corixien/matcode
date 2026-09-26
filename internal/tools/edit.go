package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"matcode/internal/engine"
)

// Edit replaces an exact string in a file. The old string must be unique
// unless replace_all is set — ambiguity is an error, not a guess.
func Edit(workdir string, formatOnWrite bool) engine.Tool {
	return engine.Tool{
		ID:          "edit",
		Description: "Replace an exact string in a file.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":        map[string]any{"type": "string", "description": "File path."},
				"old":         map[string]any{"type": "string", "description": "Exact text to find."},
				"new":         map[string]any{"type": "string", "description": "Replacement text."},
				"replace_all": map[string]any{"type": "boolean", "description": "Replace every occurrence."},
			},
			"required": []string{"path", "old", "new"},
		},
		Execute: func(ctx context.Context, input json.RawMessage) (engine.Result, error) {
			var in struct {
				Path       string `json:"path"`
				Old        string `json:"old"`
				New        string `json:"new"`
				ReplaceAll bool   `json:"replace_all"`
			}
			if err := json.Unmarshal(input, &in); err != nil {
				return engine.Result{}, err
			}
			if in.Old == "" {
				return engine.Result{}, fmt.Errorf("old string is empty")
			}
			p := resolve(workdir, in.Path)
			b, err := os.ReadFile(p)
			if err != nil {
				return engine.Result{}, err
			}
			n := strings.Count(string(b), in.Old)
			switch {
			case n == 0:
				return engine.Result{}, fmt.Errorf("old string not found in %s", in.Path)
			case n > 1 && !in.ReplaceAll:
				return engine.Result{}, fmt.Errorf("old string occurs %d times in %s; add context or set replace_all", n, in.Path)
			}
			out := strings.Replace(string(b), in.Old, in.New, 1)
			if in.ReplaceAll {
				out = strings.ReplaceAll(string(b), in.Old, in.New)
			}
			if err := os.WriteFile(p, []byte(out), 0o644); err != nil {
				return engine.Result{}, err
			}
			format(workdir, formatOnWrite, p)
			return engine.Result{Text: fmt.Sprintf("edited %s (%d replacement(s))", in.Path, n)}, nil
		},
	}
}
