package tui

// /mcp dialog (spec §7 lifecycle surface, row 46): list the configured
// servers, toggle them, restart one live, and read its stderr log.

import (
	"os"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"matcode/internal/mcp"
)

// mcpChoices lists configured servers as Describe lines (name, kind,
// state, target) for the picker.
func (a *App) mcpChoices() []string {
	t := a.cur()
	if t == nil || t.built == nil {
		return nil
	}
	servers, err := mcp.Load(mcp.Path(t.built.Cfg.DataDir()))
	if err != nil {
		a.status = err.Error()
		return nil
	}
	names := make([]string, 0, len(servers))
	for n := range servers {
		names = append(names, n)
	}
	sort.Strings(names)
	items := make([]string, 0, len(names))
	for _, n := range names {
		items = append(items, strings.ReplaceAll(mcp.Describe(n, servers[n]), "\t", "   "))
	}
	return items
}

// openMCPActions opens the per-server action menu; the picked server
// rides in the menu kind ("mcp:<name>").
func (a *App) openMCPActions(item string) (tea.Model, tea.Cmd) {
	name := mcpName(item)
	if name == "" {
		return a, nil
	}
	return a.openPicker("mcp:"+name, name, []string{"toggle enabled", "restart", "view log"})
}

// mcpName pulls the server name out of a Describe line (first field).
func mcpName(item string) string {
	fields := strings.Fields(item)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// mcpAction runs one action for the named server.
func (a *App) mcpAction(name, item string) (tea.Model, tea.Cmd) {
	t := a.cur()
	if t == nil || t.built == nil {
		return a, nil
	}
	b := t.built
	switch item {
	case "toggle enabled":
		servers, err := mcp.Load(mcp.Path(b.Cfg.DataDir()))
		if err != nil {
			a.status = err.Error()
			return a, nil
		}
		s, ok := servers[name]
		if !ok {
			a.status = "mcp: no such server " + name
			return a, nil
		}
		on := !s.IsEnabled()
		if err := b.SetMCPEnabled(name, on); err != nil {
			a.status = err.Error()
			return a, nil
		}
		if err := b.RestartMCP(name); err != nil {
			a.status = err.Error()
			return a, nil
		}
		if on {
			a.status = name + " enabled"
		} else {
			a.status = name + " disabled"
		}
	case "restart":
		if err := b.RestartMCP(name); err != nil {
			a.status = err.Error()
			return a, nil
		}
		a.status = name + " restarted"
	case "view log":
		return a.openMCPLog(name)
	}
	return a, nil
}

// openMCPLog shows the tail of a server's stderr log (row 46: stdio
// stderr lands in <data>/logs/mcp-<name>.log). Scrollable like diff.
func (a *App) openMCPLog(name string) (tea.Model, tea.Cmd) {
	t := a.cur()
	if t == nil || t.built == nil {
		return a, nil
	}
	data, err := os.ReadFile(mcp.LogPath(t.built.Cfg.DataDir(), name))
	if err != nil {
		if os.IsNotExist(err) {
			a.status = name + ": no log yet"
		} else {
			a.status = err.Error()
		}
		return a, nil
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > 400 {
		lines = lines[len(lines)-400:]
	}
	o := newOverlay("mcplog", "mcp "+name+" · log", nil, a.theme)
	o.diffLines = lines
	a.overlay = o
	return a, nil
}
