package routes

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"matcode/internal/store"
	"matcode/internal/tui/theme"
)

// Chat route messages: the engine's callbacks arrive as these on the
// bubbletea message loop (the TUI wires Echo/Emit → program.Send).
type (
	// AppendMsg carries a store message (loaded or just appended).
	AppendMsg struct{ Msg store.Message }
	// StreamMsg is one assistant text delta.
	StreamMsg struct{ Text string }
	// TurnStartMsg marks a turn running (submit starts queueing).
	TurnStartMsg struct{}
	// TurnDoneMsg ends the turn; Err != "" surfaces a failure line.
	TurnDoneMsg struct{ Err string }
	// ToolMsg is a tool.start / tool.end milestone.
	ToolMsg struct {
		Kind   string // "start" | "end"
		Name   string
		Input  string
		Output string
	}
	// UsageMsg carries token/cost totals for the footer.
	UsageMsg struct {
		Input, Output int
		Cost          float64
	}
)

// Chat is the chat route: transcript + composer + footer (spec §10 rows
// 21, 23, 26). Pure reducer — no I/O — so the app layer owns sessions,
// engines, and dialogs.
type Chat struct {
	meta       store.Meta
	theme      theme.Theme
	list       *MessageList
	prompt     string // composer text
	queued     string // prompt queued behind a running turn
	flushed    string // queued prompt released at turn end, awaiting send
	streaming  string // assistant text still streaming
	live       []ToolBlock
	running    bool
	usage      string
	status     string // right-footer status (model/agent)
	err        string // last turn error, shown in the composer line
	width      int
	height     int
	offset     int // transcript scroll offset (0 = bottom)
	tabs       []Tab
	active     int
	recent     []string
	sidebar    bool   // sidebar widgets visible (row 35)
	cwd        string // workdir the sidebar reads todos.json from
	hideTokens bool   // [ui] show_tokens = false (row 38)
}

// Tab is one open session tab (row 27).
type Tab struct {
	ID    string
	Title string
	Model string
	Agent string
}

// NewChat creates an empty chat bound to a session.
func NewChat(t theme.Theme, meta store.Meta) *Chat {
	return &Chat{
		meta:  meta,
		theme: t,
		list:  NewMessageList(t, 100),
		tabs:  []Tab{{ID: meta.ID, Title: meta.Title, Model: meta.Model, Agent: meta.Agent}},
	}
}

// Load replaces the transcript (session open / tab switch).
func (c *Chat) Load(msgs []store.Message) {
	c.list.Messages = msgs
	c.list.Live = nil
	c.list.Draft = ""
	c.streaming = ""
	c.live = nil
	c.offset = 0
	c.recent = recentFrom(msgs)
}

// recentFrom seeds the recents list (ctrl+o) from the transcript: the
// last 20 distinct user prompts, newest first.
func recentFrom(msgs []store.Message) []string {
	var out []string
	seen := make(map[string]bool, 20)
	for i := len(msgs) - 1; i >= 0 && len(out) < 20; i-- {
		if msgs[i].Role != "user" {
			continue
		}
		text := strings.TrimSpace(msgs[i].Content)
		if text == "" || seen[text] {
			continue
		}
		seen[text] = true
		out = append(out, text)
	}
	return out
}

// Messages returns the transcript.
func (c *Chat) Messages() []store.Message { return c.list.Messages }

// SetTheme swaps the palette and re-renders markdown.
func (c *Chat) SetTheme(t theme.Theme) {
	c.theme = t
	c.list.Theme = t
	c.list.Resize(c.width)
}

// SetMeta updates the footer identity (agent/model switches).
func (c *Chat) SetMeta(m store.Meta) {
	c.meta = m
	if c.active < len(c.tabs) {
		c.tabs[c.active].Model = m.Model
		c.tabs[c.active].Agent = m.Agent
	}
}

// Meta returns the footer identity.
func (c *Chat) Meta() store.Meta { return c.meta }

// SetPrompt replaces the composer text.
func (c *Chat) SetPrompt(s string) { c.prompt = s }

// SetShowTokens toggles the footer's token counters (row 38); cost
// display is unaffected.
func (c *Chat) SetShowTokens(v bool) { c.hideTokens = !v }

// Prompt returns the composer text.
func (c *Chat) Prompt() string { return c.prompt }

// Queued returns the prompt waiting behind the running turn ("" = none).
func (c *Chat) Queued() string { return c.queued }

// Running reports whether a turn is in flight.
func (c *Chat) Running() bool { return c.running }

