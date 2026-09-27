// Package engine runs a session: it assembles the payload, calls the
// provider, dispatches tool calls, and appends everything to the store.
package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"matcode/internal/catalog"
	"matcode/internal/permissions"
	"matcode/internal/plugin"
	"matcode/internal/providers"
	"matcode/internal/skills"
	"matcode/internal/snapshots"
	"matcode/internal/store"
)

// Result is what a tool hands back: Text the model reads, plus optional
// Media (base64 images/PDFs) persisted on the store message and replayed
// to vision-capable providers on resume.
type Result struct {
	Text  string
	Media []Media
}

// Media is an attachment as tools hand it to the engine — the store's
// persisted form, aliased so tools never import the store directly.
type Media = store.Media

// Tool is one callable. Schema is the JSON Schema for the tool's input;
// execute returns the text (and any media) the model sees as the result.
type Tool struct {
	ID          string
	Description string
	Schema      map[string]any
	Execute     func(ctx context.Context, input json.RawMessage) (Result, error)
}

// Engine drives one session.
type Engine struct {
	Session  *store.Session
	Provider providers.Provider
	Model    string
	System   string
	Tools    []Tool
	// toolsMu guards Tools: MCP servers swap their `server__` subset
	// live on tools_list_changed (row 43). Every read of e.Tools in a
	// hot path takes the read lock; ReplaceMCP takes the write lock.
	toolsMu sync.RWMutex
	// Echo receives streamed assistant text. nil discards it.
	Echo func(string)
	// Emit receives structured milestones of a turn (tool start/end,
	// compaction, usage). nil discards them; `run --format json` and the
	// TUI both render from this stream.
	Emit func(Event)
	// Permissions is evaluated before every tool call; nil allows everything.
	Permissions *permissions.Set
	// Plugins is the hook bus (§8): permission.evaluate runs between a rule
	// miss and the default, tool.execute.before/.after wrap every call,
	// session.context appends system lines, session.compaction may replace
	// the built-in fold. nil disables the whole surface.
	Plugins plugin.Hooker
	// Ask handles an "ask" verdict interactively: it blocks the turn until
	// the caller answers. allow lets the call through, and always also
	// persists an allow rule through Permissions.Approve. nil Ask (or a
	// false answer) denies the call, which keeps `mtc run` and the API
	// headless-safe.
	Ask func(action string, input json.RawMessage) (allow, always bool)
	// MaxRounds caps consecutive tool-call loops per turn (0 = 32);
	// agents set it from frontmatter `steps`.
	MaxRounds int
	// AfterTurn runs when the model stops asking, before the usage
	// flush; the app layer uses it to generate title.txt/summary.txt
	// after a session's first turn (row 40). Best effort: the hook
	// swallows its own errors and the turn never fails because of it.
	AfterTurn func(ctx context.Context)
	// CompactionLLM is the §11 step-5 fallback: it receives the prepared
	// prompt (previous checkpoint + folded transcript) and returns a
	// checkpoint.md body from the compaction agent. nil (or an error)
	// surfaces the extraction error as before — the normal path never
	// spends a model call.
	CompactionLLM func(ctx context.Context, prompt string) (string, error)
	// RequestHeaders/RequestBody are per-agent request overlays forwarded
	// into every providers.Request (frontmatter `request.*`).
	RequestHeaders map[string]string
	RequestBody    map[string]any
	// Snapshots records a file-state pair per tool-using step; nil = off.
	Snapshots *snapshots.Capturer
	// AutoCompact folds the transcript at ContextLimit-CompactBuffer before a
	// turn starts (spec §11).
	AutoCompact bool
	// ContextLimit is the model context window in tokens; 0 disables the
	// auto-compaction threshold.
	ContextLimit int
	// Skills supplies the loaded-skill guidance block for the payload; nil
	// means no skills are in play.
	Skills *skills.Set
	// Usage accumulates token accounting across completed provider calls.
	Usage providers.Usage
	// flushed tracks what flushUsage already wrote, so multi-turn engines
	// never double-count a turn into session totals.
	flushed providers.Usage
}

// Oneshot sends one prompt and returns the reply without touching the store:
// the path for stateless agents (summary, title).
func (e *Engine) Oneshot(ctx context.Context, text string, media ...Media) (string, error) {
	reply, calls, u, err := e.complete(ctx, []store.Message{{Role: "user", Content: text, Media: media}})
	e.addUsage(u)
	if err != nil {
		return "", err
	}
	if len(calls) > 0 {
		return reply, fmt.Errorf("engine: stateless run produced %d tool call(s)", len(calls))
	}
	return reply, nil
}

