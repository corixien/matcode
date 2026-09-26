package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"matcode/internal/engine"
)

// Todo persists a task list to the data tree so the TUI (and any later run)
// can read it back — no hidden in-memory state.
func Todo(workdir string) engine.Tool {
	return engine.Tool{
		ID:          "todowrite",
		Description: "Save the current task list to the project data tree.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"todos": map[string]any{
					"type":  "array",
					"items": todoSchema(),
				},
			},
			"required": []string{"todos"},
		},
		Execute: func(ctx context.Context, input json.RawMessage) (engine.Result, error) {
			var in struct {
				Todos []todo `json:"todos"`
			}
			if err := json.Unmarshal(input, &in); err != nil {
				return engine.Result{}, err
			}
			b, err := json.MarshalIndent(in.Todos, "", "  ")
			if err != nil {
				return engine.Result{}, err
			}
			p := filepath.Join(workdir, "todos.json")
			if err := os.WriteFile(p, append(b, '\n'), 0o644); err != nil {
				return engine.Result{}, err
			}
			return engine.Result{Text: fmt.Sprintf("saved %d todos", len(in.Todos))}, nil
		},
	}
}

type todo struct {
	Content    string `json:"content"`
	Status     string `json:"status"` // pending | in_progress | completed
	ActiveForm string `json:"active_form,omitempty"`
}

func todoSchema() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{"content": map[string]any{"type": "string"}},
		"required":   []string{"content"},
	}
}
