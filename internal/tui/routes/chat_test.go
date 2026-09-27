package routes

import (
	"strings"
	"testing"

	"matcode/internal/store"
	"matcode/internal/tui/theme"
)

// The chat route is a pure reducer + renderer: prompt state, submit rules,
// and view assembly. This covers the behavior the integration harness
// exercises end to end (transcript, streaming, footer).

func newTestChat() *Chat {
	return NewChat(theme.Default, store.Meta{ID: "ses_t1", Model: "mockt/t", Agent: "build"})
}

func view(t *testing.T, c *Chat) string {
	t.Helper()
	return c.View(24, 100)
}

func TestChatStartsEmpty(t *testing.T) {
	c := newTestChat()
	if strings.TrimSpace(view(t, c)) == "" {
		t.Fatal("empty chat rendered nothing (want empty transcript + composer)")
	}
	if !strings.Contains(view(t, c), "ses_t1") {
		t.Fatalf("footer lacks session id:\n%s", view(t, c))
	}
}

// containsIn reports whether want appears in s once ANSI escapes and
// whitespace are ignored (glamour wraps and re-styles words mid-line).
func containsIn(s, want string) bool {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b {
			for i < len(s) && s[i] != 'm' {
				i++
			}
			continue
		}
		b.WriteByte(s[i])
	}
	flat := strings.Join(strings.Fields(b.String()), "")
	return strings.Contains(flat, strings.Join(strings.Fields(want), ""))
}

func TestChatSubmitEchoesPrompt(t *testing.T) {
	c := newTestChat()
	c.Update(&AppendMsg{Msg: store.Message{Role: "user", Content: "hello world"}})
	c.Update(&AppendMsg{Msg: store.Message{Role: "assistant", Content: "hi!"}})
	v := view(t, c)
	for _, want := range []string{"hello world", "hi!", "you"} {
		if !containsIn(v, want) {
			t.Fatalf("view missing %q:\n%s", want, v)
		}
	}
}

func TestChatStreamingDraftShows(t *testing.T) {
	c := newTestChat()
	c.Update(&StreamMsg{Text: "streaming "})
	c.Update(&StreamMsg{Text: "reply"})
	if c.streaming != "streaming reply" {
		t.Fatalf("draft = %q", c.streaming)
	}
	if !strings.Contains(view(t, c), "streaming reply") {
		t.Fatalf("draft not visible:\n%s", view(t, c))
	}
	c.Update(&TurnDoneMsg{})
	if c.streaming != "" {
		t.Fatalf("draft survived turn end: %q", c.streaming)
	}
}

func TestChatToolBlocksCollapse(t *testing.T) {
	c := newTestChat()
	c.Update(&ToolMsg{Kind: "start", Name: "bash", Input: `{"cmd":"ls"}`})
	v := view(t, c)
	if !strings.Contains(v, "▸ bash") || !strings.Contains(v, "running") {
		t.Fatalf("live tool block missing:\n%s", v)
	}
	c.Update(&ToolMsg{Kind: "end", Name: "bash", Output: "main.go"})
	v = view(t, c)
	if strings.Contains(v, "(running)") {
		t.Fatalf("finished block still running:\n%s", v)
	}
	c.Update(&TurnDoneMsg{})
	if len(c.live) != 0 {
		t.Fatalf("live blocks not cleared: %v", c.live)
	}
}

func TestChatFooterUsage(t *testing.T) {
	c := newTestChat()
	c.Update(&UsageMsg{Input: 1200, Output: 340, Cost: 0.0021})
	v := view(t, c)
	if !strings.Contains(v, "1200") || !strings.Contains(v, "$0.0021") {
		t.Fatalf("footer lacks usage:\n%s", v)
	}
}

