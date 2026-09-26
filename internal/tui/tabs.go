package tui

import (
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"

	"matcode/internal/store"
	"matcode/internal/tui/routes"
)

// cycleTab moves the selection (ctrl+tab).
func (a *App) cycleTab(delta int) {
	if len(a.tabs) == 0 {
		return
	}
	a.active = (a.active + delta + len(a.tabs)) % len(a.tabs)
	a.syncTabStrip()
}

// closeTab parks the active tab for a ctrl+shift+t reopen.

// closeTab parks the active tab for a ctrl+shift+t reopen.
func (a *App) closeTab() (tea.Model, tea.Cmd) {
	t := a.cur()
	if t == nil || len(a.tabs) == 1 {
		return a, nil
	}
	a.closed = append(a.closed, closedTab{
		sessID: t.sess.Meta.ID, model: t.sess.Meta.Model, agent: t.sess.Meta.Agent,
	})
	t.built.Close()
	a.tabs = append(a.tabs[:a.active], a.tabs[a.active+1:]...)
	if a.active >= len(a.tabs) {
		a.active = len(a.tabs) - 1
	}
	a.syncTabStrip()
	return a, nil
}

// reopenTab restores the most recently closed tab (ctrl+shift+t).

// reopenTab restores the most recently closed tab (ctrl+shift+t).
func (a *App) reopenTab() (tea.Model, tea.Cmd) {
	if len(a.closed) == 0 {
		a.status = "no closed tab"
		return a, nil
	}
	n := len(a.closed)
	ct := a.closed[n-1]
	a.closed = a.closed[:n-1]
	if err := a.addTabAt(ct.sessID, ct.model, ct.agent); err != nil {
		a.status = err.Error()
	}
	return a, nil
}

// addTabAt reopens a closed session tab.

// addTabAt reopens a closed session tab.
func (a *App) addTabAt(sessID, model, agent string) error {
	sess, err := store.Open(filepath.Join(a.cfg.SessionsDir(), sessID))
	if err != nil {
		return err
	}
	return a.addTab(sess, agent, model)
}

// syncTabStrip mirrors the tab list into every chat footer.

// syncTabStrip mirrors the tab list into every chat footer.
func (a *App) syncTabStrip() {
	strip := make([]routes.Tab, 0, len(a.tabs))
	for _, t := range a.tabs {
		strip = append(strip, routes.Tab{
			ID: t.sess.Meta.ID, Title: t.sess.Meta.Title,
			Model: t.sess.Meta.Model, Agent: t.sess.Meta.Agent,
		})
	}
	for _, t := range a.tabs {
		t.chat.SetTabs(strip, a.active)
	}
}

// cycleModel steps F2 through the configured provider/model pairs.

// newSession starts a fresh session tab.
func (a *App) newSession() (tea.Model, tea.Cmd) {
	// Model is left empty on purpose: addTab resolves it through the
	// agent's frontmatter model first, then the config default.
	sess, err := store.Create(a.cfg.SessionsDir(), "")
	if err != nil {
		a.status = err.Error()
		return a, nil
	}
	if err := a.addTab(sess, a.cfg.Agent, ""); err != nil {
		a.status = err.Error()
	}
	return a, nil
}

// --- commands ---

// runCommand dispatches a slash / leader / palette command id.

// toggleSidebar flips the sidebar widget column (row 35).
func (a *App) toggleSidebar() (tea.Model, tea.Cmd) {
	if t := a.cur(); t != nil {
		if on := t.chat.ToggleSidebar(); on {
			a.status = "sidebar on"
		} else {
			a.status = "sidebar off"
		}
	}
	return a, nil
}

// modelChoices lists provider/model pairs from config: every model a
// provider declares (its `models` list) or its default_model alone (row 35
// model picker with variants).
