package tui

import (
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"matcode/internal/store"
)

// sessionsView lists saved sessions with the selection highlighted.
func (a *App) sessionsView(height, width int) string {
	if a.sessList == nil {
		a.sessList = a.loadSessions()
		a.sessSel = 0
	}
	var lines []string
	lines = append(lines, a.titleStyle("sessions  (enter open · esc back)"))
	room := height - 3
	if room < 1 {
		room = 1
	}
	off := 0
	if a.sessSel >= room {
		off = a.sessSel - room + 1
	}
	for i := off; i < len(a.sessList) && i < off+room; i++ {
		m := a.sessList[i]
		label := fmt.Sprintf("%-20s %-14s %-8s %s", m.ID, m.Model, m.Agent, m.Title)
		if i == a.sessSel {
			lines = append(lines, lipgloss.NewStyle().Foreground(lipgloss.Color(a.theme.Primary())).Bold(true).Render("▸ "+truncate(label, width-2)))
		} else {
			lines = append(lines, "  "+truncate(label, width-2))
		}
	}
	if len(a.sessList) == 0 {
		lines = append(lines, "  (no sessions)")
	}
	return strings.Join(lines, "\n")
}

// sessionKey navigates the session list.

// sessionKey navigates the session list.
func (a *App) sessionKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "up", "ctrl+k":
		if a.sessSel > 0 {
			a.sessSel--
		}
	case "down", "ctrl+j":
		if a.sessSel < len(a.sessList)-1 {
			a.sessSel++
		}
	case "enter":
		if a.sessSel < len(a.sessList) {
			id := a.sessList[a.sessSel].ID
			a.sessList = nil
			a.route = ""
			if cur := a.cur(); cur != nil && cur.sess.Meta.ID != id {
				if err := a.addTabAt(id, "", ""); err != nil {
					a.status = err.Error()
				}
			}
		}
	case "esc", "q":
		a.route = ""
		a.sessList = nil
	}
	return a, nil
}

// loadSessions reads the session index newest first.

// loadSessions reads the session index newest first.
func (a *App) loadSessions() []store.Meta {
	metas, err := store.List(a.cfg.SessionsDir())
	if err != nil {
		a.status = err.Error()
		return nil
	}
	return metas
}

// settingsView shows the effective configuration (row 21 footer data).

// settingsView shows the effective configuration (row 21 footer data).
func (a *App) settingsView(height, width int) string {
	t := a.cur()
	rows := []struct{ k, v string }{
		{"config (global)", a.cfg.GlobalDir},
		{"config (project)", dash(a.cfg.ProjectDir)},
		{"model", a.cfg.Model},
		{"agent", a.cfg.Agent},
		{"theme", a.cfg.Theme},
		{"snapshots", boolWord(a.cfg.Snapshots)},
		{"auto-compact", boolWord(a.cfg.AutoCompact)},
		{"context limit", strconv.Itoa(a.cfg.ContextLimit)},
		{"api port", strconv.Itoa(a.cfg.APIPort)},
		{"providers", fmt.Sprint(len(a.cfg.Providers))},
		{"references", fmt.Sprint(len(a.cfg.References))},
	}
	if t != nil {
		rows = append(rows,
			struct{ k, v string }{"session", t.sess.Meta.ID},
			struct{ k, v string }{"session model", t.sess.Meta.Model},
			struct{ k, v string }{"session agent", t.sess.Meta.Agent},
		)
	}
	var lines []string
	lines = append(lines, a.titleStyle("settings  (esc back)"))
	for _, r := range rows {
		lines = append(lines, "  "+truncate(fmt.Sprintf("%-18s %s", r.k, r.v), width-2))
	}
	_ = height
	return strings.Join(lines, "\n")
}

// titleStyle paints a route heading in the theme's primary color.

// titleStyle paints a route heading in the theme's primary color.
func (a *App) titleStyle(text string) string {
	return lipgloss.NewStyle().Foreground(lipgloss.Color(a.theme.Primary())).Bold(true).Render(text)
}

// --- small shared helpers ---

// dash renders an empty value.

// dash renders an empty value.
func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// boolWord renders a flag as yes/no.

// boolWord renders a flag as yes/no.
func boolWord(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// lastAssistantToolStep returns the index of the newest assistant message
// carrying tool calls, or -1.