// pending returns the flushed prompt awaiting send, or nil.
func (c *Chat) pending() *store.Message {
	if c.flushed == "" {
		return nil
	}
	return &store.Message{Role: "user", Content: c.flushed}
}

// Submit commits the composer. Idle: the prompt becomes one message (a
// flushed queue, when one is waiting, goes first). Mid-turn: the prompt
// queues behind the running turn — one deep, never clobbering an
// existing queue — and nothing is returned.
func (c *Chat) Submit() []store.Message {
	if c.flushed != "" {
		msg := store.Message{Role: "user", Content: c.flushed}
		c.flushed = ""
		c.prompt = ""
		return []store.Message{msg}
	}
	if c.prompt == "" {
		return nil
	}
	if c.running {
		if c.queued == "" {
			c.queued = c.prompt
			c.prompt = ""
		}
		return nil
	}
	msg := store.Message{Role: "user", Content: c.prompt}
	c.prompt = ""
	return []store.Message{msg}
}

// Update applies one chat route message.
func (c *Chat) Update(msg any) {
	switch m := msg.(type) {
	case *AppendMsg:
		c.list.Messages = append(c.list.Messages, m.Msg)
	case *StreamMsg:
		c.streaming += m.Text
		c.list.Draft = c.streaming
	case *TurnStartMsg:
		c.running = true
		c.err = ""
	case *TurnDoneMsg:
		c.running = false
		c.streaming = ""
		c.list.Draft = ""
		c.live = nil
		c.list.Live = nil
		c.err = m.Err
		if c.queued != "" {
			c.flushed = c.queued
			c.queued = ""
		}
	case *ToolMsg:
		c.updateTool(m)
	case *UsageMsg:
		c.usage = ""
		// [ui] show_tokens = false keeps the cost but drops the
		// raw token counters (row 38).
		if !c.hideTokens {
			c.usage = fmt.Sprintf("%d in / %d out", m.Input, m.Output)
		}
		if m.Cost > 0 {
			if c.usage != "" {
				c.usage += " | "
			}
			c.usage += fmt.Sprintf("$%.4f", m.Cost)
		}
	}
}

// updateTool keeps the live block list in step with tool milestones.
func (c *Chat) updateTool(m *ToolMsg) {
	switch m.Kind {
	case "start":
		c.live = append(c.live, ToolBlock{Name: m.Name, Input: summarizeJSON(m.Input, 72)})
		c.list.Live = c.live
	case "end":
		for i := range c.live {
			if c.live[i].Name == m.Name && !c.live[i].Done {
				c.live[i].Done = true
				c.live[i].Out = trimForView(m.Output)
				break
			}
		}
		c.list.Live = c.live
	}
}

// Resize adapts the layout to a new terminal size.
func (c *Chat) Resize(height, width int) {
	c.height, c.width = height, width
	c.list.Resize(width)
}

// Scroll moves the transcript viewport (delta lines, clamped at bottom).
func (c *Chat) Scroll(delta int) {
	c.offset += delta
	if c.offset < 0 {
		c.offset = 0
	}
	max := len(c.list.visibleLines(c.width)) - 4
	if c.offset > max {
		c.offset = max
		if c.offset < 0 {
			c.offset = 0
		}
	}
}

// composerLines is how many rows the composer block occupies.
const composerLines = 3

// View renders the route: transcript (+ sidebar widgets), composer,
// footer.
func (c *Chat) View(height, width int) string {
	if height != c.height || width != c.width {
		c.Resize(height, width)
	}
	// -2: one for the footer, one breathing row — the composer is a
	// real 3-line box (border/content/border), so the transcript yields
	// its rows instead of the frame overflowing.
	transcript := height - composerLines - 2
	if transcript < 3 {
		transcript = 3
	}
	c.list.Offset = c.offset
	if c.sidebar && width >= sidebarMinWidth {
		sw := sidebarWidth
		body := lipgloss.JoinHorizontal(lipgloss.Top,
			c.list.View(transcript, width-sw-1),
			" "+sidebarView(c.widgets(), transcript, sw, c.theme))
		return body + "\n" + c.composer(width) + "\n" + c.footer(width)
	}
	return c.list.View(transcript, width) + "\n" + c.composer(width) + "\n" + c.footer(width)
}

