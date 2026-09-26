package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"matcode/internal/engine"
	"matcode/internal/skills"
)

// Skill loads one skill's instructions into the conversation. Registration
// is conditional: with an empty data tree there is nothing to load, so the
// model never sees a tool it cannot use.
func Skill(set *skills.Set) engine.Tool {
	return engine.Tool{
		ID:          "skill",
		Description: skillDescription(set),
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{
					"type":        "string",
					"description": "Exact skill ID from the available skills list.",
				},
			},
			"required": []string{"id"},
		},
		Execute: func(ctx context.Context, input json.RawMessage) (engine.Result, error) {
			var in struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(input, &in); err != nil {
				return engine.Result{}, err
			}
			id := strings.TrimSpace(in.ID)
			sk, ok := set.Get(id)
			if !ok {
				return engine.Result{}, fmt.Errorf("unknown skill %q (available: %s)",
					id, strings.Join(set.IDs(), ", "))
			}
			set.MarkLoaded(id)
			return engine.Result{Text: skillBody(sk)}, nil
		},
	}
}

// skillDescription advertises every loadable skill. The model picks by ID;
// only ID, name and description are ever sent.
func skillDescription(set *skills.Set) string {
	head := "Load a specialized skill's instructions and bundled resources into the conversation " +
		"by exact ID. A skill holds task-specific instructions; its supporting files are listed, " +
		"not read, until the skill directs you to them."
	if block := set.Advertise(); block != "" {
		return head + "\n\n" + block
	}
	return head + "\nNo skills are configured in the data tree."
}

// skillBody is what the model reads back: the definition without its
// frontmatter, plus the base directory and a sample of supporting files.
func skillBody(sk skills.Skill) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(sk.Body))
	b.WriteString("\n\nBase directory: " + sk.Dir)
	if len(sk.Files) == 0 {
		return b.String()
	}
	b.WriteString("\nSupporting files (relative to the base directory, not loaded automatically):")
	for _, f := range sk.Files {
		b.WriteString("\n- " + f)
	}
	return b.String()
}
