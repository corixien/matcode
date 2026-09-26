package routes

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"

	"matcode/internal/store"
	"matcode/internal/tui/theme"
)

// ToolBlock is one collapsed tool call the message list renders: name,
// a one-line input summary, and the result tail.
type ToolBlock struct {
	Name  string
	Input string
	Done  bool
	Out   string
}

// MessageList renders the chat transcript: markdown assistant/user text,
// collapsed tool blocks, the live streaming tail, and viewport
// scrolling (spec §10 rows 21, 26). Rendering is a pure function of
// list state, which keeps it testable.
type MessageList struct {
	Messages []store.Message
	Live     []ToolBlock // in-flight blocks of the running turn
	Draft    string      // assistant text still streaming
	Usage    string      // footer: tokens/cost
	Status   string      // right-side footer status
	Offset   int         // lines scrolled up from the bottom
	Sel      int         // selected message index, -1 = none
	Theme    theme.Theme
	Proto    ImageProtocol // inline-image protocol (row 35), "" = placeholder

	md *glamour.TermRenderer
}

// NewMessageList renders markdown at the given width.
func NewMessageList(t theme.Theme, width int) *MessageList {
	return &MessageList{
		Theme: t, Sel: -1, md: renderer(width),
		Proto: DetectImageProtocol(os.Getenv),
	}
}

// renderer builds (or rebuilds) the glamour renderer for a width.
// Palette colors are applied around it (roleStyle, block); glamour
// styles the markdown structure itself.
func renderer(width int) *glamour.TermRenderer {
	if width < 20 {
		width = 20
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle("dark"),
		glamour.WithWordWrap(width-2),
	)
	if err != nil {
		return nil
	}
	return r
}

// Resize re-wraps markdown for a new width.
func (m *MessageList) Resize(width int) { m.md = renderer(width) }

// markdown renders text through glamour, falling back to raw text when
// the renderer is unavailable.
func (m *MessageList) markdown(text string) string {
	if m.md == nil || strings.TrimSpace(text) == "" {
		return text
	}
	out, err := m.md.Render(text)
	if err != nil {
		return text
	}
	return strings.TrimRight(out, "\n")
}

// block renders one tool block, collapsed by default:
//
//	▸ bash: ls -la            (running)
//	▸ read: src/main.go
//
// An expanded block appends its output tail.
func (m *MessageList) block(b ToolBlock, expanded bool) string {
	state := ""
	if !b.Done {
		state = " (running)"
	}
	head := fmt.Sprintf("▸ %s: %s%s", b.Name, b.Input, state)
	style := fg(m.Theme.Colors.Tool)
	if !b.Done {
		style = fg(m.Theme.Colors.Warning)
	}
	out := style.Render(head)
	if expanded && b.Out != "" {
		body := fg(m.Theme.Colors.Muted).
			MaxWidth(80).
			Render(indentTail(b.Out, 2))
		out += "\n" + body
	}
	return out
}

// fg maps a palette color ("#rrggbb" or an ANSI name) to a style.
func fg(color string) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(lipgloss.Color(color))
}

// indentTail prefixes every line of s with n spaces.
func indentTail(s string, n int) string {
	pad := strings.Repeat(" ", n)
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = pad + l
	}
	return strings.Join(lines, "\n")
}

// roleStyle maps a message role to its palette color.
func (m *MessageList) roleStyle(role string) lipgloss.Style {
	switch role {
	case "user":
		return fg(m.Theme.Colors.Primary).Bold(true)
	case "assistant":
		return fg(m.Theme.Colors.Assistant)
	case "tool":
		return fg(m.Theme.Colors.Tool)
	default: // system / compaction
		return fg(m.Theme.Colors.System)
	}
}

// truncate keeps a string within n display cells, appending … when cut.
func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= n {
		return s
	}
	runes := []rune(s)
	cells := 0
	for i, r := range runes {
		w := lipgloss.Width(string(r))
		if cells+w > n-1 {
			return string(runes[:i]) + "…"
		}
		cells += w
	}
	return s
}

