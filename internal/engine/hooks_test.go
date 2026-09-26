package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"matcode/internal/permissions"
	"matcode/internal/plugin"
	"matcode/internal/store"
)

// fakeHooks is a scripted plugin.Hooker: every method answers what the test
// set, and records the params it was handed. It is the stand-in for a host
// so the engine's §8 wiring can be tested without a subprocess.
type fakeHooks struct {
	ctxLines   string
	ctxOK      bool
	before     json.RawMessage
	beforeOK   bool
	after      string
	afterOK    bool
	perm       string
	permOK     bool
	shell      plugin.ShellDecision
	compaction string
	compOK     bool

	seenTool    string
	seenCommand string
}

func (f *fakeHooks) SessionContext(context.Context, string) (string, bool) {
	return f.ctxLines, f.ctxOK
}

func (f *fakeHooks) ToolBefore(_ context.Context, tool string, _ json.RawMessage) (json.RawMessage, bool) {
	f.seenTool = tool
	return f.before, f.beforeOK
}

func (f *fakeHooks) ToolAfter(_ context.Context, tool string, _ json.RawMessage, out string) (string, bool) {
	f.seenTool = tool
	return f.after, f.afterOK
}

func (f *fakeHooks) PermissionEvaluate(context.Context, string, json.RawMessage) (string, bool) {
	return f.perm, f.permOK
}

func (f *fakeHooks) ShellBefore(_ context.Context, cmd string) plugin.ShellDecision {
	f.seenCommand = cmd
	return f.shell
}

func (f *fakeHooks) SessionCompaction(context.Context, string, any) (string, bool) {
	return f.compaction, f.compOK
}

// echoTool records the input it was called with and returns it back.
func echoTool(id string, got *json.RawMessage) Tool {
	return Tool{
		ID:          id,
		Description: "echo",
		Execute: func(_ context.Context, input json.RawMessage) (Result, error) {
			*got = input
			return Result{Text: "ran"}, nil
		},
	}
}

// TestSessionContextReachesSystem proves the hook's lines land in the system
// prompt of the next provider call, and that no session means no call.
func TestSessionContextReachesSystem(t *testing.T) {
	h := &fakeHooks{ctxLines: "[plugin] house rules", ctxOK: true}
	s := newSession(t)
	fp := &fakeProvider{}
	e := &Engine{Session: s, System: "SYS", Provider: fp, Model: "mockt/t", Plugins: h}
	if _, _, _, err := e.complete(context.Background(), []store.Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatal(err)
	}
	if len(fp.reqs) != 1 {
		t.Fatalf("provider saw %d requests, want 1", len(fp.reqs))
	}
	if !strings.Contains(fp.reqs[0].System, "house rules") {
		t.Errorf("system prompt missing plugin lines: %q", fp.reqs[0].System)
	}
	if !strings.HasPrefix(fp.reqs[0].System, "SYS") {
		t.Errorf("system prompt lost the agent system: %q", fp.reqs[0].System)
	}

	// Stateless runs have no session id to pass: the hook never fires.
	fp2 := &fakeProvider{}
	e2 := &Engine{System: "SYS", Provider: fp2, Model: "mockt/t", Plugins: h}
	if _, _, _, err := e2.complete(context.Background(), []store.Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fp2.reqs[0].System, "house rules") {
		t.Error("stateless run must not ask for session context")
	}
}

