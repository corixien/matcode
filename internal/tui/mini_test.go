package tui

import (
	"strings"
	"testing"

	"matcode/internal/tui/theme"
)

func newTestMini() *Mini {
	return NewMini(theme.Default, "ses_x", "mockt/t", "build")
}

func TestMiniSubmitIdle(t *testing.T) {
	m := newTestMini()
	m.SetPrompt("hello")
	if !m.Submit() {
		t.Fatal("idle submit returned false")
	}
	if m.Prompt() != "" {
		t.Fatalf("prompt not cleared: %q", m.Prompt())
	}
	if !m.Running() {
		t.Fatal("not running after submit")
	}
	if len(m.Lines()) != 1 || !strings.Contains(m.Lines()[0], "hello") {
		t.Fatalf("user line missing: %v", m.Lines())
	}
	if got := strings.TrimPrefix(m.Lines()[0], ""); !strings.Contains(got, "you> ") {
		t.Fatalf("user line not prefixed: %q", m.Lines()[0])
	}
}

func TestMiniSubmitEmpty(t *testing.T) {
	m := newTestMini()
	if m.Submit() {
		t.Fatal("empty prompt submitted")
	}
	if m.Running() || len(m.Lines()) != 0 {
		t.Fatal("state changed on empty submit")
	}
}

func TestMiniSubmitMidTurnDropsPrompt(t *testing.T) {
	m := newTestMini()
	m.SetPrompt("first")
	if !m.Submit() {
		t.Fatal("first submit failed")
	}
	m.SetPrompt("queued")
	if m.Submit() {
		t.Fatal("mid-turn submit returned true (mini has no queue)")
	}
	if m.Prompt() != "" {
		t.Fatalf("mid-turn prompt kept: %q", m.Prompt())
	}
	if !m.Running() {
		t.Fatal("turn marked finished by a dropped prompt")
	}
}

func TestMiniMessages(t *testing.T) {
	m := newTestMini()
	m.SetPrompt("go")
	m.Submit()
	m.Update(miniDelta{text: "answer text"})
	m.Update(miniTool{Kind: "start", Name: "write"})
	m.Update(miniTool{Kind: "end", Name: "write", Out: "wrote"})
	m.Update(miniUsage{In: 12, Out: 7, Cost: 0.001})
	body := strings.Join(m.Lines(), "\n")
	for _, want := range []string{"answer text", "▸ write …", "wrote"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in:\n%s", want, body)
		}
	}
	view := m.View(24, 100)
	if !strings.Contains(view, "12 in / 7 out") {
		t.Errorf("usage missing from view:\n%s", view)
	}
	if !strings.Contains(view, "$0.0010") {
		t.Errorf("cost missing from view:\n%s", view)
	}
	if !strings.Contains(view, "working…") {
		t.Errorf("working indicator missing:\n%s", view)
	}
	m.Update(miniDone{})
	if m.Running() {
		t.Error("still running after miniDone")
	}
	if strings.Contains(m.View(24, 100), "working…") {
		t.Error("working indicator kept after miniDone")
	}
}

func TestMiniDoneWithError(t *testing.T) {
	m := newTestMini()
	m.SetPrompt("go")
	m.Submit()
	m.Update(miniDone{err: "boom"})
	view := m.View(24, 100)
	if !strings.Contains(view, "error: boom") {
		t.Fatalf("error line missing:\n%s", view)
	}
}

func TestMiniViewTruncates(t *testing.T) {
	m := newTestMini()
	for i := 0; i < 50; i++ {
		m.Update(miniDelta{text: strings.Repeat("x", 200)})
	}
	view := m.View(8, 40)
	if lines := strings.Split(view, "\n"); len(lines) > 8 {
		t.Fatalf("view has %d lines, want <= 8", len(lines))
	}
}

func TestMiniLinesForExport(t *testing.T) {
	m := newTestMini()
	m.SetPrompt("p")
	m.Submit()
	m.Update(miniDelta{text: "d"})
	got := m.linesFor()
	if len(got) != 2 {
		t.Fatalf("linesFor = %d lines, want 2", len(got))
	}
}
