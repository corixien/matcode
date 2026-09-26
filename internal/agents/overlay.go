package agents

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"matcode/internal/skills"
)

// overlay state: `<dir>/<id>.md` files layered over the compiled roster.
// The zero state (nothing loaded) leaves Get/IDs at their builtins, so a
// caller that never loads behaves exactly as before.
var (
	ovMu      sync.RWMutex
	overlays  map[string]Agent // merged effective agents by id
	ovOrder   []string         // file-only ids, in load order (global→project)
	ovLoaded  bool
	ovBaseIDs = map[string]bool{} // ids that came from the compiled roster
)

// Load reads every `<dir>/*.md` agent definition, later dirs winning per
// file (project replaces global file-by-file, no deep merge), and swaps the
// resulting registry in atomically. A missing dir is skipped; a read error
// fails the load and keeps the previous registry.
func Load(dirs ...string) error {
	files := map[string]string{}
	var order []string
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".md") {
				names = append(names, e.Name())
			}
		}
		sort.Strings(names)
		for _, name := range names {
			b, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				return err
			}
			id := strings.TrimSuffix(name, filepath.Ext(name))
			if _, seen := files[id]; !seen {
				order = append(order, id)
			}
			files[id] = string(b)
		}
	}

	merged := map[string]Agent{}
	base := map[string]bool{}
	for _, id := range order {
		seed := Agent{ID: id, ToolIDs: []string{"*"}, UseInstructions: true}
		if b, ok := lookupRoster(id); ok {
			seed = b
			base[id] = true
		}
		merged[id] = applyFrontmatter(seed, files[id])
	}

	ovMu.Lock()
	overlays = merged
	ovOrder = order
	ovBaseIDs = base
	ovLoaded = true
	ovMu.Unlock()
	return nil
}

// Loaded reports whether Load has run at least once.
func Loaded() bool {
	ovMu.RLock()
	defer ovMu.RUnlock()
	return ovLoaded
}

// Get returns the agent with the given id.
func Get(id string) (Agent, error) {
	ovMu.RLock()
	a, ok := overlays[id]
	ovMu.RUnlock()
	if ok {
		if a.Disabled {
			return Agent{}, fmt.Errorf("agent %q is disabled", id)
		}
		return a, nil
	}
	if b, ok := lookupRoster(id); ok {
		return b, nil
	}
	return Agent{}, fmt.Errorf("unknown agent %q (available: %s)", id, join(IDs()))
}

// IDs lists every selectable agent id: compiled roster in dispatch order,
// then file-defined agents in load order. Hidden and disabled agents are
// excluded — pickers, listings, and `debug agents` all render from this.
func IDs() []string {
	ovMu.RLock()
	defer ovMu.RUnlock()
	out := make([]string, 0, len(roster)+len(ovOrder))
	add := func(a Agent) {
		if a.Hidden || a.Disabled {
			return
		}
		for _, id := range out {
			if id == a.ID {
				return
			}
		}
		out = append(out, a.ID)
	}
	for _, b := range roster {
		if a, ok := overlays[b.ID]; ok {
			add(a)
			continue
		}
		add(b)
	}
	for _, id := range ovOrder {
		if !ovBaseIDs[id] {
			if a, ok := overlays[id]; ok {
				add(a)
			}
		}
	}
	return out
}

// FromFile reports whether an agent id came from an `.md` definition rather
// than the compiled roster (used by `mtc debug agents`).
func FromFile(id string) (Agent, bool) {
	ovMu.RLock()
	defer ovMu.RUnlock()
	a, ok := overlays[id]
	return a, ok
}

// ModelFor resolves the model for an agent: the agent's frontmatter model
// when set, otherwise the fallback (the session/config model).
func ModelFor(id, fallback string) string {
	if a, err := Get(id); err == nil && a.Model != "" {
		return a.Model
	}
	return fallback
}

func lookupRoster(id string) (Agent, bool) {
	for _, b := range roster {
		if b.ID == id {
			return b, true
		}
	}
	return Agent{}, false
}

