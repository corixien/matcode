// Package tui implements the interactive terminal UI (spec §10): one
// bubbletea app routing chat, session list, settings, and dialog layers.
package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"matcode/internal/agents"
	"matcode/internal/app"
	"matcode/internal/config"
	"matcode/internal/engine"
	"matcode/internal/plugin"
	"matcode/internal/store"
	"matcode/internal/tui/routes"
	"matcode/internal/tui/theme"
)

// Options wires the program: the working directory and the merged
// config; Agent/Model/Session seed the first tab (CLI flags).
type Options struct {
	Cwd     string
	Config  *config.Config
	Agent   string
	Model   string
	Session string
}

// tab is one open session: its own transcript, build, and turn state.
type tab struct {
	sess   *store.Session
	built  *app.Built
	chat   *routes.Chat
	turn   chan struct{} // closed when the running turn ends (nil = idle)
	cancel context.CancelFunc
}

// reload replaces the chat transcript with the persisted one, so stored
// tool messages and compaction checkpoints appear after a turn.
func (t *tab) reload() error {
	msgs, err := t.sess.Messages()
	if err != nil {
		return err
	}
	t.chat.Load(msgs)
	return nil
}

// App is the bubbletea root model.
type App struct {
	opts Options
	cfg  *config.Config
	cwd  string

	tabs    []*tab
	closed  []closedTab
	active  int
	theme   theme.Theme
	leader  leaderState
	route   string // "" (chat) | "sessions" | "settings"
	overlay *overlay
	// dismissed is the composer text whose live overlay the user escaped
	// out of; it must not auto-reopen until the text changes.
	dismissed string

	// pendingMedia carries @-attached media into the next turn.
	pendingMedia []store.Media

	// plugins is the one hook host (§8) shared by every tab; built owns
	// nothing and never closes it — only the TUI does, on quit.
	plugins *plugin.Host

	prog     *tea.Program
	width    int
	height   int
	status   string
	quitting bool

	// session list route state (route == "sessions").
	sessList []store.Meta
	sessSel  int
}

// closedTab remembers a tab closed with ctrl+shift+t reopen in mind (row 27).
type closedTab struct {
	sessID string
	model  string
	agent  string
}

// askAnswer is what the ask dialog sends back to the engine.
type askAnswer struct{ allow, always bool }

// askRequest arrives from the engine goroutine's Ask hook (row 29).
type askRequest struct {
	action string
	input  json.RawMessage
	reply  chan askAnswer
}

// btwResultMsg carries a side-question answer onto the loop (row 25).
type btwResultMsg struct {
	text string
	err  error
}

// turnDoneMsg marks the end of a turn on the loop.
type turnDoneMsg struct {
	tab *tab
	err string
}

// reloadMsg is sent by the data-tree watcher: the config/themes changed
// under us (spec row 31) and the app should re-read them.
type reloadMsg struct{}

// width0 is the width used before the first WindowSizeMsg.
const width0 = 100

// New builds the root model.
func New(opts Options) (*App, error) {
	cfg := opts.Config
	cwd := opts.Cwd
	if cwd == "" {
		cwd = "."
	}
	t, _ := theme.Get(cfg.ThemesDir(), cfg.Theme)
	a := &App{opts: opts, cfg: cfg, cwd: cwd, theme: t}
	// Hook host (§8): one per TUI, reloaded with the config watcher, closed
	// once on quit. Plugins are opt-in, so a missing tree starts silent.
	a.plugins = plugin.Open(cfg.PluginsDirs(), filepath.Join(cfg.DataDir(), "logs"))
	// Agent .md overlays: loaded before the first tab resolves its agent.
	if err := agents.Load(cfg.AgentsDirs()...); err != nil {
		fmt.Fprintf(os.Stderr, "mtc: agents: %s\n", err)
	}
	// Data-tree slash commands (row 33): shown by / and the palette.
	if err := a.loadCommands(); err != nil {
		fmt.Fprintf(os.Stderr, "mtc: commands: %s\n", err)
	}
	if err := a.openFirst(); err != nil {
		if a.plugins != nil {
			a.plugins.Close()
		}
		return nil, err
	}
	// Booting without a credential is allowed (row: /key) — say so once
	// instead of failing, so the fix is discoverable from the status line.
	if t := a.cur(); t != nil && t.built != nil && t.built.Engine.Provider == nil {
		a.status = "no API key — type /key [provider] <key>"
	}
	return a, nil
}