// composer renders the prompt (or its first line) with state markers.
func (c *Chat) composer(width int) string {
	prefix := c.themeStyle(c.theme.Colors.Muted).Render("> ")
	body := ""
	switch {
	case c.queued != "":
		body = c.themeStyle(c.theme.Colors.Warning).Render("queued: " + firstLine(c.queued))
	case c.err != "":
		body = c.themeStyle(c.theme.Colors.Error).Render("error: " + firstLine(c.err))
	default:
		text := c.prompt
		if text == "" && c.flushed != "" {
			text = c.flushed + "  (pending)"
		}
		line := firstLine(text)
		if n := strings.Count(text, "\n"); n > 0 && text != "" {
			line += fmt.Sprintf("  [%d lines]", n+1)
		}
		body = c.themeStyle(c.theme.Colors.Primary).Render(line)
	}
	// Boxed prompt: border + padding around the line, full width.
	content := truncate(prefix+body, width-4)
	if d := width - 4 - lipgloss.Width(content); d > 0 {
		content += strings.Repeat(" ", d)
	}
	return lipgloss.NewStyle().
		Border(lipgloss.NormalBorder()).
		BorderForeground(lipgloss.Color(c.theme.Border())).
		Background(lipgloss.Color(c.theme.UserBubble())).
		Padding(0, 1).
		Render(content)
}

// footer renders session/model/agent/usage/status plus the tab strip.
func (c *Chat) footer(width int) string {
	muted := c.themeStyle(c.theme.Colors.Muted)
	left := fmt.Sprintf("%s  %s  %s", c.meta.ID, c.meta.Model, c.meta.Agent)
	if c.usage != "" {
		left += "  " + c.usage
	}
	if len(c.tabs) > 1 {
		var parts []string
		for i, t := range c.tabs {
			label := t.ID
			if t.Title != "" {
				label = t.Title
			}
			if i == c.active {
				parts = append(parts, c.themeStyle(c.theme.Colors.Primary).Bold(true).Render("["+label+"]"))
			} else {
				parts = append(parts, muted.Render(label))
			}
		}
		left = strings.Join(parts, " ") + "  " + left
	}
	right := c.status
	if c.running {
		right = "working…"
	}
	gap := width - lipgloss.Width(left) - lipgloss.Width(right) - 1
	if gap < 1 {
		gap = 1
	}
	return muted.Render(truncate(left, width-lipgloss.Width(right)-2)) +
		strings.Repeat(" ", gap) + muted.Render(right)
}

// themeStyle applies a palette color.
func (c *Chat) themeStyle(color string) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(lipgloss.Color(color))
}

// SetStatus sets the right-footer status text.
func (c *Chat) SetStatus(s string) { c.status = s }

// --- tabs (row 27) ---

// Tabs returns the open tabs.
func (c *Chat) Tabs() []Tab { return c.tabs }

// ActiveTab returns the currently selected tab.
func (c *Chat) ActiveTab() Tab {
	if c.active < len(c.tabs) {
		return c.tabs[c.active]
	}
	return Tab{}
}

// AddTab opens a new tab (returns its index).
func (c *Chat) AddTab(t Tab) int {
	c.tabs = append(c.tabs, t)
	return len(c.tabs) - 1
}

// SetTabs replaces the strip and selects active (the app layer owns the
// real tab list and mirrors it here for the footer).
func (c *Chat) SetTabs(tabs []Tab, active int) {
	c.tabs = append([]Tab(nil), tabs...)
	if active < 0 || active >= len(c.tabs) {
		active = 0
	}
	c.active = active
}

// CycleTab moves the selection by delta (wrapping).
func (c *Chat) CycleTab(delta int) {
	if len(c.tabs) == 0 {
		return
	}
	c.active = (c.active + delta + len(c.tabs)) % len(c.tabs)
}

// SetActive selects a tab by index.
func (c *Chat) SetActive(i int) {
	if i >= 0 && i < len(c.tabs) {
		c.active = i
	}
}

// RemoveTab closes tab i, keeping the selection in range.
func (c *Chat) RemoveTab(i int) {
	if i < 0 || i >= len(c.tabs) || len(c.tabs) == 1 {
		return
	}
	c.tabs = append(c.tabs[:i], c.tabs[i+1:]...)
	if c.active >= len(c.tabs) {
		c.active = len(c.tabs) - 1
	}
}

// --- recents (ctrl+o) ---

// PushRecent records a recently used entry (deduped, most recent first).
func (c *Chat) PushRecent(s string) {
	if s == "" {
		return
	}
	out := []string{s}
	for _, r := range c.recent {
		if r != s {
			out = append(out, r)
		}
	}
	if len(out) > 20 {
		out = out[:20]
	}
	c.recent = out
}

// Recent returns the recents list (most recent first).
func (c *Chat) Recent() []string { return c.recent }