// TestPermissionEvaluateOnlyTightens proves the three rules of §8: a rule
// match never reaches the hook, a plugin may tighten the default, and a
// plugin may never loosen it.
func TestPermissionEvaluateOnlyTightens(t *testing.T) {
	newEngine := func(set *permissions.Set, h plugin.Hooker) (*Engine, *json.RawMessage) {
		var got json.RawMessage
		e := &Engine{
			Permissions: set,
			Plugins:     h,
			Tools:       []Tool{echoTool("read", &got)},
		}
		return e, &got
	}
	call := store.ToolCall{Name: "read", Arguments: `{"path":"a.txt"}`}

	// Default allow + a plugin that says deny: tighten wins.
	e, _ := newEngine(&permissions.Set{Default: permissions.Allow},
		&fakeHooks{perm: permissions.Deny, permOK: true})
	if got := e.dispatch(context.Background(), call).Text; got != "denied by plugin" {
		t.Errorf("tighten: %q, want denied by plugin", got)
	}

	// Default deny + a plugin that says allow: the loosening is ignored.
	e, _ = newEngine(&permissions.Set{Default: permissions.Deny},
		&fakeHooks{perm: permissions.Allow, permOK: true})
	if got := e.dispatch(context.Background(), call).Text; got != "denied by permissions.toml" {
		t.Errorf("loosen: %q, want denied by permissions.toml", got)
	}

	// A matching rule answers before the hook is ever consulted.
	set := &permissions.Set{Default: permissions.Deny, Rules: []permissions.Rule{
		{Actions: []string{"read"}, Pattern: "*", Decision: permissions.Allow},
	}}
	e, got := newEngine(set, &fakeHooks{perm: permissions.Deny, permOK: true})
	if out := e.dispatch(context.Background(), call).Text; out != "ran" {
		t.Errorf("rule match: %q, want the call to run", out)
	}
	if string(*got) == "" {
		t.Error("tool should have executed with the original input")
	}

	// The build agent has no Permissions at all: no evaluation, no hook.
	e, _ = newEngine(nil, &fakeHooks{perm: permissions.Deny, permOK: true})
	if out := e.dispatch(context.Background(), call).Text; out != "ran" {
		t.Errorf("nil permissions: %q, want the call to run", out)
	}
}

// TestToolHooksWrapExecution proves input rewrite and output augmentation,
// and that an unanswered hook leaves both untouched.
func TestToolHooksWrapExecution(t *testing.T) {
	var got json.RawMessage
	h := &fakeHooks{
		before: json.RawMessage(`{"path":"b.txt"}`), beforeOK: true,
		after: "ran [+plugin]", afterOK: true,
	}
	e := &Engine{Tools: []Tool{echoTool("read", &got)}, Plugins: h}
	out := e.dispatch(context.Background(), store.ToolCall{Name: "read", Arguments: `{"path":"a.txt"}`})
	if !strings.Contains(string(got), "b.txt") || strings.Contains(string(got), "a.txt") {
		t.Errorf("input = %s, want the rewritten one", got)
	}
	if out.Text != "ran [+plugin]" {
		t.Errorf("result = %q, want the augmented one", out.Text)
	}
	if h.seenTool != "read" {
		t.Errorf("hook saw tool %q, want read", h.seenTool)
	}

	// No opinion: original input, original output.
	var got2 json.RawMessage
	e2 := &Engine{Tools: []Tool{echoTool("read", &got2)}, Plugins: &fakeHooks{}}
	out2 := e2.dispatch(context.Background(), store.ToolCall{Name: "read", Arguments: `{"path":"a.txt"}`})
	if !strings.Contains(string(got2), "a.txt") || out2.Text != "ran" {
		t.Errorf("unhandled hooks must not touch anything: %s / %q", got2, out2.Text)
	}
}

// TestShellHookRewritesAndVetoes proves shell.create.before gets the command,
// can rewrite it, and can veto the call outright.
func TestShellHookRewritesAndVetoes(t *testing.T) {
	var got json.RawMessage
	e := &Engine{
		Tools: []Tool{echoTool("bash", &got)},
		Plugins: &fakeHooks{shell: plugin.ShellDecision{
			Command: "echo safe", Handled: true,
		}},
	}
	out := e.dispatch(context.Background(), store.ToolCall{Name: "bash", Arguments: `{"command":"rm -rf /"}`})
	if !strings.Contains(string(got), "echo safe") || strings.Contains(string(got), "rm -rf") {
		t.Errorf("input = %s, want the rewritten command", got)
	}
	if out.Text != "ran" {
		t.Errorf("result = %q, want the call to run", out.Text)
	}

	veto := &Engine{
		Tools: []Tool{echoTool("bash", &got)},
		Plugins: &fakeHooks{shell: plugin.ShellDecision{
			Deny: true, Reason: "not today", Handled: true,
		}},
	}
	out = veto.dispatch(context.Background(), store.ToolCall{Name: "bash", Arguments: `{"command":"rm -rf /"}`})
	if out.Text != "denied by plugin: not today" {
		t.Errorf("veto = %q, want denied by plugin: not today", out.Text)
	}
	if got == nil {
		t.Fatal("a veto must not execute the tool")
	}
}