// openFirst builds the initial tab from CLI flags or a fresh session.
func (a *App) openFirst() error {
	sess, err := a.resolveSession(a.opts.Session)
	if err != nil {
		return err
	}
	return a.addTab(sess, a.opts.Agent, a.opts.Model)
}

// resolveSession picks an existing session (flag = id or id prefix, or
// "latest") or returns nil for a fresh one.
func (a *App) resolveSession(ref string) (*store.Session, error) {
	root := a.cfg.SessionsDir()
	switch ref {
	case "", "latest":
		s, err := store.Latest(root)
		if err != nil {
			return nil, nil // no sessions yet: start fresh
		}
		return s, nil
	}
	metas, err := store.List(root)
	if err != nil {
		return nil, err
	}
	for _, m := range metas {
		if m.ID == ref || strings.HasPrefix(m.ID, ref) {
			return store.Open(filepath.Join(root, m.ID))
		}
	}
	return nil, fmt.Errorf("no session matches %q", ref)
}

// addTab builds a session tab: engine, chat route, and wiring.
func (a *App) addTab(sess *store.Session, agent, model string) error {
	if sess == nil {
		var err error
		sess, err = store.Create(a.cfg.SessionsDir(), a.cfg.Model)
		if err != nil {
			return err
		}
	}
	if agent != "" {
		sess.Meta.Agent = agent
	}
	if model != "" {
		sess.Meta.Model = model
	}
	if sess.Meta.Agent == "" {
		sess.Meta.Agent = a.cfg.Agent
	}
	if sess.Meta.Model == "" {
		// The agent's frontmatter model wins over the config default.
		sess.Meta.Model = agents.ModelFor(sess.Meta.Agent, a.cfg.Model)
	}
	if err := sess.Save(); err != nil {
		return err
	}
	built, err := app.New(context.Background(), app.Options{
		Cwd:     a.cwd,
		Config:  a.cfg,
		Agent:   sess.Meta.Agent,
		Model:   sess.Meta.Model,
		Session: sess,
		Plugins: a.plugins,
	})
	if err != nil && sess.Meta.Model != a.cfg.Model {
		// The session remembers a model that is unusable now (its
		// credential env var is gone, provider removed, …). Fall back to
		// the configured default instead of refusing to start.
		sess.Meta.Model = a.cfg.Model
		_ = sess.Save()
		built, err = app.New(context.Background(), app.Options{
			Cwd:     a.cwd,
			Config:  a.cfg,
			Agent:   sess.Meta.Agent,
			Model:   sess.Meta.Model,
			Session: sess,
			Plugins: a.plugins,
		})
	}
	if err != nil {
		return err
	}
	t := &tab{sess: sess, built: built, chat: routes.NewChat(a.theme, sess.Meta)}
	t.chat.SetCwd(a.cwd)
	t.chat.SetShowTokens(a.cfg.UIShowTokens)
	if err := t.reload(); err != nil {
		built.Close()
		return err
	}
	a.tabs = append(a.tabs, t)
	a.active = len(a.tabs) - 1
	a.wire(t)
	a.syncTabStrip()
	return nil
}

// wire hooks the engine callbacks onto the bubbletea message loop.
func (a *App) wire(t *tab) {
	eng := t.built.Engine
	eng.Echo = func(s string) { a.send(&routes.StreamMsg{Text: s}) }
	eng.Emit = func(ev engine.Event) { a.emit(ev) }
	eng.Ask = func(action string, input json.RawMessage) (bool, bool) {
		reply := make(chan askAnswer, 1)
		a.send(&askRequest{action: action, input: input, reply: reply})
		ans, ok := <-reply
		if !ok {
			return false, false
		}
		return ans.allow, ans.always
	}
}

