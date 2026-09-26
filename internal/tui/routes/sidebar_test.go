package routes

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"matcode/internal/store"
	"matcode/internal/tui/theme"
)

// TestBuildWidgets proves every sidebar panel derives from state the
// session already has: identity, usage, todos.json, touched files.
func TestBuildWidgets(t *testing.T) {
	cwd := t.TempDir()
	todos := `[{"content":"wire the diff","status":"completed"},{"content":"ship it","status":"in_progress"}]`
	if err := os.WriteFile(filepath.Join(cwd, "todos.json"), []byte(todos), 0o644); err != nil {
		t.Fatal(err)
	}
	meta := store.Meta{ID: "ses_t1", Title: "row 35", Model: "mock/t", Agent: "build"}
	msgs := []store.Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "ok", ToolCalls: []store.ToolCall{
			{Name: "write", ID: "c1", Arguments: rawJSON(t, map[string]string{"path": "src/new.go"})},
			{Name: "bash", ID: "c2", Arguments: rawJSON(t, map[string]string{"command": "ls"})},
			{Name: "edit", ID: "c3", Arguments: rawJSON(t, map[string]string{"path": "src/new.go"})},
			{Name: "patch", ID: "c4", Arguments: rawJSON(t, map[string]string{"path": "src/old.go"})},
		}},
	}
	ws := buildWidgets(meta, "1.2k tok · $0.01", cwd, msgs)

	var titles []string
	byTitle := map[string]Widget{}
	for _, w := range ws {
		titles = append(titles, w.Title)
		byTitle[w.Title] = w
	}
	want := strings.Join([]string{"session", "usage", "todos", "files"}, ",")
	if strings.Join(titles, ",") != want {
		t.Fatalf("widgets = %v, want %s", titles, want)
	}
	if got := strings.Join(byTitle["session"].Lines, " | "); !strings.Contains(got, "row 35") ||
		!strings.Contains(got, "mock/t") || !strings.Contains(got, "2 messages") {
		t.Errorf("session lines = %q", got)
	}
	if got := strings.Join(byTitle["todos"].Lines, " "); !strings.Contains(got, "✔ wire the diff") ||
		!strings.Contains(got, "▸ ship it") {
		t.Errorf("todo lines = %q", got)
	}
	files := strings.Join(byTitle["files"].Lines, " ")
	if !strings.Contains(files, "src/new.go") || !strings.Contains(files, "src/old.go") {
		t.Errorf("files = %q", files)
	}
	if strings.Contains(files, "bash") {
		t.Errorf("non-mutating tool leaked into files: %q", files)
	}
}

// TestBuildWidgetsNoExtras proves a bare session (no todos file, no
// mutations) only shows the identity panel.
func TestBuildWidgetsNoExtras(t *testing.T) {
	ws := buildWidgets(store.Meta{ID: "s"}, "", t.TempDir(), []store.Message{{Role: "user"}})
	if len(ws) != 1 || ws[0].Title != "session" {
		t.Fatalf("widgets = %+v", ws)
	}
}

// TestSidebarViewPads proves the column is exactly height lines so it
// can sit beside the transcript.
func TestSidebarViewPads(t *testing.T) {
	ws := buildWidgets(store.Meta{ID: "s", Model: "m"}, "", "", nil)
	got := sidebarView(ws, 10, 30, theme.Default)
	if n := strings.Count(got, "\n") + 1; n != 10 {
		t.Fatalf("sidebar height = %d lines, want 10", n)
	}
	if !strings.Contains(got, "session") {
		t.Error("session title missing")
	}
}

// TestChatSidebarToggle proves the toggle flips visibility and the view
// only renders the column when the terminal is wide enough.
func TestChatSidebarToggle(t *testing.T) {
	c := newTestChat()
	if !c.ToggleSidebar() {
		t.Fatal("toggle did not turn on")
	}
	wide := c.View(24, 120)
	if !strings.Contains(wide, "session") {
		t.Fatalf("wide view missing sidebar:\n%s", wide)
	}
	// Every line of the wide view stays within the terminal width.
	for _, l := range strings.Split(wide, "\n") {
		if lipglossWidth(l) > 120 {
			t.Fatalf("line too wide (%d): %q", lipglossWidth(l), l)
		}
	}
	narrow := c.View(24, 80)
	if strings.Contains(narrow, "\nsession") {
		t.Fatal("sidebar rendered at 80 columns")
	}
	if c.ToggleSidebar() {
		t.Fatal("toggle did not turn off")
	}
}

// rawJSON marshals a one-key input object for a tool call.
func rawJSON(t *testing.T, v map[string]string) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// lipglossWidth is the display width of one rendered line.
func lipglossWidth(s string) int { return lipgloss.Width(s) }
