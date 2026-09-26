package engine

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"matcode/internal/permissions"
	"matcode/internal/plugin"
	"matcode/internal/store"
)

// mcpToolID mirrors mcp.ToolName's `server__tool` namespacing. The engine
// package cannot import matcode/internal/mcp (mcp imports engine), so the
// id is built exactly the way mcp.EngineTools builds it.
func mcpToolID(server, tool string) string { return server + "__" + tool }

// countingMCPTool is a fake MCP-namespaced tool: it counts executions and
// records the input it was dispatched with.
func countingMCPTool(id string, runs *int, got *json.RawMessage) Tool {
	return Tool{
		ID:          id,
		Description: "fake MCP tool",
		Schema:      map[string]any{"type": "object"},
		Execute: func(_ context.Context, input json.RawMessage) (Result, error) {
			*runs++
			*got = input
			return Result{Text: "ran"}, nil
		},
	}
}

// TestMCPToolRuleAllowAndDeny proves a `server__tool` id reaches the
// permission set before Execute: an allow rule runs it (even on a tool
// swapped in live by ReplaceMCP), a deny rule stops it, and a rule for the
// bare builtin id never leaks into the server namespace.
func TestMCPToolRuleAllowAndDeny(t *testing.T) {
	id := mcpToolID("fake", "echo")
	call := store.ToolCall{Name: id, Arguments: `{"query":"hello"}`}

	// Default deny + exact allow rule: the MCP tool runs, input intact.
	runs, got := 0, json.RawMessage(nil)
	e := &Engine{
		Permissions: &permissions.Set{Default: permissions.Deny, Rules: []permissions.Rule{
			{Actions: []string{id}, Pattern: "*", Decision: permissions.Allow},
		}},
	}
	e.ReplaceMCP("fake", []Tool{countingMCPTool(id, &runs, &got)})
	if out := e.dispatch(context.Background(), call); out.Text != "ran" {
		t.Fatalf("allow rule: %q, want the tool to run", out.Text)
	}
	if runs != 1 || !strings.Contains(string(got), "hello") {
		t.Fatalf("runs=%d input=%s, want 1 call with the original input", runs, got)
	}

	// Default allow + server-prefix deny rule: never reaches Execute.
	runs, got = 0, nil
	e = &Engine{
		Permissions: &permissions.Set{Default: permissions.Allow, Rules: []permissions.Rule{
			{Actions: []string{"fake__*"}, Pattern: "*", Decision: permissions.Deny},
		}},
	}
	e.ReplaceMCP("fake", []Tool{countingMCPTool(id, &runs, &got)})
	if out := e.dispatch(context.Background(), call); out.Text != "denied by permissions.toml" {
		t.Fatalf("deny rule: %q", out.Text)
	}
	if runs != 0 {
		t.Errorf("denied MCP tool executed %d times", runs)
	}

	// A rule for the bare builtin name covers only the builtin: the
	// namespaced tool stays on the default deny, the builtin runs.
	bareRuns, bareGot := 0, json.RawMessage(nil)
	runs, got = 0, nil
	e = &Engine{
		Permissions: &permissions.Set{Default: permissions.Deny, Rules: []permissions.Rule{
			{Actions: []string{"echo"}, Pattern: "*", Decision: permissions.Allow},
		}},
		Tools: []Tool{
			countingMCPTool("echo", &bareRuns, &bareGot),
			countingMCPTool(id, &runs, &got),
		},
	}
	if out := e.dispatch(context.Background(), call); out.Text != "denied by permissions.toml" || runs != 0 {
		t.Errorf("namespaced tool inherited the bare rule: %q runs=%d", out.Text, runs)
	}
	if out := e.dispatch(context.Background(), store.ToolCall{Name: "echo", Arguments: `{"query":"hello"}`}); out.Text != "ran" || bareRuns != 1 {
		t.Errorf("bare builtin: %q runs=%d, want the allow rule to run it", out.Text, bareRuns)
	}
}

