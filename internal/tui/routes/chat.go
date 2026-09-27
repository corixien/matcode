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

	// promptTop is the frame row where the composer box starts and
	// compH its height in rows. View recomputes both on every frame;
	// App reads them back to repaint the composer over an overlay so
	// menu options end behind the prompt field.
	promptTop int
	compH     int
}

// Tab is one open session tab (row 27).
type Tab struct {
	ID    string
	Title string
	Model string
	Agent string
}

// NewChat creates an empty chat bound to a session. The sidebar is on
// by default: it is part of the layout, not a maximized-window extra.
func NewChat(t theme.Theme, meta store.Meta) *Chat {
	return &Chat{
		meta:    meta,
		theme:   t,
		list:    NewMessageList(t, 100),
		tabs:    []Tab{{ID: meta.ID, Title: meta.Title, Model: meta.Model, Agent: meta.Agent}},
		sidebar: true,
	}
}

// PromptTop is the frame row where the composer box starts (0 when the
// route has not rendered a composer yet).
func (c *Chat) PromptTop() int { return c.promptTop }

// PromptBottom is the frame row just past the footer — the region an
// overlay must never paint over.
func (c *Chat) PromptBottom() int { return c.promptTop + c.compH + 1 }

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

// View renders the route: transcript (+ sidebar widgets), composer,
// footer. The composer is measured before the transcript so a long
// prompt can grow the prompt field downward and the transcript simply
// yields the rows that are left.
func (c *Chat) View(height, width int) string {
	if height != c.height || width != c.width {
		c.Resize(height, width)
	}
	comp := c.composer(width)
	compH := strings.Count(comp, "\n") + 1
	// -2: one for the footer, one breathing row.
	transcript := height - compH - 2
	if transcript < 3 {
		transcript = 3
	}
	c.compH = compH
	c.promptTop = transcript
	c.list.Offset = c.offset

	// The sidebar is part of the layout, not a maximized-window
	// extra: it renders at every width the frame can still afford,
	// shrinking with the window. The transcript block is padded to
	// its exact column budget so the sidebar sits flush right instead
	// of hugging whatever the transcript happens to fill.
	sw := sidebarBudget(width)
	tw := width - sw - 1
	if c.sidebar && tw >= 16 {
		head := strings.Join(padLines(strings.Split(c.list.View(transcript, tw), "\n"), tw), "\n")
		side := strings.Join(padLines(strings.Split(
			sidebarView(c.widgets(), transcript, sw, c.theme), "\n"), sw), "\n")
		body := lipgloss.JoinHorizontal(lipgloss.Top, head, " "+side)
		return body + "\n" + comp + "\n" + c.footer(width)
	}
	head := strings.Join(padLines(strings.Split(c.list.View(transcript, width), "\n"), width), "\n")
	return head + "\n" + comp + "\n" + c.footer(width)
}

// padLines widens every row to w (truncating longer ones) so a block
// joins flush against the next one instead of hugging the left edge.
func padLines(lines []string, w int) []string {
	for i, l := range lines {
		l = truncate(l, w)
		if d := w - lipgloss.Width(l); d > 0 {
			l += strings.Repeat(" ", d)
		}
		lines[i] = l
	}
	return lines
}

// composer renders the prompt in a box that grows with the text: a
// prompt longer than one line wraps under the first (indented to the
// prompt marker) and the box gains a row per line, so the end being
// typed always stays visible above the footer. queued/error notices
// stay single-line.
func (c *Chat) composer(width int) string {
	inner := width - 4 // border + padding on each side
	if inner < 4 {
		inner = 4
	}
	prefix := c.themeStyle(c.theme.Colors.Muted).Render("> ")
	var lines []string
	switch {
	case c.queued != "":
		lines = []string{prefix + c.themeStyle(c.theme.Colors.Warning).
			Render("queued: "+firstLine(c.queued))}
	case c.err != "":
		lines = []string{prefix + c.themeStyle(c.theme.Colors.Error).
			Render("error: "+firstLine(c.err))}
	default:
		text := c.prompt
		if text == "" && c.flushed != "" {
			text = c.flushed + "  (pending)"
		}
		w := inner - 2 // "> " on the first row, "  " on continuations
		if w < 8 {
			w = 8
		}
		lines = wrapText(text, w)
		if len(lines) == 0 {
			lines = []string{""}
		}
		if max := c.composerMax(); len(lines) > max {
			lines = lines[len(lines)-max:] // keep the end: that is the typed part
		}
		styled := make([]string, len(lines))
		for i, l := range lines {
			if i == 0 {
				styled[i] = prefix + c.themeStyle(c.theme.Colors.Primary).Render(l)
			} else {
				styled[i] = c.themeStyle(c.theme.Colors.Primary).Render("  " + l)
			}
		}
		lines = styled
	}
	for i, l := range lines {
		l = truncate(l, inner)
		if d := inner - lipgloss.Width(l); d > 0 {
			l += strings.Repeat(" ", d)
		}
		lines[i] = l
	}
	return lipgloss.NewStyle().
		Border(lipgloss.NormalBorder()).
		BorderForeground(lipgloss.Color(c.theme.Border())).
		Background(lipgloss.Color(c.theme.UserBubble())).
		Padding(0, 1).
		Render(strings.Join(lines, "\n"))
}

// composerMax is how many text rows the composer may hold: the frame
// keeps at least three transcript rows plus the footer.
func (c *Chat) composerMax() int {
	if c.height <= 0 {
		return 20
	}
	if m := c.height - 7; m > 0 {
		return m
	}
	return 1
}

// wrapText wraps s at w columns on word boundaries and keeps hard line
// breaks; a single word longer than w is split rather than overflowing.
func wrapText(s string, w int) []string {
	var out []string
	for _, para := range strings.Split(s, "\n") {
		runes := []rune(para)
		for len(runes) > w {
			cut := 0
			for i := w; i > w/2; i-- { // prefer a break at a space
				if runes[i] == ' ' {
					cut = i
					break
				}
			}
			if cut == 0 {
				cut = w
			}
			out = append(out, string(runes[:cut]))
			runes = runes[cut:]
			for len(runes) > 0 && runes[0] == ' ' {
				runes = runes[1:]
			}
		}
		out = append(out, string(runes))
	}
	return out
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
