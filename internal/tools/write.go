package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"matcode/internal/engine"
)

// Write creates or overwrites a file with the given content.
func Write(workdir string, formatOnWrite bool) engine.Tool {
	return engine.Tool{
		ID:          "write",
		Description: "Write content to a file, creating parent directories as needed.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":    map[string]any{"type": "string", "description": "File path."},
				"content": map[string]any{"type": "string", "description": "Full file content."},
			},
			"required": []string{"path", "content"},
		},
		Execute: func(ctx context.Context, input json.RawMessage) (engine.Result, error) {
			var in struct {
				Path    string `json:"path"`
				Content string `json:"content"`
			}
			if err := json.Unmarshal(input, &in); err != nil {
				return engine.Result{}, err
			}
			p := resolve(workdir, in.Path)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return engine.Result{}, err
			}
			if err := os.WriteFile(p, []byte(in.Content), 0o644); err != nil {
				return engine.Result{}, err
			}
			format(workdir, formatOnWrite, p)
			return engine.Result{Text: fmt.Sprintf("wrote %d bytes to %s", len(in.Content), in.Path)}, nil
		},
	}
}
