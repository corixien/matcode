package tui

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"

	tea "github.com/charmbracelet/bubbletea"

	"matcode/internal/agents"
	"matcode/internal/app"
	"matcode/internal/catalog"
	"matcode/internal/cmds"
	"matcode/internal/config"
	"matcode/internal/providers"
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
	return a.runCommandArgs(id, "")
}

// runCommandArgs is runCommand with the typed arguments ("/key openrouter
// sk-…"); builtins that ignore arguments simply never read them.
func (a *App) runCommandArgs(id, args string) (tea.Model, tea.Cmd) {
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
		m, c := a.openPicker("model", "switch model", a.modelChoices())
		// Open the static list immediately, then widen it with the
		// provider's real catalogue in the background.
		return m, tea.Batch(c, a.fetchModelsLive())
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
	case "provider", "key":
		return a.openProvider(args)
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

// modelChoices lists provider/model pairs from config, grouped by
// provider: providers are walked in name order so the picker reads as
// one block per provider, and each block holds that provider's declared
// models (its `models` list, its default_model, or — for built-ins
// without either — the embedded models.dev catalog). Only providers
// whose credential resolves are offered (row 35 model picker).
func (a *App) modelChoices() []string {
	names := make([]string, 0, len(a.cfg.Providers))
	for name := range a.cfg.Providers {
		names = append(names, name)
	}
	sort.Strings(names)
	var out []string
	for _, name := range names {
		p := a.cfg.Providers[name]
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
			models = catalogModels(name)
		}
		for _, m := range models {
			if m != "" {
				out = append(out, name+"/"+m)
			}
		}
	}
	if t := a.cur(); t != nil && t.sess.Meta.Model != "" {
		out = append([]string{t.sess.Meta.Model}, out...)
	}
	return dedupe(out)
}

// catalogModels falls back to the embedded models.dev snapshot for
// keyed providers that declare no models of their own, keeping only
// chat-capable ids (no image/video/audio/embedding endpoints).
func catalogModels(provider string) []string {
	prov, ok := catalog.LookupProvider(provider)
	if !ok {
		return nil
	}
	var out []string
	for id := range prov.Models {
		if !chatModel(id) {
			continue
		}
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// chatModel reports whether a catalog id looks like a chat model.
func chatModel(id string) bool {
	l := strings.ToLower(id)
	for _, bad := range []string{
		"image", "video", "audio", "tts", "embed", "whisper",
		"moderation", "dall", "veo", "lyria", "transcribe",
	} {
		if strings.Contains(l, bad) {
			return false
		}
	}
	return true
}

// fetchModelsLive asks every keyed provider, in parallel, for the
// model catalogue behind its credential and reports the ids on the
// loop as one message. The request is the same /models call used to
// validate a key, so it costs one round trip per provider and never
// blocks the UI.
func (a *App) fetchModelsLive() tea.Cmd {
	type target struct {
		name string
		p    config.Provider
		key  string
	}
	var todo []target
	for name, p := range a.cfg.Providers {
		key, err := p.ResolveKey()
		if err != nil || key == "" {
			continue
		}
		todo = append(todo, target{name: name, p: p, key: key})
	}
	if len(todo) == 0 {
		return nil
	}
	return func() tea.Msg {
		var (
			wg   sync.WaitGroup
			mu   sync.Mutex
			live = make(map[string][]string, len(todo))
		)
		for _, t := range todo {
			wg.Add(1)
			go func() {
				defer wg.Done()
				ids, err := providers.ListModels(t.p, t.key)
				if err != nil || len(ids) == 0 {
					return // keep whatever the static list says
				}
				mu.Lock()
				live[t.name] = ids
				mu.Unlock()
			}()
		}
		wg.Wait()
		return modelsLiveMsg{live: live}
	}
}

// applyModelsLive swaps the open model picker's items for the merged
// list: providers the fetch reached show their whole catalogue, the
// rest keep their preset ids, and the selection plus query survive the
// swap so typing is never interrupted.
func (a *App) applyModelsLive(live map[string][]string) (tea.Model, tea.Cmd) {
	if len(live) == 0 || a.overlay == nil || a.overlay.kind != "model" {
		return a, nil
	}
	cur := ""
	if t := a.cur(); t != nil {
		cur = t.sess.Meta.Model
	}
	a.overlay.items = mergeLiveModels(a.modelChoices(), live, cur)
	a.overlay.clamp()
	models, providersN := 0, 0
	for _, ids := range live {
		providersN++
		models += len(ids)
	}
	a.status = fmt.Sprintf("live: %d models from %d providers", models, providersN)
	return a, nil
}

// mergeLiveModels rewrites the static picker list so every provider
// the fetch reached contributes its full live catalogue, while the
// ones it could not reach keep their preset ids. Provider order comes
// from the static list (so the menu keeps its grouping) with any
// live-only provider appended alphabetically; the active model stays
// first and duplicates are dropped.
func mergeLiveModels(static []string, live map[string][]string, cur string) []string {
	if len(live) == 0 {
		return static
	}
	var order []string
	seen := make(map[string]bool, len(live))
	see := func(name string) {
		if name != "" && !seen[name] {
			seen[name] = true
			order = append(order, name)
		}
	}
	byProvider := make(map[string][]string, len(live)+1)
	for _, it := range static {
		if i := strings.IndexByte(it, '/'); i > 0 {
			see(it[:i])
			byProvider[it[:i]] = append(byProvider[it[:i]], it)
			continue
		}
		byProvider[""] = append(byProvider[""], it)
	}
	extra := make([]string, 0, len(live))
	for name := range live {
		if !seen[name] {
			extra = append(extra, name)
		}
	}
	sort.Strings(extra)
	order = append(order, extra...)

	var out []string
	for _, name := range order {
		ids, ok := live[name]
		if !ok {
			out = append(out, byProvider[name]...)
			continue
		}
		for _, id := range ids {
			if id != "" && chatModel(id) {
				out = append(out, name+"/"+id)
			}
		}
	}
	out = append(out, byProvider[""]...)
	if cur != "" {
		out = append([]string{cur}, out...)
	}
	return dedupe(out)
}

// agentChoices lists built-in agent ids.
func (a *App) agentChoices() []string { return agents.IDs() }

// View renders the active route plus overlays.