// OneshotSystem runs one stateless completion with an explicit system
// prompt and no tools — the generator path for title.txt/summary.txt
// (row 40). The reply is trimmed; usage is billed to this engine.
func (e *Engine) OneshotSystem(ctx context.Context, system, text string) (string, error) {
	reply, calls, u, err := e.completeWith(ctx, []store.Message{{Role: "user", Content: text}}, system, nil)
	e.addUsage(u)
	if err != nil {
		return "", err
	}
	if len(calls) > 0 {
		return "", fmt.Errorf("engine: generator produced %d tool call(s)", len(calls))
	}
	return strings.TrimSpace(reply), nil
}

// Turn sends one user message and runs tool calls until the model stops
// asking. Assistant prose and every tool exchange are persisted. media
// attaches prompt inputs (file://, data:) to the user message.
func (e *Engine) Turn(ctx context.Context, text string, media ...Media) error {
	if err := e.Session.Append(&store.Message{Role: "user", Content: text, Media: media}); err != nil {
		return err
	}
	return e.run(ctx)
}

// run is the turn body once the user prompt is in the transcript:
// auto-compact check, then the tool-call loop until the model stops
// asking (calls the post-turn hook and flushes usage).
func (e *Engine) run(ctx context.Context) error {
	history, err := e.Session.Messages()
	if err != nil {
		return err
	}
	if e.AutoCompact && e.ContextLimit > 0 &&
		payloadEstimate(e.systemPrompt(), history) > e.ContextLimit-CompactBuffer {
		if err := e.Compact(); err == nil {
			if fresh, err := e.Session.Messages(); err == nil {
				history = fresh
			}
		}
		// A failed round keeps the previous checkpoint: the turn still runs.
	}
	maxRounds := e.MaxRounds
	if maxRounds <= 0 {
		maxRounds = 32
	}

	for round := 0; round < maxRounds; round++ {
		reply, calls, u, err := e.complete(ctx, history)
		e.addUsage(u)
		if err != nil {
			return err
		}
		if reply != "" || len(calls) > 0 {
			asst := store.Message{Role: "assistant", Content: reply, ToolCalls: calls}
			if err := e.Session.Append(&asst); err != nil {
				return err
			}
			history = append(history, asst)
		}
		if len(calls) == 0 {
			// The post-turn hook runs before the usage flush so its
			// calls (title/summary generation, row 40) are billed too.
			if e.AfterTurn != nil {
				e.AfterTurn(ctx)
			}
			if err := e.flushUsage(); err != nil {
				return err
			}
			e.emitUsage()
			return nil
		}
		step := history[len(history)-1].ID
		if e.Snapshots != nil {
			e.Snapshots.Step(step)
		}
		for _, call := range calls {
			res := e.dispatch(ctx, call)
			msg := store.Message{Role: "tool", Content: res.Text, Media: res.Media, ToolCallID: call.ID}
			if err := e.Session.Append(&msg); err != nil {
				return err
			}
			history = append(history, msg)
		}
		if e.Snapshots != nil {
			e.Snapshots.Finish(step)
		}
	}
	return fmt.Errorf("engine: tool loop exceeded %d rounds", maxRounds)
}

// PrepareRetry (row 47, §10) drops everything after the last user
// prompt — archived as a revert backup, so the dropped turn stays
// recoverable — and returns that prompt. A transcript whose last
// message is the prompt itself has nothing to retry.
func (e *Engine) PrepareRetry() (string, error) {
	msgs, err := e.Session.Messages()
	if err != nil {
		return "", err
	}
	idx := -1
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			idx = i
			break
		}
	}
	if idx < 0 || idx == len(msgs)-1 {
		return "", fmt.Errorf("engine: nothing to retry")
	}
	if _, _, err := e.Session.Revert(msgs[idx].ID); err != nil {
		return "", err
	}
	return msgs[idx].Content, nil
}

// Retry runs the turn over the transcript as PrepareRetry left it: the
// prompt is already the last message, so — unlike Turn — nothing is
// appended. Usage of both runs is kept: the re-run costs real tokens.
func (e *Engine) Retry(ctx context.Context) error { return e.run(ctx) }