// send delivers a message to the program (no-op before it exists).
func (a *App) send(msg tea.Msg) {
	if a.prog != nil {
		a.prog.Send(msg)
	}
}

// emit maps engine milestones onto chat route messages.
func (a *App) emit(ev engine.Event) {
	switch ev.Type {
	case engine.EventToolStart:
		a.send(&routes.ToolMsg{Kind: "start", Name: ev.Tool, Input: string(ev.Input)})
	case engine.EventToolEnd:
		a.send(&routes.ToolMsg{Kind: "end", Name: ev.Tool, Output: ev.Output})
	case engine.EventUsage:
		if ev.Usage != nil {
			a.send(&routes.UsageMsg{Input: ev.Usage.Input, Output: ev.Usage.Output, Cost: ev.Usage.Cost})
		}
	}
}

// Init starts the program loop.
func (a *App) Init() tea.Cmd { return nil }

// Update is the root reducer.
func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m := msg.(type) {
	case tea.WindowSizeMsg:
		a.width, a.height = m.Width, m.Height
		if t := a.cur(); t != nil {
			t.chat.Resize(m.Height, m.Width)
		}
		return a, nil
	case turnDoneMsg:
		return a.onTurnDone(m)
	case reloadMsg:
		return a.onReload()
	case *askRequest:
		return a.openAsk(m)
	case btwResultMsg:
		if a.overlay != nil && a.overlay.kind == "btw" {
			a.overlay.btwPending = false
			a.overlay.btwAnswer = m.text
			if m.err != nil {
				a.overlay.btwAnswer = "error: " + m.err.Error()
			}
		}
		return a, nil
	case *routes.AppendMsg, *routes.StreamMsg, *routes.TurnStartMsg,
		*routes.ToolMsg, *routes.UsageMsg, *routes.TurnDoneMsg:
		if t := a.cur(); t != nil {
			t.chat.Update(msg)
		}
		return a, nil
	case tea.KeyMsg:
		return a.onKey(m)
	case tea.MouseMsg:
		return a, nil
	}
	// bubbletea v1 has no CSI-u parser: shift+enter (and friends) arrive
	// as an unknown CSI message rather than a KeyMsg (row 26).
	if name := csiKeyName(msg); name != "" {
		return a.csiKey(name)
	}
	return a, nil
}

func (a *App) cur() *tab {
	if a.active < 0 || a.active >= len(a.tabs) {
		return nil
	}
	return a.tabs[a.active]
}

func (a *App) View() string {
	if a.quitting {
		return ""
	}
	body := ""
	switch a.route {
	case "sessions":
		body = a.sessionsView(a.height, a.width)
	case "settings":
		body = a.settingsView(a.height, a.width)
	default:
		if t := a.cur(); t != nil {
			body = t.chat.View(a.height, a.width)
		}
	}
	if a.overlay != nil {
		dialog := a.overlay.view(a.height, a.width)
		body = lipgloss.JoinVertical(lipgloss.Left, body, dialog)
	}
	if a.status != "" {
		body += "\n" + lipgloss.NewStyle().Foreground(
			lipgloss.Color(a.theme.Muted())).Render(truncate(a.status, a.width))
	}
	return body
}

// Run starts the program and blocks until it exits.
func Run(opts Options) error {
	m, err := New(opts)
	if err != nil {
		return err
	}
	m.prog = tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())

	// Hot-reload: a write in the data tree (config.toml, themes/,
	// commands/, agents/) reloads config + theme into the running app
	// (spec row 31).
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if w, werr := config.NewWatcher(opts.Config); werr == nil {
		w.OnChange(func() { m.prog.Send(reloadMsg{}) })
		go w.Start(ctx)
		defer w.Close()
	}

	_, err = m.prog.Run()
	cancel()
	// No dialog can outlive the program: deny a pending approval.
	m.closeAsk()
	for _, t := range m.tabs {
		t.built.Close()
	}
	return err
}

// --- helpers ---

var _ = engine.Event{}
