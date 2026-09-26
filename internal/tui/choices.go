package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"matcode/internal/app"
	"matcode/internal/attach"
	"matcode/internal/tui/theme"
)

// undoChoices lists reversible steps, newest first: each entry is the
// assistant tool-call message id to revert to (row 28).
func (a *App) undoChoices() []string {
	t := a.cur()
	if t == nil {
		return nil
	}
	msgs, err := t.sess.Messages()
	if err != nil {
		return nil
	}
	var out []string
	for i := len(msgs) - 1; i >= 1; i-- {
		if msgs[i].Role == "assistant" && len(msgs[i].ToolCalls) > 0 {
			out = append(out, msgs[i].ID)
		}
		if len(out) >= 20 {
			break
		}
	}
	return out
}

// redoChoices lists transcript backups holding more messages than live.

// redoChoices lists transcript backups holding more messages than live.
func (a *App) redoChoices() []string {
	t := a.cur()
	if t == nil {
		return nil
	}
	live, err := t.sess.Messages()
	if err != nil {
		return nil
	}
	var out []string
	for _, p := range globBackups(t.sess.Dir) {
		if countLines(p) > len(live) {
			out = append(out, filepath.Base(p))
		}
	}
	return out
}

// modelChoices-free appliers: swapping model/agent rebuilds the engine.

// applyModelChoice switches the active tab's model.

// applyModelChoice switches the active tab's model.
func (a *App) applyModelChoice(choice string) (tea.Model, tea.Cmd) {
	if strings.HasPrefix(choice, a.currentModel()) {
		return a, nil
	}
	return a.setActive(func(t *tab) error {
		t.sess.Meta.Model = choice
		return nil
	})
}

// applyAgentChoice switches the active tab's agent.

// applyAgentChoice switches the active tab's agent.
func (a *App) applyAgentChoice(choice string) (tea.Model, tea.Cmd) {
	return a.setActive(func(t *tab) error {
		t.sess.Meta.Agent = choice
		return nil
	})
}

// currentModel returns the active tab's model ("" when none).

// currentModel returns the active tab's model ("" when none).
func (a *App) currentModel() string {
	if t := a.cur(); t != nil {
		return t.sess.Meta.Model
	}
	return ""
}

// setActive applies a meta change and rebuilds the tab's engine so the
// next turn uses it.

// setActive applies a meta change and rebuilds the tab's engine so the
// next turn uses it.
func (a *App) setActive(fn func(*tab) error) (tea.Model, tea.Cmd) {
	t := a.cur()
	if t == nil {
		return a, nil
	}
	prev := t.sess.Meta
	if err := fn(t); err != nil {
		a.status = err.Error()
		return a, nil
	}
	if err := t.sess.Save(); err != nil {
		a.status = err.Error()
		return a, nil
	}
	old := t.built
	built, err := app.New(context.Background(), app.Options{
		Cwd: a.cwd, Config: a.cfg,
		Agent: t.sess.Meta.Agent, Model: t.sess.Meta.Model, Session: t.sess,
		Plugins: a.plugins,
	})
	if err != nil {
		// Roll back so the footer never disagrees with the session file.
		t.sess.Meta = prev
		_ = t.sess.Save()
		a.status = err.Error()
		return a, nil
	}
	t.built = built
	if old != nil {
		old.Close()
	}
	a.wire(t)
	t.chat.SetMeta(t.sess.Meta)
	a.syncTabStrip()
	a.status = t.sess.Meta.Model + " · " + t.sess.Meta.Agent
	return a, nil
}

// applyThemeChoice swaps the palette for every open tab.

// applyThemeChoice swaps the palette for every open tab.
func (a *App) applyThemeChoice(name string) (tea.Model, tea.Cmd) {
	t, err := theme.Get(a.cfg.ThemesDir(), name)
	if err != nil {
		a.status = err.Error()
		return a, nil
	}
	a.theme = t
	a.cfg.Theme = name
	for _, tb := range a.tabs {
		tb.chat.SetTheme(t)
	}
	if a.overlay != nil {
		a.overlay.theme = t
	}
	a.status = "theme: " + name
	return a, nil
}