// addUsage folds one provider response into the engine totals. When the
// provider reports no cost itself (only OpenRouter does), the embedded
// models.dev catalog prices the call from token counts — the first
// `provider/model` whose rate the catalog knows.
func (e *Engine) addUsage(u providers.Usage) {
	if u.CostUSD == 0 && (u.Input > 0 || u.Output > 0) {
		if usd, ok := catalog.Price(e.Model, u.Input, u.Output); ok {
			u.CostUSD = usd
		}
	}
	e.Usage.Input += u.Input
	e.Usage.Output += u.Output
	e.Usage.CostUSD += u.CostUSD
}

// flushUsage writes this turn's new token totals into the session's
// session.json. Usage keeps accumulating for the footer; only the delta
// since the last flush is persisted.
func (e *Engine) flushUsage() error {
	if e.Session == nil {
		return nil
	}
	in := e.Usage.Input - e.flushed.Input
	out := e.Usage.Output - e.flushed.Output
	cost := e.Usage.CostUSD - e.flushed.CostUSD
	e.flushed = e.Usage
	if in == 0 && out == 0 && cost == 0 {
		return nil
	}
	return e.Session.AddUsage(in, out, cost)
}

// emitUsage publishes this engine's accumulated token/cost totals after a
// turn completes. nil Emit discards it.
func (e *Engine) emitUsage() {
	if e.Emit == nil {
		return
	}
	e.Emit(Event{Type: EventUsage, Usage: &UsageEvent{
		Input: e.Usage.Input, Output: e.Usage.Output, Cost: e.Usage.CostUSD,
	}})
}

// systemPrompt is the system text for one step: the agent system plus the
// loaded-skill guidance block (spec §11 pipeline). Skills live here because
// the system prompt is the only part of the payload compaction never folds.
func (e *Engine) systemPrompt() string {
	if e.Skills == nil {
		return e.System
	}
	g := e.Skills.Guidance()
	if g == "" {
		return e.System
	}
	return e.System + "\n\n" + g
}

// complete streams one provider call with the engine's system prompt,
// echoing text, and returns the reply, accumulated tool calls, and the
// call's token usage.
func (e *Engine) complete(ctx context.Context, history []store.Message) (string, []store.ToolCall, providers.Usage, error) {
	return e.completeWith(ctx, history, e.systemPrompt(), e.schemas())
}

// completeWith is complete with an explicit system prompt and tool set —
// the generator path for stateless calls (row 40) overrides both.
func (e *Engine) completeWith(ctx context.Context, history []store.Message, system string, tools []providers.ToolSchema) (string, []store.ToolCall, providers.Usage, error) {
	// No credential at boot (the TUI starts without one so /provider can set
	// it): fail the turn with a fixable message instead of panicking.
	if e.Provider == nil {
		return "", nil, providers.Usage{}, fmt.Errorf("no API key for %q — set one with /provider in the TUI, in the data dir .env, or in the shell", e.Model)
	}
	system, msgs := payload(system, history)
	// session.context (§8): every session plugin stacks lines onto the
	// system prompt. Stateless runs (no session) have no id to pass.
	if e.Session != nil && e.Plugins != nil {
		if lines, ok := e.Plugins.SessionContext(ctx, e.Session.Meta.ID); ok {
			system += "\n\n" + lines
		}
	}
	req := providers.Request{
		Model:     e.Model,
		System:    system,
		Messages:  msgs,
		Tools:     tools,
		MaxTokens: 4096,
		Headers:   e.RequestHeaders,
		Body:      e.RequestBody,
	}
	var text strings.Builder
	var usage providers.Usage
	byIndex := map[int]*store.ToolCall{}
	var order []int

	for chunk := range e.Provider.Stream(ctx, req) {
		if chunk.Err != nil {
			return "", nil, usage, chunk.Err
		}
		if chunk.Usage != nil {
			usage = *chunk.Usage
		}
		text.WriteString(chunk.Text)
		if chunk.Text != "" && e.Echo != nil {
			e.Echo(chunk.Text)
		}
		for _, tc := range chunk.ToolCalls {
			// OpenAI deltas carry no index, so match on id: a delta repeating
			// the open call's id continues that call, a new id starts the next
			// call, and a delta without an id appends to the last one.
			slot := -1
			if tc.ID != "" {
				if last := lastKey(byIndex); byIndex[last] != nil && byIndex[last].ID == tc.ID {
					slot = last
				}
			} else if len(order) > 0 {
				slot = order[len(order)-1]
			}
			if slot < 0 {
				slot = len(order)
			}
			if byIndex[slot] == nil {
				byIndex[slot] = &store.ToolCall{ID: tc.ID}
				order = append(order, slot)
			}
			c := byIndex[slot]
			if tc.ID != "" {
				c.ID = tc.ID
			}
			// A provider may repeat the whole function name on every delta;
			// appending it twice would corrupt the call.
			if tc.Name != "" && !strings.Contains(c.Name, tc.Name) {
				c.Name += tc.Name
			}
			c.Arguments += tc.Arguments
		}
	}
	if err := ctx.Err(); err != nil {
		return "", nil, usage, err
	}

	calls := make([]store.ToolCall, 0, len(order))
	for _, slot := range order {
		c := *byIndex[slot]
		c.Arguments = string(store.RawJSON(c.Arguments))
		if c.ID == "" {
			c.ID = fmt.Sprintf("call_%d", slot)
		}
		calls = append(calls, c)
	}
	return text.String(), calls, usage, nil
}

