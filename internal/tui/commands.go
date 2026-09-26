package tui

import (
	"context"
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"matcode/internal/agents"
	"matcode/internal/app"
	"matcode/internal/cmds"
	"matcode/internal/config"
	"matcode/internal/store"
	"matcode/internal/tui/theme"
)

// loadCommands re-reads commands/*.md from the data tree (row 33) and
// publishes it for the /-list and the palette.
func (a *App) loadCommands() error {
	list, err := cmds.Load(a.cfg.CommandsDirs()...)
	if err != nil {
		return err
	}
	setUserCmds(list)
	return nil
}

// runUserCommand runs a data-tree command as a normal prompt: the body is
// expanded ($ARGUMENTS → what the user typed) and sent verbatim, so the
// transcript shows exactly what the model received. Frontmatter
// agent/model pin the session first through the same engine rebuild the
// pickers use.

// runUserCommand runs a data-tree command as a normal prompt: the body is
// expanded ($ARGUMENTS → what the user typed) and sent verbatim, so the
// transcript shows exactly what the model received. Frontmatter
// agent/model pin the session first through the same engine rebuild the
// pickers use.
func (a *App) runUserCommand(c cmds.Command, args string) (tea.Model, tea.Cmd) {
	t := a.cur()
	if t == nil {
		a.status = "no session"
		return a, nil
	}
	if t.turn != nil {
		a.status = "a turn is already running"
		return a, nil
	}
	if c.Agent != "" || c.Model != "" {
		a.setActive(func(tb *tab) error {
			if c.Agent != "" {
				tb.sess.Meta.Agent = c.Agent
			}
			if c.Model != "" {
				tb.sess.Meta.Model = c.Model
			}
			return nil
		})
		// setActive rolls the meta back and reports the error itself when
		// the pin does not resolve; then there is nothing to run.
		if (c.Agent != "" && t.sess.Meta.Agent != c.Agent) ||
			(c.Model != "" && t.sess.Meta.Model != c.Model) {
			return a, nil
		}
	}
	msg := store.Message{Role: "user", Content: cmds.Expand(c, args)}
	return a, a.startTurn(t, msg)
}

// onReload re-reads config + theme after a data-tree change (row 31).
// The engine of an open tab keeps its resolved model; only display-level
// settings (theme, defaults for new tabs) and the status line move.

// onReload re-reads config + theme after a data-tree change (row 31).
// The engine of an open tab keeps its resolved model; only display-level
// settings (theme, defaults for new tabs) and the status line move.
func (a *App) onReload() (tea.Model, tea.Cmd) {
	cfg, err := config.Load(a.cwd)
	if err != nil {
		a.status = "reload: " + err.Error()
		return a, nil
	}
	a.cfg = cfg
	a.opts.Config = cfg
	if err := agents.Load(cfg.AgentsDirs()...); err != nil {
		a.status = "reload: " + err.Error()
		return a, nil
	}
	// Command files reload with everything else; a broken one keeps the
	// previous list rather than aborting the whole reload.
	if err := a.loadCommands(); err != nil {
		fmt.Fprintf(os.Stderr, "mtc: commands: %s\n", err)
	}
	// Plugins restart against the new data tree (§8): added, edited and
	// removed plugins are picked up without a restart.
	if a.plugins != nil {
		a.plugins.Reload(cfg.PluginsDirs()...)
	}
	// Skills reload with the data trees too (row 37): each idle tab
	// rebuilds its engine so new/edited skills reach both the skill tool
	// and the system prompt. A tab mid-turn keeps its current engine.
	for _, tb := range a.tabs {
		if tb.turn != nil || tb.sess == nil || tb.built == nil {
			continue
		}
		tb.chat.SetShowTokens(cfg.UIShowTokens)
		built, err := app.New(context.Background(), app.Options{
			Cwd:     a.cwd,
			Config:  cfg,
			Agent:   tb.sess.Meta.Agent,
			Model:   tb.sess.Meta.Model,
			Session: tb.sess,
			Plugins: a.plugins,
		})
		if err != nil {
			a.status = "reload: " + err.Error()
			continue
		}
		old := tb.built
		tb.built = built
		a.wire(tb)
		old.Close()
	}
	if cfg.Theme != a.theme.Name {
		if t, terr := theme.Get(cfg.ThemesDir(), cfg.Theme); terr == nil && t.Name != a.theme.Name {
			a.theme = t
			for _, tb := range a.tabs {
				tb.chat.SetTheme(t)
			}
			if a.overlay != nil {
				a.overlay.theme = t
			}
		}
	}
	a.status = "config reloaded"
	return a, nil
}

// onTurnDone flushes the queued prompt and refreshes the transcript.

