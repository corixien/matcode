package plugin

import (
	"context"
	"encoding/json"
	"strings"
)

// Hooker is the hook surface (§8) the engine, the HTTP API and the TUI use.
// *Host implements it; a nil *Host answers "not handled" for every method,
// so a build without plugins pays nothing.
type Hooker interface {
	// SessionContext returns the system lines session plugins append to the
	// next request (session.context).
	SessionContext(ctx context.Context, sessionID string) (lines string, ok bool)
	// ToolBefore rewrites a tool input before it executes
	// (tool.execute.before); ok=false keeps the input as the model sent it.
	ToolBefore(ctx context.Context, tool string, input json.RawMessage) (json.RawMessage, bool)
	// ToolAfter augments a tool result (tool.execute.after); ok=false keeps
	// the original output.
	ToolAfter(ctx context.Context, tool string, input json.RawMessage, output string) (string, bool)
	// PermissionEvaluate asks plugins between a rule miss and the default
	// (permission.evaluate). Only a tightening answer is honored.
	PermissionEvaluate(ctx context.Context, tool string, input json.RawMessage) (string, bool)
	// ShellBefore rewrites or vetoes a shell command (shell.create.before).
	ShellBefore(ctx context.Context, command string) ShellDecision
	// SessionCompaction supplies a checkpoint instead of the default fold
	// (session.compaction); ok=false runs the built-in extractor.
	SessionCompaction(ctx context.Context, sessionID string, messages any) (string, bool)
}

// ShellDecision is what a shell.create.before hook answered.
type ShellDecision struct {
	Command string // rewritten command ("" = unchanged)
	Deny    bool   // true = veto the command outright
	Reason  string // shown to the model when Deny
	Handled bool   // false = no plugin had an opinion
}

// SessionContext implements Hooker.
func (h *Host) SessionContext(ctx context.Context, sessionID string) (string, bool) {
	if h == nil {
		return "", false
	}
	var lines []string
	for _, res := range h.callAll(ctx, HookSessionContext, PermSession, map[string]any{"sessionID": sessionID}) {
		var r struct {
			Lines json.RawMessage `json:"lines"`
		}
		if json.Unmarshal(res, &r) != nil || len(r.Lines) == 0 {
			continue
		}
		var one string
		if json.Unmarshal(r.Lines, &one) == nil {
			if strings.TrimSpace(one) != "" {
				lines = append(lines, one)
			}
			continue
		}
		var many []string
		if json.Unmarshal(r.Lines, &many) == nil {
			for _, l := range many {
				if strings.TrimSpace(l) != "" {
					lines = append(lines, l)
				}
			}
		}
	}
	if len(lines) == 0 {
		return "", false
	}
	return strings.Join(lines, "\n"), true
}

// ToolBefore implements Hooker.
func (h *Host) ToolBefore(ctx context.Context, tool string, input json.RawMessage) (json.RawMessage, bool) {
	if h == nil {
		return input, false
	}
	res := h.callFirst(ctx, HookToolBefore, PermTool, map[string]any{"tool": tool, "input": rawMap(input)})
	if len(res) == 0 {
		return input, false
	}
	var r struct {
		Input json.RawMessage `json:"input"`
	}
	if json.Unmarshal(res, &r) != nil || len(r.Input) == 0 {
		return input, false
	}
	return r.Input, true
}

// ToolAfter implements Hooker.
func (h *Host) ToolAfter(ctx context.Context, tool string, input json.RawMessage, output string) (string, bool) {
	if h == nil {
		return output, false
	}
	res := h.callFirst(ctx, HookToolAfter, PermTool, map[string]any{
		"tool": tool, "input": rawMap(input), "output": output,
	})
	if len(res) == 0 {
		return output, false
	}
	var r struct {
		Output *string `json:"output"`
	}
	if json.Unmarshal(res, &r) != nil || r.Output == nil || *r.Output == "" {
		return output, false
	}
	return *r.Output, true
}

// PermissionEvaluate implements Hooker.
func (h *Host) PermissionEvaluate(ctx context.Context, tool string, input json.RawMessage) (string, bool) {
	if h == nil {
		return "", false
	}
	res := h.callFirst(ctx, HookPermissionEvaluate, PermPermission, map[string]any{
		"tool": tool, "input": rawMap(input),
	})
	if len(res) == 0 {
		return "", false
	}
	var r struct {
		Decision string `json:"decision"`
	}
	if json.Unmarshal(res, &r) != nil {
		return "", false
	}
	switch r.Decision {
	case Allow, Ask, Deny:
		return r.Decision, true
	default:
		return "", false // an unknown decision is not an answer
	}
}

// Hook decisions; they match permissions.Allow/Ask/Deny on purpose so the
// engine can compare them without importing anything.
const (
	Allow = "allow"
	Ask   = "ask"
	Deny  = "deny"
)

// ShellBefore implements Hooker.
func (h *Host) ShellBefore(ctx context.Context, command string) ShellDecision {
	if h == nil {
		return ShellDecision{}
	}
	res := h.callFirst(ctx, HookShellBefore, PermShell, map[string]any{"command": command})
	if len(res) == 0 {
		return ShellDecision{}
	}
	var r struct {
		Command string `json:"command"`
		Deny    bool   `json:"deny"`
		Reason  string `json:"reason"`
	}
	if json.Unmarshal(res, &r) != nil {
		return ShellDecision{}
	}
	return ShellDecision{Command: r.Command, Deny: r.Deny, Reason: r.Reason, Handled: true}
}

// SessionCompaction implements Hooker.
func (h *Host) SessionCompaction(ctx context.Context, sessionID string, messages any) (string, bool) {
	if h == nil {
		return "", false
	}
	res := h.callFirst(ctx, HookSessionCompaction, PermSession, map[string]any{
		"sessionID": sessionID, "messages": messages,
	})
	if len(res) == 0 {
		return "", false
	}
	var r struct {
		Checkpoint string `json:"checkpoint"`
	}
	if json.Unmarshal(res, &r) != nil || strings.TrimSpace(r.Checkpoint) == "" {
		return "", false
	}
	return r.Checkpoint, true
}

// rawMap passes a tool input through as a JSON object: a plugin reads
// {"input": {...}} and an unparseable input stays visible as raw text.
func rawMap(input json.RawMessage) any {
	var m map[string]any
	if json.Unmarshal(input, &m) == nil {
		return m
	}
	return string(input)
}

var _ Hooker = (*Host)(nil)