func lastKey(m map[int]*store.ToolCall) int {
	last := -1
	for k := range m {
		if k > last {
			last = k
		}
	}
	return last
}

// shellToolID is the tool whose command shell.create.before rewrites or
// vetoes (§8); every other tool only has the execute hooks.
const shellToolID = "bash"

// dispatch runs one tool call and never panics the session: failures become
// results the model can read, not engine errors.
func (e *Engine) dispatch(ctx context.Context, call store.ToolCall) (out Result) {
	defer func() {
		if r := recover(); r != nil {
			out = Result{Text: fmt.Sprintf("tool %s panicked: %v", call.Name, r)}
		}
		if e.Emit != nil {
			e.Emit(Event{Type: EventToolEnd, Tool: call.Name, Output: out.Text})
		}
	}()
	tool := e.lookup(call.Name)
	if tool == nil {
		return Result{Text: fmt.Sprintf("unknown tool: %s", call.Name)}
	}
	raw := store.RawJSON(call.Arguments)
	if e.Emit != nil {
		e.Emit(Event{Type: EventToolStart, Tool: call.Name, Input: raw})
	}

	// 1. permissions (§11) — rules first, then on a rule miss the plugin
	// hook, which may only tighten (§8). Nil Permissions is the build
	// agent's bypass: no evaluation at all.
	if decision, fromPlugin, ok := e.decision(ctx, call.Name, raw); ok {
		switch decision {
		case permissions.Deny:
			if fromPlugin {
				return Result{Text: "denied by plugin"}
			}
			return Result{Text: "denied by permissions.toml"}
		case permissions.Ask:
			allow, always := false, false
			if e.Ask != nil {
				allow, always = e.Ask(call.Name, raw)
			}
			if !allow {
				return Result{Text: "denied: approval was not granted"}
			}
			if always {
				// "always" persists an allow rule; a matching deny rule
				// still wins and Approve reports that refusal.
				if err := e.Permissions.Approve(call.Name, raw, "always"); err != nil {
					return Result{Text: "approved once, but the always rule did not persist: " + err.Error()}
				}
			}
		}
	}

	// 2. plugin hooks around the call (§8): rewrite the input, and for a
	// shell command let shell.create.before rewrite or veto it.
	input := raw
	if e.Plugins != nil {
		if rew, handled := e.Plugins.ToolBefore(ctx, call.Name, input); handled {
			input = rew
		}
		if call.Name == shellToolID {
			if cmd := commandOf(input); cmd != "" {
				switch sd := e.Plugins.ShellBefore(ctx, cmd); {
				case sd.Handled && sd.Deny:
					if sd.Reason != "" {
						return Result{Text: "denied by plugin: " + sd.Reason}
					}
					return Result{Text: "denied by plugin"}
				case sd.Handled && sd.Command != "":
					input = withCommand(input, sd.Command)
				}
			}
		}
	}

	// 3. execute, then let a plugin augment what the model reads (§8).
	res, err := tool.Execute(ctx, input)
	if err != nil {
		return Result{Text: fmt.Sprintf("error: %v", err)}
	}
	if e.Plugins != nil {
		if aug, handled := e.Plugins.ToolAfter(ctx, call.Name, input, res.Text); handled {
			res.Text = aug
		}
	}
	return res
}

