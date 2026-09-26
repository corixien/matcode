// Package app assembles a runnable engine from config: agent, model,
// permissions, skills, MCP tools, snapshots. `mtc run` and the HTTP API
// (§9) share it so every surface goes through one engine.
package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"matcode/internal/agents"
	"matcode/internal/catalog"
	"matcode/internal/config"
	"matcode/internal/engine"
	"matcode/internal/mcp"
	"matcode/internal/permissions"
	"matcode/internal/plugin"
	"matcode/internal/providers"
	"matcode/internal/references"
	"matcode/internal/skills"
	"matcode/internal/snapshots"
	"matcode/internal/store"
	"matcode/internal/tools"
)

// Options selects what to build. Cwd and Config are required; everything
// else falls back to the config file's values.
type Options struct {
	Cwd     string
	Config  *config.Config
	Agent   string             // explicit agent id ("" = config agent, then build)
	Model   string             // explicit provider/model ("" = config model)
	Session *store.Session     // attach + snapshot capture when non-nil
	Echo    func(string)       // assistant text deltas (nil discards)
	Emit    func(engine.Event) // milestones (nil discards)
	// Plugins is the shared hook host (§8). nil makes this build own one
	// (opened here, closed by Built.Close); a caller-owned host is left
	// open so the TUI/server can reload it in place.
	Plugins *plugin.Host
}

// Built is an assembled engine plus what the caller still needs.
type Built struct {
	Engine *engine.Engine
	Agent  agents.Agent
	Model  string // resolved provider/model
	Cfg    *config.Config
	Cwd    string
	// Clients holds the live MCP connections, keyed by dial order;
	// RestartMCP swaps an entry in place (§7 lifecycle, rows 43/46).
	Clients []*mcp.Client
	cleanup func()
}

// Close releases MCP clients and the dial timeout.
func (b *Built) Close() {
	if b.cleanup != nil {
		b.cleanup()
		b.cleanup = nil
	}
}

