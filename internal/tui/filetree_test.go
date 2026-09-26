package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"matcode/internal/tui/theme"
)

// mkTree lays out a small worktree: a loose file, a nested dir, and the
// two directory names the tree must skip.
func mkTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, rel := range []string{
		"a.txt", "sub/b.txt", "sub/deep/c.txt", ".git/config", "node_modules/x.js",
	} {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(rel), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// treePaths flattens the overlay's visible labels to their paths.
func treePaths(o *overlay) []string {
	var out []string
	for _, it := range o.items {
		out = append(out, treePath(it))
	}
	return out
}

// TestTreeDefaultsTwoLevels proves depth-0 dirs start expanded, deeper
// ones start collapsed, and .git/node_modules never appear.
func TestTreeDefaultsTwoLevels(t *testing.T) {
	o := newTreeOverlay(mkTree(t), theme.Default)
	got := treePaths(o)
	want := []string{"a.txt", "sub", "sub/b.txt", "sub/deep"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("rows = %v, want %v", got, want)
	}
	for _, p := range got {
		if strings.Contains(p, ".git") || strings.Contains(p, "node_modules") {
			t.Fatalf("skipped dir leaked: %q", p)
		}
	}
}

// TestTreeExpandCollapse proves ←/→ and rebuild keep the selection on
// the toggled path.
func TestTreeExpandCollapse(t *testing.T) {
	o := newTreeOverlay(mkTree(t), theme.Default)
	// Select "sub" (row 1) and collapse it.
	o.sel = 1
	o.treeExpandTo("sub", false)
	if got := treePaths(o); len(got) != 2 {
		t.Fatalf("after collapse rows = %v, want 2 rows", got)
	}
	if p := treePath(o.chosen()); p != "sub" {
		t.Fatalf("selection after collapse = %q, want sub", p)
	}
	// Expand the collapsed "sub/deep" is impossible while sub is closed;
	// reopen sub: deep stays collapsed (its default).
	o.treeExpandTo("sub", true)
	if !o.treeIsOpen("sub") {
		t.Fatal("sub should be open")
	}
	if o.treeIsOpen("sub/deep") {
		t.Fatal("sub/deep should stay collapsed by default")
	}
	// Explicitly open the deep dir → c.txt appears.
	o.treeExpandTo("sub/deep", true)
	found := false
	for _, p := range treePaths(o) {
		if p == "sub/deep/c.txt" {
			found = true
		}
	}
	if !found {
		t.Fatalf("sub/deep/c.txt missing after expand: %v", treePaths(o))
	}
}

// TestTreeFilter proves typing filters on paths and backspace restores.
func TestTreeFilter(t *testing.T) {
	o := newTreeOverlay(mkTree(t), theme.Default)
	o.insert("b.txt")
	if got := o.filtered(); len(got) != 1 || treePath(got[0]) != "sub/b.txt" {
		t.Fatalf("filtered = %v", got)
	}
	for o.query != "" {
		o.backspace()
	}
	if len(o.filtered()) != len(o.items) {
		t.Fatalf("backspace did not clear: %d vs %d", len(o.filtered()), len(o.items))
	}
}

// TestTreeKeyOpenAndEscape drives the overlay keys: escape closes, a
// file pick opens $EDITOR (a non-nil exec command), a dir pick expands.
func TestTreeKeyOpenAndEscape(t *testing.T) {
	dir := mkTree(t)
	a := &App{cwd: dir, theme: theme.Default}
	m, _ := a.openTree()
	app := m.(*App)
	o := app.overlay
	if o.kind != "tree" {
		t.Fatalf("kind = %q", o.kind)
	}

	// down to "sub" (a dir), right expands nothing new (already open),
	// left collapses it.
	app.treeKey(o, tea.KeyMsg{Type: tea.KeyDown})
	app.treeKey(o, tea.KeyMsg{Type: tea.KeyLeft})
	if o.treeIsOpen("sub") {
		t.Fatal("left did not collapse sub")
	}

	// Escape closes the overlay.
	app.treeKey(o, tea.KeyMsg{Type: tea.KeyEsc})
	if app.overlay != nil {
		t.Fatal("esc did not close the tree")
	}

	// Enter on a file returns the editor command.
	m, cmd := app.openTree()
	app = m.(*App)
	o = app.overlay
	app.treeKey(o, tea.KeyMsg{Type: tea.KeyUp}) // a.txt
	if p := treePath(o.chosen()); p != "a.txt" {
		t.Fatalf("selected %q, want a.txt", p)
	}
	_, cmd = app.treeKey(o, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter on a file returned no editor command")
	}
	if app.overlay != nil {
		t.Fatal("enter on a file did not close the tree")
	}
}

// TestTreePathRoundTrip proves label → path is just TrimLeft(" ").
func TestTreePathRoundTrip(t *testing.T) {
	r := treeRow{path: "sub/deep/c.txt", depth: 3}
	if got := treePath(r.label()); got != r.path {
		t.Fatalf("treePath(label) = %q, want %q", got, r.path)
	}
}