// TestCompactionHookReplacesExtractor proves session.compaction writes the
// checkpoint itself: no archive round, one compaction event, append-only.
func TestCompactionHookReplacesExtractor(t *testing.T) {
	s := newSession(t)
	mustAppend(t, s, &store.Message{Role: "user", Content: "build a parser"})
	for i := 0; i < 40; i++ {
		addExchange(t, s, "c"+string(rune('a'+i%26))+string(rune('a'+i/26)), "write", `{"path":"f.go"}`, "wrote f.go")
	}
	mustAppend(t, s, &store.Message{Role: "user", Content: "now add tests"})
	before, _ := s.Messages()

	cp := "## Objective\nfrom the plugin\n"
	e := &Engine{Session: s, System: "sys", Plugins: &fakeHooks{compaction: cp, compOK: true}}
	if err := e.Compact(); err != nil {
		t.Fatal(err)
	}
	after, _ := s.Messages()
	if len(after) != len(before)+1 {
		t.Fatalf("transcript grew by %d lines, want exactly 1", len(after)-len(before))
	}
	body, _ := os.ReadFile(filepath.Join(s.Dir, "checkpoint.md"))
	if string(body) != cp {
		t.Errorf("checkpoint.md = %q, want the plugin's text", body)
	}
	if _, err := os.Stat(filepath.Join(s.Dir, "compaction", "archive.md")); err == nil {
		t.Error("the plugin owns compaction: the built-in archive round must not run")
	}
	ev := after[len(after)-1]
	if ev.Role != store.RoleCompaction || ev.Content != cp || ev.Through == "" {
		t.Errorf("event = %+v, want a compaction event carrying the checkpoint", ev)
	}

	// A plugin that declines falls back to the deterministic extractor.
	s2 := newSession(t)
	mustAppend(t, s2, &store.Message{Role: "user", Content: "build a parser"})
	for i := 0; i < 40; i++ {
		addExchange(t, s2, "d"+string(rune('a'+i%26))+string(rune('a'+i/26)), "write", `{"path":"f.go"}`, "wrote f.go")
	}
	mustAppend(t, s2, &store.Message{Role: "user", Content: "now add tests"})
	e2 := &Engine{Session: s2, System: "sys", Plugins: &fakeHooks{}}
	if err := e2.Compact(); err != nil {
		t.Fatal(err)
	}
	body2, _ := os.ReadFile(filepath.Join(s2.Dir, "checkpoint.md"))
	if !strings.Contains(string(body2), "## Objective") || strings.Contains(string(body2), "from the plugin") {
		t.Errorf("declining plugin: checkpoint = %q, want the built-in shape", body2)
	}
}

// TestHooksNilEngine is the free-path guard: a build with no hook host runs
// every hook-shaped branch as a no-op.
func TestHooksNilEngine(t *testing.T) {
	var got json.RawMessage
	e := &Engine{Tools: []Tool{echoTool("bash", &got)}}
	out := e.dispatch(context.Background(), store.ToolCall{Name: "bash", Arguments: `{"command":"echo hi"}`})
	if out.Text != "ran" || !strings.Contains(string(got), "echo hi") {
		t.Errorf("nil Plugins changed the call: %q / %s", out.Text, got)
	}
	// A typed-nil host answers unhandled instead of panicking: that is how
	// `mtc plugin …`-less builds stay free of the whole surface.
	var h plugin.Hooker = (*plugin.Host)(nil)
	if _, ok := h.SessionContext(context.Background(), "ses_x"); ok {
		t.Error("nil hooker must answer unhandled")
	}
	if _, ok := h.ToolBefore(context.Background(), "read", nil); ok {
		t.Error("nil hooker must not rewrite input")
	}
}