// TestMCPToolDefaultAndPlugin: with no rule the engine falls back to the
// set's default, and the plugin hook sits between the miss and the default
// for namespaced calls too — it may tighten, never loosen, and a rule match
// answers before the hook is consulted at all.
func TestMCPToolDefaultAndPlugin(t *testing.T) {
	id := mcpToolID("fake", "echo")
	call := store.ToolCall{Name: id, Arguments: `{"query":"x"}`}
	mcpEngine := func(set *permissions.Set, h plugin.Hooker) (*Engine, *int) {
		runs := 0
		var got json.RawMessage
		e := &Engine{Permissions: set, Plugins: h}
		e.ReplaceMCP("fake", []Tool{countingMCPTool(id, &runs, &got)})
		return e, &runs
	}

	e, runs := mcpEngine(&permissions.Set{Default: permissions.Deny},
		&fakeHooks{perm: permissions.Allow, permOK: true})
	if out := e.dispatch(context.Background(), call); out.Text != "denied by permissions.toml" || *runs != 0 {
		t.Errorf("loosen: %q runs=%d, want default deny to hold", out.Text, *runs)
	}

	e, runs = mcpEngine(&permissions.Set{Default: permissions.Allow},
		&fakeHooks{perm: permissions.Deny, permOK: true})
	if out := e.dispatch(context.Background(), call); out.Text != "denied by plugin" || *runs != 0 {
		t.Errorf("tighten: %q runs=%d, want the plugin to block it", out.Text, *runs)
	}

	e, runs = mcpEngine(&permissions.Set{Default: permissions.Deny, Rules: []permissions.Rule{
		{Actions: []string{id}, Pattern: "*", Decision: permissions.Allow},
	}}, &fakeHooks{perm: permissions.Deny, permOK: true})
	if out := e.dispatch(context.Background(), call); out.Text != "ran" || *runs != 1 {
		t.Errorf("rule vs hook: %q runs=%d, want the rule to win untouched", out.Text, *runs)
	}
}

// TestMCPToolAskPath: an ask verdict on a namespaced tool blocks without
// approval, runs on a one-off approval, and "always" persists an approved
// allow rule into permissions.toml.
func TestMCPToolAskPath(t *testing.T) {
	id := mcpToolID("fake", "echo")
	call := store.ToolCall{Name: id, Arguments: `{"query":"ship it"}`}
	set := &permissions.Set{
		Default: permissions.Allow,
		Path:    filepath.Join(t.TempDir(), "permissions.toml"),
		Rules: []permissions.Rule{
			{Actions: []string{id}, Pattern: "*", Decision: permissions.Ask},
		},
	}
	runs := 0
	var got json.RawMessage
	e := &Engine{Permissions: set, Tools: []Tool{countingMCPTool(id, &runs, &got)}}

	if out := e.dispatch(context.Background(), call); out.Text != "denied: approval was not granted" || runs != 0 {
		t.Fatalf("no approval: %q runs=%d", out.Text, runs)
	}
	e.Ask = func(string, json.RawMessage) (bool, bool) { return true, false }
	if out := e.dispatch(context.Background(), call); out.Text != "ran" || runs != 1 {
		t.Fatalf("approved once: %q runs=%d", out.Text, runs)
	}
	e.Ask = func(string, json.RawMessage) (bool, bool) { return true, true }
	if out := e.dispatch(context.Background(), call); out.Text != "ran" || runs != 2 {
		t.Fatalf("approved always: %q runs=%d", out.Text, runs)
	}
	if len(set.Rules) != 2 || set.Rules[1].Decision != permissions.Allow || !set.Rules[1].Approved {
		t.Fatalf("always did not persist an approved allow: %+v", set.Rules)
	}
	if got := set.Eval(id, json.RawMessage(`{"query":"ship it"}`)); got != permissions.Allow {
		t.Errorf("after always = %q, want allow (approved beats ask)", got)
	}
}
