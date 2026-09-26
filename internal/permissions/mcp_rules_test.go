package permissions

import "testing"

// MCP tools reach the engine as `server__tool` (mcp.ToolName). These pin
// how permissions.toml addresses them: by that exact id, never by the bare
// tool name.

// TestMCPNamespacedActions covers rule matching on namespaced ids: a
// server-prefix glob, an exact id with an input pattern, another server's
// untouched id, and the star.
func TestMCPNamespacedActions(t *testing.T) {
	s := &Set{Default: Allow, Rules: []Rule{
		{Actions: []string{"gh__*"}, Pattern: "*", Decision: Deny},
		{Actions: []string{"slack__post"}, Pattern: "secret*", Decision: Ask},
	}}
	if got := s.Eval("gh__search", raw(`{"query":"repo:x"}`)); got != Deny {
		t.Errorf("gh__search = %q, want deny (server prefix glob)", got)
	}
	// The pattern matches the input's command-ish text (here: query).
	if got := s.Eval("slack__post", raw(`{"query":"secret channel"}`)); got != Ask {
		t.Errorf("slack__post secret = %q, want ask", got)
	}
	if got := s.Eval("slack__post", raw(`{"query":"standup notes"}`)); got != Allow {
		t.Errorf("slack__post plain = %q, want default allow (pattern miss)", got)
	}
	if got := s.Eval("git__status", raw(`{}`)); got != Allow {
		t.Errorf("git__status = %q, want the untouched default", got)
	}

	// A rule for the bare builtin id must not leak into the namespace.
	bare := &Set{Default: Deny, Rules: []Rule{
		{Actions: []string{"post"}, Pattern: "*", Decision: Allow},
	}}
	if got := bare.Eval("slack__post", raw(`{"query":"hi"}`)); got != Deny {
		t.Errorf("bare rule leaked: %q, want default deny", got)
	}

	// "*" spans the whole namespaced id.
	star := &Set{Default: Deny, Rules: []Rule{
		{Actions: []string{"*"}, Pattern: "*", Decision: Allow},
	}}
	if got := star.Eval("any__thing", raw(`{"query":"hi"}`)); got != Allow {
		t.Errorf("star action = %q, want allow", got)
	}
}

// TestMCPNamespacedEvalMatch pins the hit/miss contract the engine's
// decision() relies on: a matched namespaced rule answers directly, a miss
// reports matched=false so the caller can fall through to the default (or
// the plugin hook).
func TestMCPNamespacedEvalMatch(t *testing.T) {
	s := &Set{Default: Deny, Rules: []Rule{
		{Actions: []string{"gh__*"}, Pattern: "*", Decision: Allow},
	}}
	if d, matched := s.EvalMatch("gh__search", raw(`{"query":"x"}`)); !matched || d != Allow {
		t.Errorf("hit = (%q,%v), want (allow,true)", d, matched)
	}
	if d, matched := s.EvalMatch("git__status", raw(`{}`)); matched || d != "" {
		t.Errorf("miss = (%q,%v), want (\"\",false)", d, matched)
	}
	if got := s.Eval("git__status", raw(`{}`)); got != Deny {
		t.Errorf("Eval on miss = %q, want the default", got)
	}
}

// TestMCPNamespacedRawInput: an MCP input with none of the command-ish
// fields (command/query/prompt/url/path/pattern/id) falls back to the raw
// JSON as the matchable text, so a pattern can still reach it.
func TestMCPNamespacedRawInput(t *testing.T) {
	s := &Set{Default: Allow, Rules: []Rule{
		{Actions: []string{"gh__search"}, Pattern: "*golang/go*", Decision: Deny},
	}}
	if got := s.Eval("gh__search", raw(`{"repo":"golang/go"}`)); got != Deny {
		t.Errorf("raw input pattern = %q, want deny", got)
	}
	if got := s.Eval("gh__search", raw(`{"repo":"golang/x"}`)); got != Allow {
		t.Errorf("non-matching raw input = %q, want allow", got)
	}
}

// TestMCPNamespacedExternalDirectory: a path inside an MCP tool's input
// still runs through the implicit external_directory action, whatever the
// tool's own id is.
func TestMCPNamespacedExternalDirectory(t *testing.T) {
	s := &Set{Default: Allow, WorkDir: t.TempDir(), Rules: []Rule{
		{Actions: []string{ExternalDirectory}, Pattern: "/etc/*", Decision: Deny},
	}}
	if got := s.Eval("fs__read", raw(`{"path":"/etc/shadow"}`)); got != Deny {
		t.Errorf("external path via MCP tool = %q, want deny", got)
	}
	if got := s.Eval("fs__read", raw(`{"path":"notes.txt"}`)); got != Allow {
		t.Errorf("inside workdir = %q, want allow", got)
	}
}
