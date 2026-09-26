package tools

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"

	"matcode/internal/engine"
)

// Glob returns file paths matching a pattern, relative to the workdir.
func Glob(workdir string) engine.Tool {
	return engine.Tool{
		ID:          "glob",
		Description: "Find file paths matching a glob pattern.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{"type": "string", "description": "Glob pattern, e.g. **/*.go"},
			},
			"required": []string{"pattern"},
		},
		Execute: func(ctx context.Context, input json.RawMessage) (engine.Result, error) {
			var in struct {
				Pattern string `json:"pattern"`
			}
			if err := json.Unmarshal(input, &in); err != nil {
				return engine.Result{}, err
			}
			matches, err := filepath.Glob(resolve(workdir, in.Pattern))
			if err != nil {
				return engine.Result{}, err
			}
			var rel []string
			for _, m := range matches {
				if r, err := filepath.Rel(workdir, m); err == nil {
					rel = append(rel, r)
				} else {
					rel = append(rel, m)
				}
				if len(rel) >= 200 {
					break
				}
			}
			if len(rel) == 0 {
				return engine.Result{Text: "no matches"}, nil
			}
			return engine.Result{Text: strings.Join(rel, "\n")}, nil
		},
	}
}