// applyUndo reverts the transcript to the message before the picked
// tool step and restores that step's file snapshots.

// applyUndo reverts the transcript to the message before the picked
// tool step and restores that step's file snapshots.
func (a *App) applyUndo(stepID string) (tea.Model, tea.Cmd) {
	t := a.cur()
	if t == nil {
		return a, nil
	}
	msgs, err := t.sess.Messages()
	if err != nil {
		a.status = err.Error()
		return a, nil
	}
	idx := -1
	for i, m := range msgs {
		if m.ID == stepID {
			idx = i
			break
		}
	}
	if idx < 1 {
		a.status = "nothing to undo"
		return a, nil
	}
	kept, _, err := t.sess.Revert(msgs[idx-1].ID)
	if err != nil {
		a.status = err.Error()
		return a, nil
	}
	files := restoreStepFiles(a.cfg, t.sess, msgs[idx].ID, false)
	if err := t.reload(); err != nil {
		a.status = err.Error()
		return a, nil
	}
	a.status = fmt.Sprintf("undone: %d messages kept, %d files restored", kept, files)
	return a, nil
}

// applyRedo re-applies an undone step from its transcript backup.

// applyRedo re-applies an undone step from its transcript backup.
func (a *App) applyRedo(name string) (tea.Model, tea.Cmd) {
	t := a.cur()
	if t == nil {
		return a, nil
	}
	n := 0
	if i := strings.LastIndex(name, "."); i >= 0 {
		n, _ = strconv.Atoi(name[i+1:])
	}
	if n <= 0 {
		a.status = "bad backup name " + name
		return a, nil
	}
	if err := t.sess.Restore(n); err != nil {
		a.status = err.Error()
		return a, nil
	}
	files := -1
	if msgs, err := t.sess.Messages(); err == nil {
		if step := lastAssistantToolStep(msgs); step >= 0 {
			files = restoreStepFiles(a.cfg, t.sess, msgs[step].ID, true)
		}
	}
	if err := t.reload(); err != nil {
		a.status = err.Error()
		return a, nil
	}
	a.status = fmt.Sprintf("redone (%d files restored)", files)
	return a, nil
}

// --- attach (row 22) ---

// attachMention resolves the picked "@file" reference — optionally with
// a "#start-end" line range — and adds its content to the composer.

// attachMention resolves the picked "@file" reference — optionally with
// a "#start-end" line range — and adds its content to the composer.
func (a *App) attachMention(sel string) (tea.Model, tea.Cmd) {
	t := a.cur()
	if t == nil {
		return a, nil
	}
	prompt := t.chat.Prompt()
	tok, idx := mentionQuery(prompt)
	if idx < 0 {
		return a, nil
	}
	// "@path#10-20" → file://path?start=10&end=20. The range rides on
	// either the typed token or the picked item; the item wins.
	path, rng := sel, ""
	if i := strings.IndexByte(sel, '#'); i >= 0 {
		path, rng = sel[:i], sel[i+1:]
	} else if i := strings.IndexByte(tok, '#'); i >= 0 {
		rng = tok[i+1:]
	}
	abs, err := filepath.Abs(filepath.Join(a.cwd, path))
	if err != nil {
		a.status = err.Error()
		return a, nil
	}
	ref := abs
	if rng != "" {
		parts := strings.SplitN(rng, "-", 2)
		start := strings.TrimSpace(parts[0])
		end := start
		if len(parts) == 2 {
			end = strings.TrimSpace(parts[1])
		}
		ref = "file://" + abs + "?start=" + start + "&end=" + end
	}
	att, err := attach.Resolve(a.cwd, ref, a.mediaConfig())
	if err != nil {
		a.status = err.Error()
		return a, nil
	}
	base := prompt[:idx]
	if att.Text != "" {
		base += "@" + path + "\n\n" + strings.TrimSpace(att.Text)
	} else {
		base += "@" + sel
	}
	t.chat.SetPrompt(base)
	a.pendingMedia = append(a.pendingMedia, att.Media...)
	return a, nil
}

// --- sessions / settings routes ---

// sessionsView lists saved sessions with the selection highlighted.