// New resolves agent + provider, loads permissions/skills/MCP, and returns a
// ready engine. A failing MCP server warns on stderr and never fails the
// build (§7 lifecycle).
func New(ctx context.Context, o Options) (*Built, error) {
	cfg := o.Config
	if cfg == nil {
		return nil, fmt.Errorf("app: config is required")
	}
	agentID := o.Agent
	if agentID == "" {
		agentID = cfg.Agent
	}
	if agentID == "" {
		agentID = "build"
	}
	// Agent .md overlays (§11): cheap, idempotent, and re-read on reload.
	if err := agents.Load(cfg.AgentsDirs()...); err != nil {
		fmt.Fprintf(os.Stderr, "mtc: agents: %s\n", err)
	}
	ag, err := agents.Get(agentID)
	if err != nil {
		return nil, err
	}
	// Resolution: explicit flag > agent frontmatter model > config default.
	model := o.Model
	if model == "" {
		model = ag.Model
	}
	if model == "" {
		model = cfg.Model
	}
	if model == "" {
		return nil, fmt.Errorf("no model configured: set model in config.toml or pass -model")
	}
	provider, resolved, err := providers.For(cfg, model)
	if err != nil {
		return nil, err
	}

	instructions, _ := cfg.Find("AGENTS.md")
	// §11 payload order: agent body + env line + AGENTS.md (instructions
	// only when the agent asks for them — never prepended over the body).
	system := ag.System + "\n\n" + engine.EnvLine(o.Cwd)
	if ag.UseInstructions {
		system += "\n\n" + engine.LoadInstructions(instructions)
	}
	if !ag.Stateless {
		references.Sync(cfg.DataDir(), cfg.References)
		system += references.Block(cfg.References)
	}

	// Permissions resolve before the tool list: a denied skill must never be
	// advertised (§11). The build agent bypasses evaluation entirely.
	var perms *permissions.Set
	if !ag.Stateless && !ag.BypassPermissions {
		permsPath, _ := cfg.Find("permissions.toml")
		p, err := permissions.Load(permsPath)
		if err != nil {
			return nil, fmt.Errorf("permissions: %w", err)
		}
		p.WorkDir = o.Cwd // anchors the external_directory check
		perms = p
	}

	skillSet, err := SkillsFor(cfg, perms)
	if err != nil {
		return nil, err
	}

	// MCP: dial enabled servers; failures warn, never abort. Each
	// server dials under its own DialTimeout (tools.go); mcpCtx is the
	// lifetime ctx, cancelled in cleanup.
	mcpCtx, mcpCancel := context.WithCancel(ctx)
	clients, mcpErrs, err := mcp.LoadEnabled(mcpCtx, cfg.DataDir(), nil)
	if err != nil {
		mcpCancel()
		return nil, err
	}
	for name, e := range mcpErrs {
		fmt.Fprintf(os.Stderr, "mtc: mcp %s: %s\n", name, e)
	}

	// Auto-compact budget: an explicit context_limit wins; otherwise the
	// embedded models.dev catalog window for the resolved model (spec §4).
	contextLimit := cfg.ContextLimit
	if !cfg.ContextLimitSet {
		if n := catalog.Context(resolved); n > 0 {
			contextLimit = n
		}
	}
	eng := &engine.Engine{
		Provider:     provider,
		Model:        resolved,
		System:       system,
		Tools:        withMCP(mcpCtx, ag.Tools(o.Cwd, cfg.DataDir(), cfg.Formatter, skillSet, tools.User(o.Cwd, cfg.ToolsDirs()...)), clients),
		Echo:         o.Echo,
		Emit:         o.Emit,
		AutoCompact:  cfg.AutoCompact,
		ContextLimit: contextLimit,
		Permissions:  perms,
		Skills:       skillSet,
	}

	// Plugins (§8): one host per surface. A caller-supplied host stays the
	// caller's to reload and close; otherwise we open one and close it with
	// the MCP clients. A failing host warns and never fails the build.
	plugins := o.Plugins
	closePlugins := false
	if plugins == nil {
		plugins = plugin.Open(cfg.PluginsDirs(), filepath.Join(cfg.DataDir(), "logs"))
		closePlugins = true
	}
	eng.Plugins = plugins
	// Live MCP registry swap (§7, row 43): a tools_list_changed
	// notification re-lists that server's tools and replaces its
	// `server__` slice without interrupting the turn.
	for _, c := range clients {
		c := c
		c.OnToolsChanged = func() { reloadMCPTools(eng, c) }
	}
	if ag.Steps > 0 {
		eng.MaxRounds = ag.Steps
	}
	eng.RequestHeaders = ag.ReqHeaders
	eng.RequestBody = ag.ReqBody
	// §11 step 5: the compaction agent runs only when the deterministic
	// extractor throws — the built-in roster always resolves, and a user
	// `data/agents/compaction.md` overrides the prompt.
	if cag, cErr := agents.Get("compaction"); cErr == nil {
		eng.CompactionLLM = func(ctx context.Context, prompt string) (string, error) {
			return eng.OneshotSystem(ctx, cag.System, prompt)
		}
	}
	if o.Session != nil {
		eng.Session = o.Session
		if cfg.Snapshots {
			eng.Snapshots = snapshots.New(o.Cwd,
				filepath.Join(cfg.DataDir(), "objects"),
				snapshots.Path(o.Session.Dir),
				cfg.DataDir())
		}
		// Row 40: after the session's first turn, generate title.txt
		// and summary.txt once each, best effort.
		if !ag.Stateless {
			sess := o.Session
			done := false
			eng.AfterTurn = func(ctx context.Context) {
				if done {
					return
				}
				done = true
				generateMeta(ctx, eng, sess)
			}
		}
	}
	b := &Built{
		Engine: eng, Agent: ag, Model: resolved, Cfg: cfg, Cwd: o.Cwd,
		Clients: clients,
	}
	// cleanup reads b.Clients at call time: a /mcp restart (row 46)
	// replaces entries, and the final Close must reap the new ones.
	b.cleanup = func() {
		mcp.CloseAll(b.Clients)
		mcpCancel()
		if closePlugins {
			plugins.Close()
		}
	}
	return b, nil
}

// UseSession attaches a session (opened/created after the build) and its
// snapshot capture — the stateless path never calls it.
func (b *Built) UseSession(s *store.Session) {
	b.Engine.Session = s
	if b.Cfg.Snapshots {
		b.Engine.Snapshots = snapshots.New(b.Cwd,
			filepath.Join(b.Cfg.DataDir(), "objects"),
			snapshots.Path(s.Dir),
			b.Cfg.DataDir())
	}
}

