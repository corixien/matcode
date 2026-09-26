package tui

import (
	"context"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"matcode/internal/app"
	"matcode/internal/config"
	"matcode/internal/engine"
	"matcode/internal/store"
	"matcode/internal/tui/theme"
)

// miniApp couples the Mini reducer to an engine (spec §10 row 30): the
// same streaming callbacks as the full TUI, minus tabs and overlays.
type miniApp struct {
	m    *Mini
	b    *app.Built
	t    *store.Session
	prog *tea.Program
	turn chan struct{}
	cwd  string
	cfg  *config.Config
}

// miniDoneMsg ends a mini turn on the loop.
type miniDoneMsg struct{ err string }

// miniReloadMsg re-reads config after a data-tree change (spec row 31).
type miniReloadMsg struct{}

// RunMini starts `mtc mini` and blocks until exit (ctrl+c or ctrl+d on
// an empty prompt).
func RunMini(ctx context.Context, opts Options) error {
	a, err := New(opts)
	if err != nil {
		return err
	}
	t := a.cur()
	if t == nil {
		return errNoTab
	}
	m := NewMini(a.theme, t.sess.Meta.ID, t.sess.Meta.Model, t.sess.Meta.Agent)
	mm := &miniApp{m: m, b: t.built, t: t.sess, cwd: opts.Cwd, cfg: opts.Config}
	eng := t.built.Engine
	eng.Echo = func(s string) { mm.send(miniDelta{text: s}) }
	eng.Emit = func(ev engine.Event) { mm.emit(ev) }
	// Ask stays nil: mini mode is headless for approvals (deny).
	p := tea.NewProgram(mm, tea.WithAltScreen())
	mm.prog = p
	if w, werr := config.NewWatcher(opts.Config); werr == nil {
		w.OnChange(func() { mm.send(miniReloadMsg{}) })
		go w.Start(ctx)
		defer w.Close()
	}
	go func() {
		<-ctx.Done()
		p.Quit()
	}()
	_, err = p.Run()
	if t.built != nil {
		t.built.Close()
	}
	return err
}

// send pushes a message onto the mini loop.
func (mm *miniApp) send(msg tea.Msg) {
	if mm.prog != nil {
		mm.prog.Send(msg)
	}
}

// emit maps engine events onto mini messages.
func (mm *miniApp) emit(ev engine.Event) {
	switch ev.Type {
	case engine.EventToolStart:
		mm.send(miniTool{Kind: "start", Name: ev.Tool})
	case engine.EventToolEnd:
		mm.send(miniTool{Kind: "end", Name: ev.Tool, Out: ev.Output})
	case engine.EventUsage:
		if ev.Usage != nil {
			mm.send(miniUsage{In: ev.Usage.Input, Out: ev.Usage.Output, Cost: ev.Usage.Cost})
		}
	}
}

// Init starts the loop.
func (mm *miniApp) Init() tea.Cmd { return nil }

// Update applies terminal input and engine messages.
func (mm *miniApp) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch v := msg.(type) {
	case tea.WindowSizeMsg:
		return mm, nil
	case tea.KeyMsg:
		switch v.String() {
		case "ctrl+c":
			return mm, tea.Quit
		case "ctrl+d":
			if mm.m.Prompt() == "" {
				return mm, tea.Quit
			}
			return mm, nil
		case "enter":
			return mm.submit()
		case "backspace":
			p := mm.m.Prompt()
			if r := []rune(p); len(r) > 0 {
				mm.m.SetPrompt(string(r[:len(r)-1]))
			}
		default:
			if v.Type == tea.KeyRunes || v.String() == " " {
				mm.m.SetPrompt(mm.m.Prompt() + string(v.Runes))
			}
		}
		return mm, nil
	case miniDelta, miniTool, miniUsage, miniStart, miniDone:
		mm.m.Update(msg)
		return mm, nil
	case miniDoneMsg:
		mm.m.Update(miniDone{err: v.err})
		return mm, nil
	case miniReloadMsg:
		mm.reload()
		return mm, nil
	}
	return mm, nil
}

// reload re-reads config after a data-tree change and re-themes mini
// (spec row 31: hot reload applies to every TUI entry point).
func (mm *miniApp) reload() {
	cfg, err := config.Load(mm.cwd)
	if err != nil {
		mm.m.Update(miniNote("reload: " + err.Error()))
		return
	}
	mm.cfg = cfg
	if cfg.Theme != mm.m.theme.Name {
		if t, terr := theme.Get(cfg.ThemesDir(), cfg.Theme); terr == nil && t.Name != mm.m.theme.Name {
			mm.m.theme = t
		}
	}
	mm.m.Update(miniNote("config reloaded"))
}

// submit starts a turn when idle (mini has no queue).
func (mm *miniApp) submit() (tea.Model, tea.Cmd) {
	if mm.m.Running() || mm.m.Prompt() == "" {
		mm.m.Submit()
		return mm, nil
	}
	if !mm.m.Submit() {
		return mm, nil
	}
	text := mm.lastPrompt()
	turn := make(chan struct{})
	mm.turn = turn
	// Apply the start state inline: prog.Send blocks until the loop reads
	// it, and the loop is what is executing this Update.
	mm.m.Update(miniStart{})
	go func() {
		defer close(turn)
		ctx := context.Background()
		err := mm.b.Engine.Turn(ctx, text)
		msg := miniDoneMsg{}
		if err != nil {
			msg.err = err.Error()
		}
		mm.send(msg)
	}()
	return mm, nil
}

// lastPrompt is the echoed user line Mini just logged; strip the prefix.
func (mm *miniApp) lastPrompt() string {
	lines := mm.m.Lines()
	if len(lines) == 0 {
		return ""
	}
	s := lines[len(lines)-1]
	if i := strings.Index(s, "you> "); i >= 0 {
		return strings.TrimSpace(s[i+5:])
	}
	return s
}

// View renders the mini screen.
func (mm *miniApp) View() string {
	return mm.m.View(height0, width0)
}

// height0 is the pre-resize height used by mini's first frame.
const height0 = 24

// errNoTab reports a missing first tab.
var errNoTab = errStr("no session tab")

type errStr string

func (e errStr) Error() string { return string(e) }
