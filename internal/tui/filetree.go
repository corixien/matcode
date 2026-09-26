package tui

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"matcode/internal/tui/theme"
)

// treeRow is one visible row of the file-tree overlay (row 35): a
// worktree-relative path and its depth. A label is "indent + path", so
// trimming the leading spaces of a label yields the path back — filtering
// on labels therefore filters on paths too.
type treeRow struct {
	path  string
	dir   bool
	depth int
}

// label renders the row for the overlay list.
func (r treeRow) label() string { return strings.Repeat("  ", r.depth) + r.path }

// treeSkip are directories never listed (same set the @ mention list skips).
var treeSkip = map[string]bool{".git": true, "node_modules": true}

// treeRowLimit bounds one screenful of listings for huge worktrees.
const treeRowLimit = 2000

// buildTreeRows lists root's children depth-first, descending only into
// directories the expanded map opts into. Directories at depth 0 are
// expanded by default so the first view shows two levels.
func buildTreeRows(root string, expanded map[string]bool) []treeRow {
	var rows []treeRow
	var walk func(dir, rel string, depth int)
	walk = func(dir, rel string, depth int) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		sort.Slice(entries, func(i, j int) bool {
			return entries[i].Name() < entries[j].Name()
		})
		for _, e := range entries {
			if len(rows) >= treeRowLimit {
				return
			}
			name := e.Name()
			if e.IsDir() {
				if treeSkip[name] || (strings.HasPrefix(name, ".") && depth == 0 && name != ".") {
					continue
				}
			}
			child := filepath.Join(rel, name)
			if rel == "" {
				child = name
			}
			rows = append(rows, treeRow{path: child, dir: e.IsDir(), depth: depth})
			if !e.IsDir() {
				continue
			}
			open, ok := expanded[child]
			if !ok {
				open = depth == 0 // default: two levels visible
			}
			if open {
				walk(filepath.Join(dir, name), child, depth+1)
			}
		}
	}
	walk(root, "", 0)
	return rows
}

// openTree opens the working-tree browser over the session cwd (row 35).
func (a *App) openTree() (tea.Model, tea.Cmd) {
	o := newOverlay("tree", "tree · "+a.cwd, nil, a.theme)
	o.treeRoot = a.cwd
	o.treeExpanded = map[string]bool{}
	o.rebuildTree("")
	a.overlay = o
	return a, nil
}

// rebuildTree recomputes the visible rows after expand/collapse, keeping
// the selection on path ("" = keep the current index).
func (o *overlay) rebuildTree(keep string) {
	rows := buildTreeRows(o.treeRoot, o.treeExpanded)
	o.tree = rows
	o.items = make([]string, len(rows))
	o.treeDirs = map[string]bool{}
	sel := o.sel
	for i, r := range rows {
		o.items[i] = r.label()
		o.treeDirs[r.path] = r.dir
		if r.path == keep {
			sel = i
		}
	}
	o.sel = sel
	o.clamp()
}

// treePath maps a list label back to its relative path.
func treePath(label string) string { return strings.TrimLeft(label, " ") }

// depthOf finds a path's depth in the last built row set.
func depthOf(rows []treeRow, path string) int {
	for _, r := range rows {
		if r.path == path {
			return r.depth
		}
	}
	return -1
}

// treeHint is the tree overlay's bottom help line.
const treeHint = "enter open/expand · ←/→ collapse/expand · / filter · esc close"

// treeKey routes one press into the tree overlay: move, filter, expand,
// collapse, or open the picked file in $EDITOR.
func (a *App) treeKey(o *overlay, k tea.KeyMsg) (tea.Model, tea.Cmd) {
	label := o.chosen()
	path := ""
	if label != "" {
		path = treePath(label)
	}
	isDir := path != "" && o.treeDirs[path]
	switch k.String() {
	case "esc", "ctrl+c":
		a.overlay = nil
	case "up":
		o.move(-1)
	case "down":
		o.move(1)
	case "pgup":
		o.move(-10)
	case "pgdown":
		o.move(10)
	case "backspace":
		o.backspace()
	case "right":
		if isDir && !o.treeIsOpen(path) {
			o.treeExpandTo(path, true)
		}
	case "left":
		if isDir && o.treeIsOpen(path) {
			o.treeExpandTo(path, false)
		}
	case "enter", " ":
		if path == "" {
			return a, nil
		}
		if isDir {
			o.treeExpandTo(path, !o.treeIsOpen(path))
			return a, nil
		}
		a.overlay = nil
		return a.openFile(filepath.Join(o.treeRoot, path))
	default:
		if k.Type == tea.KeyRunes {
			o.insert(string(k.Runes))
		}
	}
	return a, nil
}

// treeIsOpen reports whether a directory is currently expanded.
func (o *overlay) treeIsOpen(path string) bool {
	if open, ok := o.treeExpanded[path]; ok {
		return open
	}
	return depthOf(o.tree, path) == 0 // default: top level open
}

// treeExpandTo sets a directory's expansion state and rebuilds,
// keeping the selection on it.
func (o *overlay) treeExpandTo(path string, open bool) {
	o.treeExpanded[path] = open
	o.rebuildTree(path)
}

// --- tests ---

// newTreeOverlay builds a tree overlay rooted at dir for tests.
func newTreeOverlay(dir string, t theme.Theme) *overlay {
	o := newOverlay("tree", "tree", nil, t)
	o.treeRoot = dir
	o.treeExpanded = map[string]bool{}
	o.rebuildTree("")
	return o
}
