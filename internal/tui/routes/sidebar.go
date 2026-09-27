package routes

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"matcode/internal/store"
	"matcode/internal/tui/theme"
)

// Widget is one sidebar panel (row 35): a title and its body lines.
// The sidebar is a live read of state the session already has — no
// separate widget config, nothing to keep in sync.
type Widget struct {
	Title string
	Lines []string
}

// sidebarWidth is the sidebar's column budget (before padding).
const sidebarWidth = 30

// sidebarMinWidth is the terminal width below which the sidebar hides
// instead of squeezing the transcript into an unreadable column.
const sidebarMinWidth = 100

// ToggleSidebar flips the sidebar and reports its new state.
func (c *Chat) ToggleSidebar() bool {
	c.sidebar = !c.sidebar
	return c.sidebar
}

// SetCwd gives the sidebar the workdir it reads todos.json from.
func (c *Chat) SetCwd(dir string) { c.cwd = dir }

// widgets builds every panel for the current transcript.
func (c *Chat) widgets() []Widget {
	return buildWidgets(c.meta, c.usage, c.cwd, c.list.Messages)
}

// buildWidgets derives the sidebar panels from the transcript: session
// identity, token/cost usage, the todos.json list, and the files this
// session has touched.
func buildWidgets(meta store.Meta, usage, cwd string, msgs []store.Message) []Widget {
	var out []Widget

	sess := []string{meta.Model, meta.Agent}
	if meta.Title != "" {
		sess = append([]string{meta.Title}, sess...)
	}
	sess = append(sess, plural(len(msgs), "message", "messages"))
	out = append(out, Widget{Title: "session", Lines: sess})

	if usage != "" {
		out = append(out, Widget{Title: "usage", Lines: []string{usage}})
	}

	if lines := todoLines(cwd); len(lines) > 0 {
		out = append(out, Widget{Title: "todos", Lines: lines})
	}
	if files := touchedFiles(msgs); len(files) > 0 {
		out = append(out, Widget{Title: "files", Lines: files})
	}
	return out
}

// todoLines reads <cwd>/todos.json (what todowrite persists) into
// checkbox lines, newest todo file wins; a missing file is no widget.
func todoLines(cwd string) []string {
	if cwd == "" {
		return nil
	}
	b, err := os.ReadFile(filepath.Join(cwd, "todos.json"))
	if err != nil {
		return nil
	}
	var todos []struct {
		Content string `json:"content"`
		Status  string `json:"status"`
	}
	if json.Unmarshal(b, &todos) != nil || len(todos) == 0 {
		return nil
	}
	var out []string
	for _, td := range todos {
		box := "◻"
		switch td.Status {
		case "in_progress":
			box = "▸"
		case "completed":
			box = "✔"
		}
		out = append(out, box+" "+truncate(td.Content, sidebarWidth-4))
	}
	return out
}

// touchedFiles lists distinct paths the write/edit/patch tools touched,
// newest first, at most six.
func touchedFiles(msgs []store.Message) []string {
	seen := map[string]bool{}
	var out []string
	for i := len(msgs) - 1; i >= 0 && len(out) < 6; i-- {
		for _, call := range msgs[i].ToolCalls {
			switch call.Name {
			case "write", "edit", "patch":
			default:
				continue
			}
			var in struct {
				Path string `json:"path"`
			}
			if json.Unmarshal([]byte(call.Arguments), &in) != nil || in.Path == "" || seen[in.Path] {
				continue
			}
			seen[in.Path] = true
			out = append(out, truncate(in.Path, sidebarWidth-4))
		}
	}
	sort.Strings(out)
	return out
}

// plural is "1 message" / "2 messages".
func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

// pad extends s with spaces to n display cells (never truncates).
func pad(s string, n int) string {
	if w := lipgloss.Width(s); w < n {
		return s + strings.Repeat(" ", n-w)
	}
	return s
}

// sidebarView renders the widgets as a height-tall, width-wide column,
// padding empty lines so it can sit next to the transcript. Each widget
// is a bordered panel on the lighter surface (OpenCode-style boxes).
func sidebarView(ws []Widget, height, width int, t theme.Theme) string {
	if width < 12 {
		width = 12
	}
	innerW := width - 4 // 1px margin + border + padding per side
	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(t.Colors.Secondary))
	muted := lipgloss.NewStyle().Foreground(lipgloss.Color(t.Colors.Muted))
	box := lipgloss.NewStyle().
		Border(lipgloss.NormalBorder()).
		BorderForeground(lipgloss.Color(t.Border())).
		Background(lipgloss.Color(t.UserBubble())).
		Padding(0, 1)
	var lines []string
	for _, w := range ws {
		if len(lines) >= height {
			break
		}
		inner := []string{pad(title.Render(truncate(w.Title, innerW)), innerW)}
		for _, l := range w.Lines {
			inner = append(inner, pad(muted.Render(truncate(l, innerW)), innerW))
		}
		lines = append(lines, strings.Split(box.Render(strings.Join(inner, "\n")), "\n")...)
		lines = append(lines, "")
	}
	if len(lines) > height {
		lines = lines[:height]
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}
