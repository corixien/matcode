package agents

import (
	"testing"

	"matcode/internal/engine"
)

// hasID reports whether a tool list contains id, returning the entry.
func toolByID(list []engine.Tool, id string) (engine.Tool, bool) {
	for _, t := range list {
		if t.ID == id {
			return t, true
		}
	}
	return engine.Tool{}, false
}

// TestToolsAllowlistCoversUserTools: data-tree tools join the same
// registry the allowlist filters, so "*" (build) sees them and an agent
// with an explicit list does not (row 33).
func TestToolsAllowlistCoversUserTools(t *testing.T) {
	user := []engine.Tool{{ID: "probe", Description: "data tree"}}

	if got := build.Tools("/tmp", "", false, nil, user); !hasTool(got, "probe") {
		t.Errorf("build (tools=*) must see the user tool; have %v", ids(got))
	}
	if got := explore.Tools("/tmp", "", false, nil, user); hasTool(got, "probe") {
		t.Errorf("explore has an explicit list; must not see the user tool: %v", ids(got))
	}
	if got := plan.Tools("/tmp", "", false, nil, user); hasTool(got, "probe") {
		t.Errorf("plan must not see the user tool: %v", ids(got))
	}
}

// TestUserToolShadowsBuiltin: last registration wins, and the allowlist is
// checked against the shadowed id — an agent allowed to `read` gets the
// data-tree read instead of the builtin one.
func TestUserToolShadowsBuiltin(t *testing.T) {
	user := []engine.Tool{{ID: "read", Description: "mine"}}

	got := explore.Tools("/tmp", "", false, nil, user)
	if len(got) != 5 {
		t.Fatalf("explore tools = %v, want its usual five", ids(got))
	}
	if t0, _ := toolByID(got, "read"); t0.Description != "mine" {
		t.Errorf("read came from %q, want the user tool to shadow it", t0.Description)
	}
	// An id nobody granted still stays out.
	if hasTool(explore.Tools("/tmp", "", false, nil,
		[]engine.Tool{{ID: "probe"}}), "probe") {
		t.Error("unlisted ids must stay filtered after the merge")
	}
}

func hasTool(list []engine.Tool, id string) bool {
	_, ok := toolByID(list, id)
	return ok
}

func ids(list []engine.Tool) []string {
	out := make([]string, 0, len(list))
	for _, t := range list {
		out = append(out, t.ID)
	}
	return out
}
