package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"matcode/internal/engine"
)

// stdin is one reader for the process: a fresh bufio.Reader per question
// would swallow the remaining lines into its buffer and starve the next call.
var stdin = bufio.NewReader(os.Stdin)

// Question asks the terminal user something and returns their answer.
// Options are optional — empty options means free-form text. Headless runs
// without stdin fail loudly instead of guessing.
func Question(workdir string) engine.Tool {
	return engine.Tool{
		ID:          "question",
		Description: "Ask the user a question and return their answer.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"question": map[string]any{"type": "string", "description": "The question to ask."},
				"header":   map[string]any{"type": "string", "description": "Short label shown above the question."},
				"options": map[string]any{
					"type":        "array",
					"description": "Optional answer options to choose from. Omit for a free-form answer.",
					"items":       map[string]any{"type": "string"},
				},
				"multiple": map[string]any{
					"type":        "boolean",
					"description": "Allow selecting several options (comma- or space-separated indices, or free text).",
				},
			},
			"required": []string{"question"},
		},
		Execute: func(ctx context.Context, input json.RawMessage) (engine.Result, error) {
			var in struct {
				Question string   `json:"question"`
				Header   string   `json:"header"`
				Options  []string `json:"options"`
				Multiple bool     `json:"multiple"`
			}
			if err := json.Unmarshal(input, &in); err != nil {
				return engine.Result{}, err
			}
			if in.Header != "" {
				fmt.Printf("[[ %s ]]\n", in.Header)
			}
			fmt.Printf("? %s\n", in.Question)
			for i, o := range in.Options {
				fmt.Printf("  %d) %s\n", i+1, o)
			}
			line, err := stdin.ReadString('\n')
			if err != nil && strings.TrimSpace(line) == "" {
				return engine.Result{}, fmt.Errorf("no answer available (stdin closed)")
			}
			ans := strings.TrimSpace(line)
			if ans == "" {
				return engine.Result{}, fmt.Errorf("empty answer")
			}
			if len(in.Options) == 0 {
				return engine.Result{Text: ans}, nil // free-form
			}
			if in.Multiple {
				return engine.Result{Text: resolveMultiple(ans, in.Options)}, nil
			}
			if n, err := strconv.Atoi(ans); err == nil && n >= 1 && n <= len(in.Options) {
				ans = in.Options[n-1]
			}
			return engine.Result{Text: ans}, nil
		},
	}
}

// resolveMultiple maps a multi-select answer — "1,3 5" — onto the chosen
// option texts; anything unparseable passes through as typed (free text).
func resolveMultiple(ans string, options []string) string {
	fields := strings.FieldsFunc(ans, func(r rune) bool { return r == ',' || r == ' ' || r == ';' })
	var picked []string
	for _, f := range fields {
		n, err := strconv.Atoi(f)
		if err != nil || n < 1 || n > len(options) {
			return ans // not indices — treat as a typed answer
		}
		picked = append(picked, options[n-1])
	}
	if len(picked) == 0 {
		return ans
	}
	return strings.Join(picked, ", ")
}
