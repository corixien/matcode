package tui

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/aymanbagabas/go-udiff"
	tea "github.com/charmbracelet/bubbletea"

	"matcode/internal/config"
	"matcode/internal/snapshots"
	"matcode/internal/store"
)

// stepDiff renders the unified diff of one tool step's file changes
// (row 35 diff view): every path whose before/after blobs differ, as
// unified-diff text. An empty string means the step touched nothing.
func stepDiff(cfg *config.Config, s *store.Session, stepID string) (string, error) {
	entry := snapshots.Find(snapshots.Load(snapshots.Path(s.Dir)), stepID)
	if entry == nil {
		return "", fmt.Errorf("no snapshot for step %s (repo-less run or capture off)", stepID)
	}
	objects := filepath.Join(cfg.DataDir(), "objects")
	before, after := snapshots.ReadStep(objects, entry)

	paths := map[string]bool{}
	for p := range before {
		paths[p] = true
	}
	for p := range after {
		paths[p] = true
	}
	var changed []string
	for p := range paths {
		if string(before[p]) != string(after[p]) {
			changed = append(changed, p)
		}
	}
	sort.Strings(changed)

	var b strings.Builder
	for _, p := range changed {
		d := udiff.Unified(p, p, string(before[p]), string(after[p]))
		if d == "" {
			continue
		}
		if !strings.HasSuffix(d, "\n") {
			d += "\n"
		}
		b.WriteString(d)
	}
	return b.String(), nil
}

// lastDiffStep picks the newest snapshot step that changed files,
// newest first; "" means the session recorded nothing.
func lastDiffStep(s *store.Session) string {
	entries := snapshots.Load(snapshots.Path(s.Dir))
	for i := len(entries) - 1; i >= 0; i-- {
		if stepChanged(entries[i]) {
			return entries[i].ID
		}
	}
	return ""
}

// stepChanged reports whether a snapshot entry recorded any file change.
func stepChanged(e snapshots.Entry) bool {
	for p, sum := range e.After {
		if e.Before[p] != sum {
			return true
		}
	}
	for p := range e.Before {
		if _, ok := e.After[p]; !ok {
			return true
		}
	}
	return false
}

// openDiff shows the unified diff of the newest file-changing step in
// the active session (row 35).
func (a *App) openDiff() (tea.Model, tea.Cmd) {
	t := a.cur()
	if t == nil {
		return a, nil
	}
	step := lastDiffStep(t.sess)
	if step == "" {
		a.status = "no file changes recorded in this session"
		return a, nil
	}
	text, err := stepDiff(a.cfg, t.sess, step)
	if err != nil {
		a.status = err.Error()
		return a, nil
	}
	if strings.TrimSpace(text) == "" {
		a.status = "no file changes recorded in this session"
		return a, nil
	}
	o := newOverlay("diff", "diff · "+step, nil, a.theme)
	o.diffLines = strings.Split(strings.TrimRight(text, "\n"), "\n")
	a.overlay = o
	return a, nil
}

// diffBody renders the scrollable, colorized diff lines.
func (o *overlay) diffBody(width, height int) []string {
	if len(o.diffLines) == 0 {
		return []string{o.fg(o.theme.Colors.Muted).Render("no file changes")}
	}
	room := height - 3 // title + hint (+1 slack)
	if room < 3 {
		room = 3
	}
	if o.diffOff > len(o.diffLines)-room {
		o.diffOff = len(o.diffLines) - room
	}
	if o.diffOff < 0 {
		o.diffOff = 0
	}
	out := make([]string, 0, room)
	for i := o.diffOff; i < len(o.diffLines) && i < o.diffOff+room; i++ {
		out = append(out, truncate(o.diffLine(o.diffLines[i]), width))
	}
	return out
}

// diffLine colors one unified-diff line by its prefix.
func (o *overlay) diffLine(s string) string {
	switch {
	case strings.HasPrefix(s, "+"):
		return o.fg(o.theme.Colors.Success).Render(s)
	case strings.HasPrefix(s, "-"):
		return o.fg(o.theme.Colors.Error).Render(s)
	case strings.HasPrefix(s, "@@"):
		return o.fg(o.theme.Colors.Accent).Render(s)
	case strings.HasPrefix(s, "diff "), strings.HasPrefix(s, "index "):
		return o.fg(o.theme.Colors.Primary).Bold(true).Render(s)
	case strings.HasPrefix(s, "+++"), strings.HasPrefix(s, "---"):
		return o.fg(o.theme.Colors.Muted).Render(s)
	default:
		return s
	}
}

// scrollDiff moves the diff viewport by delta lines.
func (o *overlay) scrollDiff(delta int) {
	o.diffOff += delta
	if o.diffOff < 0 {
		o.diffOff = 0
	}
	max := len(o.diffLines) - 1
	if o.diffOff > max {
		o.diffOff = max
	}
}
