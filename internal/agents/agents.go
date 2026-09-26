// Package agents defines the roster of agents mtc can run. Each agent fixes
// one role: its system prompt, tool allowlist, and permission behavior.
package agents

import (
	"matcode/internal/engine"
	"matcode/internal/skills"
	"matcode/internal/tools"
)

// Agent is one selectable role.
type Agent struct {
	ID     string
	System string
	// ToolIDs is the allowlist matched against registry tool ids (builtins
	// and data-tree tools alike): "*" grants every tool, an empty list
	// grants none.
	ToolIDs []string
	// UseInstructions prepends AGENTS.md (or the built-in default) to System.
	UseInstructions bool
	// BypassPermissions forces allow-everything: the build agent must never
	// ask for or deny tool access.
	BypassPermissions bool
	// Stateless runs a single completion without creating a session.
	Stateless bool
	// Model pins a model for this agent; "" inherits the session's model.
	Model string
	// Description is a one-line picker hint (frontmatter `description`).
	Description string
	// Steps caps tool rounds per turn (engine MaxRounds); 0 = default.
	Steps int
	// Hidden keeps the agent out of pickers and listings; it stays
	// addressable by id for sessions and `-agent`.
	Hidden bool
	// Disabled removes the agent from the roster entirely: Get fails.
	Disabled bool
	// Color is a display hint for pickers (6-hex or a theme color name).
	Color string
	// ReqHeaders/ReqBody are per-agent request overlays merged into every
	// provider payload this agent sends.
	ReqHeaders map[string]string
	ReqBody    map[string]any
}

// Tools resolves the allowlist against the merged registry (builtins plus
// the caller's data-tree tools), so a user tool is only offered to an
// agent whose ToolIDs grants it — "*" grants everything.
func (a Agent) Tools(cwd, dataDir string, format bool, set *skills.Set, user []engine.Tool) []engine.Tool {
	all := tools.Merge(tools.Builtin(cwd, dataDir, format, set), user)
	if len(a.ToolIDs) == 1 && a.ToolIDs[0] == "*" {
		return all
	}
	want := map[string]bool{}
	for _, id := range a.ToolIDs {
		want[id] = true
	}
	var out []engine.Tool
	for _, t := range all {
		if want[t.ID] {
			out = append(out, t)
		}
	}
	return out
}

// roster is the dispatch order; one var per agent file. Get and IDs live in
// overlay.go, where they also consult the `<dir>/*.md` registry.
var roster = []Agent{build, plan, explore, summary, title, compaction}

func join(ids []string) string {
	s := ""
	for i, id := range ids {
		if i > 0 {
			s += ", "
		}
		s += id
	}
	return s
}