// summarizeJSON compresses raw JSON arguments to one readable line.
func summarizeJSON(raw string, max int) string {
	s := strings.TrimSpace(raw)
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > max {
		s = s[:max-1] + "…"
	}
	return s
}

// visibleLines renders every message into display lines.
func (m *MessageList) visibleLines(width int) []string {
	var lines []string
	// Tool results are indexed by call id so their block shows the tail.
	outByID := map[string]string{}
	for _, msg := range m.Messages {
		if msg.Role == "tool" {
			outByID[msg.ToolCallID] = msg.Content
		}
	}
	for i, msg := range m.Messages {
		switch msg.Role {
		case "user":
			label := m.roleStyle("user").Render("you")
			body := m.markdown(msg.Content)
			lines = append(lines, label, indentTail(body, 2), "")
			lines = append(lines, m.mediaBlock(msg, width)...)
		case "assistant":
			if msg.Content != "" {
				body := m.markdown(msg.Content)
				lines = append(lines, indentTail(strings.TrimRight(body, "\n"), 2), "")
			}
			lines = append(lines, m.mediaBlock(msg, width)...)
			for _, call := range msg.ToolCalls {
				tb := ToolBlock{
					Name:  call.Name,
					Input: summarizeJSON(call.Arguments, 72),
					Done:  true,
					Out:   outByID[call.ID],
				}
				expanded := i == m.Sel
				lines = append(lines, strings.Split(m.block(tb, expanded), "\n")...)
				lines = append(lines, "")
			}
		case "tool":
			// Rendered inside its assistant block; a standalone result
			// (no call id) still shows.
			if msg.ToolCallID == "" && msg.Content != "" {
				lines = append(lines, indentTail(trimForView(msg.Content), 2), "")
			}
		default:
			// system + compaction checkpoints: a muted one-liner.
			head := firstLine(msg.Content)
			if head != "" {
				lines = append(lines, m.roleStyle(msg.Role).Render("· "+head))
			}
		}
	}
	for _, b := range m.Live {
		lines = append(lines, m.block(b, true), "")
	}
	if m.Draft != "" {
		lines = append(lines, indentTail(m.Draft, 2))
	}
	return lines
}

// View renders the window of lines ending at the bottom, height tall.
func (m *MessageList) View(height, width int) string {
	lines := m.visibleLines(width)
	if len(lines) > height && height > 0 {
		start := len(lines) - height - m.Offset
		if start < 0 {
			start = 0
		}
		end := start + height
		if end > len(lines) {
			end = len(lines)
		}
		lines = lines[start:end]
	}
	// Anchor to the bottom like a chat transcript: fill the space above
	// the newest line rather than leaving it dangling at the top.
	for len(lines) < height {
		lines = append([]string{""}, lines...)
	}
	return strings.Join(lines, "\n")
}

// Scroll moves the viewport by delta lines (clamped at the bottom).
func (m *MessageList) Scroll(delta, height int) {
	lines := len(m.visibleLines(width0))
	max := lines - height
	if max < 0 {
		max = 0
	}
	m.Offset += delta
	if m.Offset < 0 {
		m.Offset = 0
	}
	if m.Offset > max {
		m.Offset = max
	}
}

// mediaBlock renders a message's attachments (row 35 image output):
// protocol escapes or placeholders, one group per attachment.
func (m *MessageList) mediaBlock(msg store.Message, width int) []string {
	var out []string
	for _, md := range msg.Media {
		out = append(out, m.mediaLines(md, width)...)
	}
	if len(out) > 0 {
		out = append(out, "")
	}
	return out
}

// width0 is the width used for offset math before a resize; exact
// wrapping does not change line counts materially for scroll bounds.
const width0 = 100

// firstLine returns the first non-empty line of s.
func firstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(l); t != "" {
			return t
		}
	}
	return ""
}

// trimForView keeps large tool outputs viewable.
func trimForView(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > 12 {
		lines = append(lines[:12], fmt.Sprintf("… %d more lines", len(lines)-12))
	}
	return strings.Join(lines, "\n")
}