// SkillsFor loads the skill data trees (project overriding global) and hides
// what permissions deny (§11). Identical for run and API surfaces.
func SkillsFor(cfg *config.Config, perms *permissions.Set) (*skills.Set, error) {
	set, err := skills.Load(cfg.SkillDirs()...)
	if err != nil {
		return nil, fmt.Errorf("skills: %w", err)
	}
	if perms == nil {
		return set, nil
	}
	return set.Without(func(id string) bool {
		raw, err := json.Marshal(map[string]string{"id": id})
		if err != nil {
			return false
		}
		return perms.Eval("skill", raw) == permissions.Deny
	}), nil
}

// withMCP appends the tools of every dialed server to the agent's list,
// letting an MCP tool shadow one of the same id (last registration wins).
// Servers whose tools/list fails are skipped with a warning.
func withMCP(ctx context.Context, list []engine.Tool, clients []*mcp.Client) []engine.Tool {
	for _, c := range clients {
		mt, err := mcp.EngineTools(ctx, c)
		if err != nil {
			fmt.Fprintf(os.Stderr, "mtc: mcp %s: tools/list: %s\n", c.Name, err)
			continue
		}
		list = tools.Merge(list, mt)
	}
	return list
}

// reloadMCPTools re-lists one server's tools and swaps them into the
// registry (§7 live swap, row 43). Fired async by mcp.Client, bounded
// at 15s, best effort — a failing refresh keeps the old tools.
func reloadMCPTools(eng *engine.Engine, c *mcp.Client) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	mt, err := mcp.EngineTools(ctx, c)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mtc: mcp %s: tools/list refresh: %s\n", c.Name, err)
		return
	}
	eng.ReplaceMCP(c.Name, mt)
}

// generateMeta writes title.txt and summary.txt after a session's first
// turn (row 40): one stateless generation per file, best effort — a
// failure leaves the file absent and never affects the turn itself.
func generateMeta(ctx context.Context, eng *engine.Engine, sess *store.Session) {
	msgs, err := sess.Messages()
	if err != nil || len(msgs) == 0 {
		return
	}
	if sess.Meta.Title == "" {
		if text := firstUserMsg(msgs, 2000); text != "" {
			if ag, err := agents.Get("title"); err == nil {
				if title, err := eng.OneshotSystem(ctx, ag.System, text); err == nil && title != "" {
					_ = sess.SetTitle(title)
				}
			}
		}
	}
	if _, err := os.Stat(filepath.Join(sess.Dir, "summary.txt")); err != nil {
		if text := transcriptTail(msgs, 6000); text != "" {
			if ag, err := agents.Get("summary"); err == nil {
				if sum, err := eng.OneshotSystem(ctx, ag.System, text); err == nil && sum != "" {
					_ = sess.SetSummary(sum)
				}
			}
		}
	}
}

// firstUserMsg is the earliest non-empty user message, capped at n runes.
func firstUserMsg(msgs []store.Message, n int) string {
	for _, m := range msgs {
		if m.Role == "user" && m.Content != "" {
			return capRunes(m.Content, n)
		}
	}
	return ""
}

// transcriptTail joins the messages as "role: content" blocks for the
// summary generator, capped at n runes (head + tail kept).
func transcriptTail(msgs []store.Message, n int) string {
	var b strings.Builder
	for _, m := range msgs {
		if m.Content == "" && len(m.ToolCalls) == 0 {
			continue
		}
		b.WriteString(m.Role)
		b.WriteString(": ")
		b.WriteString(m.Content)
		for _, tc := range m.ToolCalls {
			b.WriteString("\n[tool call: ")
			b.WriteString(tc.Name)
			b.WriteString("]")
		}
		b.WriteString("\n\n")
	}
	return capRunes(b.String(), n)
}

// capRunes truncates s to roughly n runes, keeping both ends.
func capRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n < 8 {
		return string(r[:n])
	}
	head := n / 2
	tail := n - head - 1
	return string(r[:head]) + "\n...\n" + string(r[len(r)-tail:])
}