// applyFrontmatter folds one agent .md over its seed agent. Only keys the
// file states are applied; the body (when non-empty) replaces the system
// prompt — the §11 overlay rule, same as tools.
func applyFrontmatter(seed Agent, text string) Agent {
	fm, body := skills.SplitFrontmatter(text)
	a := seed
	if b := strings.TrimSpace(strings.TrimLeft(body, "\r\n")); b != "" {
		a.System = b
	}
	if v, ok := fm["model"]; ok && v != "" {
		a.Model = v
	}
	if v, ok := fm["description"]; ok {
		a.Description = v
	}
	if v, ok := fm["tools"]; ok {
		a.ToolIDs = parseTools(v)
	}
	if v, ok := fm["permissions"]; ok {
		a.BypassPermissions = permsBypass(v)
	}
	if v, ok := fm["steps"]; ok {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			a.Steps = n
		}
		// An unparsable steps value is ignored: a typo must not brick
		// every session on this agent.
	}
	if v, ok := fm["hidden"]; ok {
		a.Hidden = parseBool(v)
	}
	if v, ok := fm["disabled"]; ok {
		a.Disabled = parseBool(v)
	}
	// First-class Anthropic thinking (§4): `thinking = true/false/
	// enabled/disabled` and `budget_tokens = N` fold into the
	// request.body.thinking object the provider dialect already sends.
	if v, ok := fm["thinking"]; ok {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true", "yes", "1", "on", "enabled":
			a.setThinking(true)
		case "false", "no", "0", "off", "disabled":
			a.setThinking(false)
		}
	}
	if v, ok := fm["budget_tokens"]; ok {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			a.setThinkingBudget(n)
		}
	}
	if v, ok := fm["color"]; ok {
		a.Color = v
	}
	for k, v := range fm {
		if rest, ok := strings.CutPrefix(k, "request.headers."); ok && rest != "" {
			if a.ReqHeaders == nil {
				a.ReqHeaders = map[string]string{}
			}
			a.ReqHeaders[rest] = v
		}
		if rest, ok := strings.CutPrefix(k, "request.body."); ok && rest != "" {
			if a.ReqBody == nil {
				a.ReqBody = map[string]any{}
			}
			// Typed when the value parses as JSON (0.2, true, "x",
			// {"a":1}), a bare string otherwise.
			var val any
			if err := json.Unmarshal([]byte(v), &val); err != nil {
				val = v
			}
			a.ReqBody[rest] = val
		}
	}
	return a
}

// setThinking flips the Anthropic thinking object (§4) on the request
// body overlay, preserving budget_tokens if one was already set.
func (a *Agent) setThinking(on bool) {
	if a.ReqBody == nil {
		a.ReqBody = map[string]any{}
	}
	th := thinkingObject(a.ReqBody)
	if on {
		th["type"] = "enabled"
	} else {
		th["type"] = "disabled"
	}
	a.ReqBody["thinking"] = th
}

// setThinkingBudget stores thinking.budget_tokens; a budget implies
// thinking is enabled.
func (a *Agent) setThinkingBudget(n int) {
	if a.ReqBody == nil {
		a.ReqBody = map[string]any{}
	}
	th := thinkingObject(a.ReqBody)
	th["type"] = "enabled"
	th["budget_tokens"] = n
	a.ReqBody["thinking"] = th
}

// thinkingObject returns the existing request.body.thinking map (JSON
// round-trips arrive as map[string]any) or a fresh one.
func thinkingObject(body map[string]any) map[string]any {
	if th, ok := body["thinking"].(map[string]any); ok {
		return th
	}
	return map[string]any{}
}

// parseTools turns a frontmatter `tools` value into an allowlist:
// "*" all, "none"/"" none, otherwise a comma- or space-separated list.
func parseTools(v string) []string {
	v = strings.TrimSpace(v)
	switch strings.ToLower(v) {
	case "*":
		return []string{"*"}
	case "", "none":
		return nil
	}
	fields := strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' })
	out := fields[:0]
	for _, f := range fields {
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}

// permsBypass reads the frontmatter `permissions` value. "bypass" (and its
// synonyms) mirrors the build agent; everything else honors permissions.toml.
func permsBypass(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "bypass", "allow-all", "allowall", "all", "true", "yes", "1":
		return true
	default: // honors, default, enforce, false, deny, ask …
		return false
	}
}

func parseBool(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "yes", "1", "on":
		return true
	default:
		return false
	}
}
