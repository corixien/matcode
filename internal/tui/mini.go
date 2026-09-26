package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"matcode/internal/tui/routes"
	"matcode/internal/tui/theme"
)

// Mini is the reduced terminal UI behind `mtc mini` (spec §10, row 30):
// one prompt line over a scrolling log — no tabs, no dialogs, no overlays.
// It is a pure reducer over miniMsg values, so the submit/stream rules
// are testable without a terminal.
type Mini struct {
	theme   theme.Theme
	id      string // session id shown in the status line
	model   string
	agent   string
	prompt  string
	lines   []string // transcript log
	running bool
	usage   string
	err     string
}

// miniMsg carries engine callbacks into the mini reducer.
type (
	miniDelta struct{ text string }
	miniStart struct{}
	miniDone  struct{ err string }
	miniNote  string // system note line (e.g. config reloaded)
	miniTool  struct {
		Kind string // "start" | "end"
		Name string
		Out  string
	}
	miniUsage struct {
		In, Out int
		Cost    float64
	}
)

// NewMini creates a mini session view.
func NewMini(t theme.Theme, id, model, agent string) *Mini {
	return &Mini{theme: t, id: id, model: model, agent: agent}
}

// SetPrompt replaces the prompt line.
func (m *Mini) SetPrompt(s string) { m.prompt = s }

// Prompt returns the prompt line.
func (m *Mini) Prompt() string { return m.prompt }

// Running reports whether a turn is in flight.
func (m *Mini) Running() bool { return m.running }

// Lines returns the transcript log (for tests and export).
func (m *Mini) Lines() []string { return m.lines }

// Submit commits the prompt. Idle: the text is logged as an echoed user
// line and true is returned (start a turn). Mid-turn: nothing is sent —
// mini mode has no queue — and false is returned.
func (m *Mini) Submit() bool {
	if m.prompt == "" {
		return false
	}
	if m.running {
		m.prompt = ""
		return false
	}
	m.lines = append(m.lines, m.style(m.theme.Colors.Primary).Bold(true).Render("you> ")+
		m.prompt)
	m.prompt = ""
	m.err = ""
	m.running = true
	return true
}

// Update applies one mini message.
func (m *Mini) Update(msg any) {
	switch v := msg.(type) {
	case miniStart:
		m.running = true
		m.err = ""
	case miniDelta:
		m.lines = append(m.lines, v.text)
	case miniTool:
		switch v.Kind {
		case "start":
			m.lines = append(m.lines, m.style(m.theme.Colors.Tool).Render("▸ "+v.Name+" …"))
		case "end":
			if out := strings.TrimSpace(v.Out); out != "" {
				m.lines = append(m.lines, indent(out, "  "))
			}
		}
	case miniUsage:
		m.usage = fmt.Sprintf("%d in / %d out", v.In, v.Out)
		if v.Cost > 0 {
			m.usage += fmt.Sprintf(" | $%.4f", v.Cost)
		}
	case miniDone:
		m.running = false
		m.err = v.err
		if m.prompt != "" {
			// a prompt typed during the turn is dropped: mini has no queue.
			m.prompt = ""
		}
	case miniNote:
		m.lines = append(m.lines, m.style(m.theme.Colors.Muted).Render("· "+string(v)))
	}
}

// View renders status, the log tail, and the prompt line.
func (m *Mini) View(height, width int) string {
	head := fmt.Sprintf("mini  %s  %s  %s", m.id, m.model, m.agent)
	if m.usage != "" {
		head += "  " + m.usage
	}
	if m.running {
		head += "  working…"
	}
	head = truncate(head, width)
	log := m.tail(height - 2)
	prompt := "> " + m.prompt
	if m.err != "" {
		prompt = "error: " + m.err
	}
	return truncate(head, width) + "\n" + log + "\n" + truncate(prompt, width)
}

// tail returns the last n log lines, wrapped to width.
func (m *Mini) tail(n int) string {
	if n < 1 {
		n = 1
	}
	lines := m.lines
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, wrap(l, width0, n)...)
	}
	if len(out) > n {
		out = out[len(out)-n:]
	}
	return strings.Join(out, "\n")
}

// style maps a palette color to a lipgloss style.
func (m *Mini) style(color string) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(lipgloss.Color(color))
}

// wrap hard-wraps a rendered line to width cells (glamour already wraps
// markdown; raw tool output and echoes still need bounds).
func wrap(s string, width, maxLines int) []string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return []string{""}
	}
	var out []string
	for _, para := range strings.Split(s, "\n") {
		for lipgloss.Width(para) > width {
			cut := cutAt(para, width)
			out = append(out, cut)
			para = para[len(cut):]
			if len(out) >= maxLines {
				return out
			}
		}
		out = append(out, para)
		if len(out) >= maxLines {
			return out
		}
	}
	return out
}

// cutAt splits s at roughly width display cells, on a space when possible.
func cutAt(s string, width int) string {
	cells := 0
	for i, r := range s {
		w := lipgloss.Width(string(r))
		if cells+w > width {
			if i == 0 {
				return string(r)
			}
			return s[:i]
		}
		cells += w
	}
	return s
}

// indent prefixes every line of s with pad.
func indent(s, pad string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = pad + l
	}
	return strings.Join(lines, "\n")
}

// linesFor returns the raw log for export (no styling).
func (m *Mini) linesFor() []string { return m.lines }

// routes import keeps the message types shared with the full TUI in one
// module; compile-time assertion that routes is linked.
var _ = routes.ToolMsg{}