// cycleModel steps F2 through the configured provider/model pairs.
func (a *App) cycleModel(delta int) {
	t := a.cur()
	if t == nil {
		return
	}
	choices := a.modelChoices()
	if len(choices) == 0 {
		return
	}
	idx := 0
	for i, c := range choices {
		if c == t.sess.Meta.Model {
			idx = i
			break
		}
	}
	a.applyModelChoice(choices[(idx+delta+len(choices))%len(choices)])
}

// cycleAgent steps shift+tab through the built-in agents.

// cycleAgent steps shift+tab through the built-in agents.
func (a *App) cycleAgent(delta int) {
	t := a.cur()
	if t == nil {
		return
	}
	choices := a.agentChoices()
	if len(choices) == 0 {
		return
	}
	idx := 0
	for i, c := range choices {
		if c == t.sess.Meta.Agent {
			idx = i
			break
		}
	}
	a.applyAgentChoice(choices[(idx+delta+len(choices))%len(choices)])
}

// newSession starts a fresh session tab.

// runCommand dispatches a slash / leader / palette command id.
func (a *App) runCommand(id string) (tea.Model, tea.Cmd) {
	t := a.cur()
	switch id {
	case "quit", "exit", "w":
		return a.quit()
	case "new":
		return a.newSession()
	case "sessions":
		a.route = "sessions"
		return a, nil
	case "settings":
		a.route = "settings"
		return a, nil
	case "palette", "help":
		return a.openPalette()
	case "models":
		return a.openPicker("model", "switch model", a.modelChoices())
	case "agent":
		return a.openPicker("agent", "switch agent", a.agentChoices())
	case "themes":
		return a.openPicker("theme", "switch theme", theme.Names(a.cfg.ThemesDir()))
	case "mcp":
		return a.openPicker("mcp", "mcp servers", a.mcpChoices())
	case "recents":
		return a.openRecents()
	case "undo":
		return a.openPicker("undo", "revert to step", a.undoChoices())
	case "redo":
		return a.openPicker("redo", "restore backup", a.redoChoices())
	case "retry":
		if t == nil {
			a.status = "no session"
			return a, nil
		}
		return a, a.startRetry(t)
	case "editor":
		return a.openEditor()
	case "export":
		return a.exportSession()
	case "compact":
		if t != nil {
			if err := t.built.Engine.Compact(); err != nil {
				a.status = err.Error()
			} else if err := t.reload(); err != nil {
				a.status = err.Error()
			} else {
				a.status = "compacted"
			}
		}
		return a, nil
	case "btw":
		return a.openBtw()
	case "details":
		if t != nil {
			a.status = fmt.Sprintf("%s  model=%s  agent=%s  %d messages",
				t.sess.Meta.ID, t.sess.Meta.Model, t.sess.Meta.Agent, len(t.chat.Messages()))
		}
		return a, nil
	case "sidebar":
		return a.toggleSidebar()
	case "tree":
		return a.openTree()
	case "diff":
		return a.openDiff()
	}
	// Not a builtin: the palette and the /-list can both land on a
	// data-tree command, which runs with no arguments of its own there.
	if cm, ok := userCommand(id); ok {
		return a.runUserCommand(cm, "")
	}
	return a, nil
}

// toggleSidebar flips the sidebar widget column (row 35).

// modelChoices lists provider/model pairs from config: every model a
// provider declares (its `models` list) or its default_model alone (row 35
// model picker with variants).
func (a *App) modelChoices() []string {
	var out []string
	for name, p := range a.cfg.Providers {
		// A provider whose credential env var is missing cannot serve a
		// turn; offering it would only produce a red status line.
		if _, err := p.ResolveKey(); err != nil {
			continue
		}
		var models []string
		switch {
		case len(p.Models) > 0:
			models = p.Models
		case p.DefaultModel != "":
			models = []string{p.DefaultModel}
		case strings.HasPrefix(a.cfg.Model, name+"/"):
			// The provider has no default_model of its own; reuse the
			// model the config already addresses it by.
			models = []string{strings.TrimPrefix(a.cfg.Model, name+"/")}
		default:
			// No addressable model — "provider/model" is the only form
			// the engine accepts.
		}
		for _, m := range models {
			if m != "" {
				out = append(out, name+"/"+m)
			}
		}
	}
	sortStrings(out)
	if t := a.cur(); t != nil && t.sess.Meta.Model != "" {
		out = append([]string{t.sess.Meta.Model}, out...)
	}
	return dedupe(out)
}

// agentChoices lists built-in agent ids.

// agentChoices lists built-in agent ids.
func (a *App) agentChoices() []string { return agents.IDs() }

// View renders the active route plus overlays.