func TestChatMidTurnQueuesPrompt(t *testing.T) {
	c := newTestChat()
	c.SetPrompt("first")
	if msgs := c.Submit(); len(msgs) != 1 || msgs[0].Content != "first" {
		t.Fatalf("submit = %v", msgs)
	}
	c.SetPrompt("second")
	if c.Running() {
		t.Fatal("Running() false before turn start")
	}
	c.Update(&TurnStartMsg{})
	if msgs := c.Submit(); len(msgs) != 0 {
		t.Fatalf("mid-turn submit sent instead of queueing: %v", msgs)
	}
	if c.queued != "second" {
		t.Fatalf("queue = %q", c.queued)
	}
	// Ctrl+Enter never queues: it only commits history.
	c.SetPrompt("third")
	if c.Submit() != nil {
		t.Fatal("ctrl+enter queued")
	}
	if c.queued != "second" {
		t.Fatalf("queue clobbered: %q", c.queued)
	}
	// turn end flushes the queue into a sendable message
	c.Update(&TurnDoneMsg{})
	if c.queued != "" {
		t.Fatalf("queue not flushed: %q", c.queued)
	}
	if c.pending() == nil {
		// the flushed prompt must be returned by the next Submit
		c.SetPrompt("")
		if msgs := c.Submit(); len(msgs) != 1 || msgs[0].Content != "second" {
			t.Fatalf("flushed submit = %v", msgs)
		}
	}
}

func TestChatQueuedPromptVisible(t *testing.T) {
	c := newTestChat()
	c.Update(&TurnStartMsg{})
	c.SetPrompt("queued one")
	c.Submit()
	v := view(t, c)
	if !strings.Contains(v, "queued one") || !strings.Contains(v, "queued") {
		t.Fatalf("queued prompt not visible:\n%s", v)
	}
}

// TestComposerGrowsWithPrompt proves a prompt longer than the prompt
// field wraps onto continuation rows instead of truncating, and that
// the transcript yields the rows the box takes.
func TestComposerGrowsWithPrompt(t *testing.T) {
	c := newTestChat()
	c.SetPrompt(strings.Repeat("word ", 40))

	comp := c.composer(60)
	rows := strings.Split(comp, "\n")
	if len(rows) <= 3 {
		t.Fatalf("prompt field did not grow: %d rows (border/line/border)", len(rows))
	}
	for _, l := range rows {
		if lipglossWidth(l) > 60 {
			t.Fatalf("prompt row too wide (%d): %q", lipglossWidth(l), l)
		}
	}

	const height, width = 30, 60
	v := c.View(height, width)
	if n := strings.Count(v, "\n") + 1; n > height {
		t.Fatalf("frame overflowed: %d rows > %d", n, height)
	}
	if c.PromptTop() != height-len(rows)-2 {
		t.Fatalf("PromptTop = %d, want %d", c.PromptTop(), height-len(rows)-2)
	}
	if c.PromptBottom() > height {
		t.Fatalf("PromptBottom = %d, past the frame height %d", c.PromptBottom(), height)
	}
	for _, l := range strings.Split(v, "\n") {
		if lipglossWidth(l) > width {
			t.Fatalf("view row too wide (%d): %q", lipglossWidth(l), l)
		}
	}
}

// TestComposerContinuationIndent proves a hard line break in the prompt
// renders as a second row indented under the "> " marker.
func TestComposerContinuationIndent(t *testing.T) {
	c := newTestChat()
	c.SetPrompt("first line\nsecond line")
	rows := strings.Split(c.composer(60), "\n")
	if len(rows) != 4 {
		t.Fatalf("rows = %d, want border + 2 lines + border", len(rows))
	}
	if !strings.Contains(rows[1], "> first line") {
		t.Errorf("first row = %q, want the prompt marker", rows[1])
	}
	if !strings.Contains(rows[2], "  second line") {
		t.Errorf("continuation row = %q, want a 2-space indent", rows[2])
	}
}

// TestComposerKeepsTailWhenTallerThanFrame proves the box is capped at
// the rows the frame can spare and keeps the end being typed.
func TestComposerKeepsTailWhenTallerThanFrame(t *testing.T) {
	c := newTestChat()
	c.Resize(10, 60) // composerMax = 10-7 = 3 text rows
	c.SetPrompt("one\ntwo\nthree\nfour\nfive")

	rows := strings.Split(c.composer(60), "\n")
	if len(rows) != 5 { // 3 text rows + top/bottom border
		t.Fatalf("rows = %d, want 5 (3 text rows capped by the frame)", len(rows))
	}
	body := strings.Join(rows, "\n")
	if !strings.Contains(body, "five") {
		t.Errorf("tail of the prompt missing:\n%s", body)
	}
	if strings.Contains(body, "one") {
		t.Errorf("head should have scrolled out:\n%s", body)
	}
}