// decision resolves the permission decision for one call: matching rules
// first; a rule miss consults the plugin hook between the miss and the
// default (§8), which can tighten but never loosen. ok=false means there is
// no evaluation to perform (the build agent's nil Permissions).
func (e *Engine) decision(ctx context.Context, tool string, input json.RawMessage) (decision string, fromPlugin, ok bool) {
	if e.Permissions == nil {
		return "", false, false
	}
	if d, matched := e.Permissions.EvalMatch(tool, input); matched {
		return d, false, true
	}
	decision = e.Permissions.Default
	if e.Plugins != nil {
		if d, handled := e.Plugins.PermissionEvaluate(ctx, tool, input); handled && weight(d) > weight(decision) {
			return d, true, true
		}
	}
	return decision, false, true
}

// weight orders the three decisions: deny > ask > allow.
func weight(d string) int {
	switch d {
	case permissions.Deny:
		return 3
	case permissions.Ask:
		return 2
	case permissions.Allow:
		return 1
	}
	return 0
}

// commandOf reads the "command" field of a shell tool input ("" when the
// input is not a command object, so the shell hook is skipped).
func commandOf(input json.RawMessage) string {
	var m map[string]json.RawMessage
	if json.Unmarshal(input, &m) != nil {
		return ""
	}
	var s string
	if json.Unmarshal(m["command"], &s) != nil {
		return ""
	}
	return s
}

// withCommand replaces the "command" field, keeping every other input field.
func withCommand(input json.RawMessage, command string) json.RawMessage {
	var m map[string]json.RawMessage
	if json.Unmarshal(input, &m) != nil {
		return input
	}
	m["command"], _ = json.Marshal(command)
	out, err := json.Marshal(m)
	if err != nil {
		return input
	}
	return out
}

func (e *Engine) lookup(name string) *Tool {
	e.toolsMu.RLock()
	defer e.toolsMu.RUnlock()
	for i := range e.Tools {
		if e.Tools[i].ID == name {
			return &e.Tools[i]
		}
	}
	return nil
}

func (e *Engine) schemas() []providers.ToolSchema {
	e.toolsMu.RLock()
	defer e.toolsMu.RUnlock()
	if len(e.Tools) == 0 {
		return nil
	}
	out := make([]providers.ToolSchema, 0, len(e.Tools))
	for _, t := range e.Tools {
		out = append(out, providers.ToolSchema{Name: t.ID, Description: t.Description, Parameters: t.Schema})
	}
	return out
}

// ReplaceMCP swaps the tool subset namespaced `server__` for tools,
// leaving built-ins and other servers untouched. Called live on a
// tools_list_changed notification (§7 registry swap, row 43); safe
// concurrently with a turn.
func (e *Engine) ReplaceMCP(server string, tools []Tool) {
	prefix := server + "__"
	e.toolsMu.Lock()
	defer e.toolsMu.Unlock()
	keep := make([]Tool, 0, len(e.Tools)+len(tools))
	for _, t := range e.Tools {
		if !strings.HasPrefix(t.ID, prefix) {
			keep = append(keep, t)
		}
	}
	e.Tools = append(keep, tools...)
}

func toProvider(msgs []store.Message) []providers.Message {
	out := make([]providers.Message, 0, len(msgs))
	for _, m := range msgs {
		if m.Role == store.RoleCompaction {
			// Checkpoints ride in the system prompt, never as messages.
			continue
		}
		pm := providers.Message{Role: m.Role, Content: m.Content, ToolCallID: m.ToolCallID}
		for _, med := range m.Media {
			pm.Media = append(pm.Media, providers.Media{Type: med.Type, Data: med.Data, Name: med.Name})
		}
		out = append(out, pm)
	}
	return out
}

// EnvLine is the payload's env line (§11): where and when this conversation
// runs, always between the agent body and AGENTS.md. cwd may be empty, in
// which case the process working directory is used.
func EnvLine(cwd string) string {
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	return "Working directory: " + cwd +
		"\nPlatform: " + runtime.GOOS + "/" + runtime.GOARCH +
		"\nDate: " + time.Now().Format("2006-01-02")
}

// LoadInstructions reads AGENTS.md from the data tree, falling back to the
// built-in default. It is the one instructions file injected as system text.
func LoadInstructions(path string) string {
	if path != "" {
		if b, err := os.ReadFile(path); err == nil {
			if s := strings.TrimSpace(string(b)); s != "" {
				return s
			}
		}
	}
	return defaultSystem
}

const defaultSystem = "You are matcode, a coding agent operating in a terminal."
