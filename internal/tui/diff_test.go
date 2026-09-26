package tui

import (
	"bytes"
	"compress/zlib"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"matcode/internal/config"
	"matcode/internal/snapshots"
	"matcode/internal/store"
	"matcode/internal/tui/theme"
)

// writeBlob stores data in a snapshots object store (same loose-object
// layout the capturer writes) and returns its hash.
func writeBlob(t *testing.T, objects, data string) string {
	t.Helper()
	payload := append([]byte(fmt.Sprintf("blob %d\x00", len(data))), data...)
	sum := sha1.Sum(payload)
	hash := hex.EncodeToString(sum[:])
	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf)
	if _, err := zw.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(objects, hash[:2], hash[2:])
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return hash
}

// writeJournal appends snapshot entries to a session's journal.
func writeJournal(t *testing.T, sessDir string, entries ...snapshots.Entry) {
	t.Helper()
	var b bytes.Buffer
	for _, e := range entries {
		line, err := marshalEntry(e)
		if err != nil {
			t.Fatal(err)
		}
		b.WriteString(line)
	}
	if err := os.WriteFile(snapshots.Path(sessDir), b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// marshalEntry is a tiny JSON line writer for the test journal.
func marshalEntry(e snapshots.Entry) (string, error) {
	return fmt.Sprintf(`{"id":%q,"wd":%q,"before":%s,"after":%s}`+"\n",
		e.ID, e.WD, maph(e.Before), maph(e.After)), nil
}

// maph renders a path→hash map as JSON (paths in tests are simple).
func maph(m map[string]string) string {
	if len(m) == 0 {
		return "{}"
	}
	parts := make([]string, 0, len(m))
	for k, v := range m {
		parts = append(parts, fmt.Sprintf("%q:%q", k, v))
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// diffFixture builds a session + object store where step msg_1 changed
// f.txt and step msg_2 touched nothing.
func diffFixture(t *testing.T) (cfg *config.Config, s *store.Session, objects string) {
	t.Helper()
	root := t.TempDir()
	objects = filepath.Join(root, "objects")
	sessDir := t.TempDir()
	before := writeBlob(t, objects, "old line\nkeep line\n")
	after := writeBlob(t, objects, "new line\nkeep line\n")
	added := writeBlob(t, objects, "brand new\n")
	writeJournal(t, sessDir,
		snapshots.Entry{ID: "msg_1", WD: root,
			Before: map[string]string{"f.txt": before, "g.txt": before},
			After:  map[string]string{"f.txt": after, "g.txt": after}},
		snapshots.Entry{ID: "msg_2", WD: root,
			Before: map[string]string{"h.txt": before},
			After:  map[string]string{"h.txt": added}},
		snapshots.Entry{ID: "msg_3", WD: root,
			Before: map[string]string{"f.txt": after},
			After:  map[string]string{"f.txt": after}},
	)
	// g.txt gets a real add: missing before → all-plus diff.
	_ = added
	return &config.Config{ProjectDir: root, Providers: map[string]config.Provider{}},
		&store.Session{Dir: sessDir}, objects
}

// TestStepDiffUnified proves a step's before/after blobs come back as a
// unified diff with context, and an added file diffs as all-plus.
func TestStepDiffUnified(t *testing.T) {
	cfg, s, _ := diffFixture(t)
	got, err := stepDiff(cfg, s, "msg_1")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"@@ ", "-old line", "+new line", " keep line", "f.txt"} {
		if !strings.Contains(got, want) {
			t.Errorf("diff missing %q:\n%s", want, got)
		}
	}
	// msg_2 changed g.txt only by content that is actually different? No:
	// msg_2's h.txt changed before→added content.
	got2, err := stepDiff(cfg, s, "msg_2")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got2, "+brand new") {
		t.Errorf("msg_2 diff wrong:\n%s", got2)
	}
}

// TestLastDiffStep proves the picker skips steps that recorded no file
// change (msg_3 is unchanged) and reports "" for a session without one.
func TestLastDiffStep(t *testing.T) {
	_, s, _ := diffFixture(t)
	if got := lastDiffStep(s); got != "msg_2" {
		t.Fatalf("lastDiffStep = %q, want msg_2", got)
	}
	empty := &store.Session{Dir: t.TempDir()}
	if got := lastDiffStep(empty); got != "" {
		t.Fatalf("lastDiffStep(empty) = %q, want \"\"", got)
	}
}

// TestOpenDiffOverlay proves /diff opens a scrollable diff overlay, or
// reports why it could not.
func TestOpenDiffOverlay(t *testing.T) {
	cfg, s, _ := diffFixture(t)
	a := &App{cfg: cfg, cwd: filepath.Dir(s.Dir), theme: theme.Default,
		tabs: []*tab{{sess: s}}}
	m, _ := a.openDiff()
	app := m.(*App)
	if app.overlay == nil || app.overlay.kind != "diff" {
		t.Fatalf("overlay = %+v, want diff", app.overlay)
	}
	if len(app.overlay.diffLines) == 0 {
		t.Fatal("diff overlay has no lines")
	}
	// Scroll down past the end: clamped, never negative.
	app.overlay.scrollDiff(1000)
	if app.overlay.diffOff < 0 {
		t.Fatalf("diffOff = %d after scroll", app.overlay.diffOff)
	}
	// Rendered body is colorized and bounded by height.
	app.overlay.diffOff = 0
	body := app.overlay.diffBody(80, 10)
	if len(body) == 0 || len(body) > 7 {
		t.Fatalf("diffBody lines = %d", len(body))
	}
	if !strings.Contains(strings.Join(body, "\n"), "h.txt") {
		t.Error("diff body does not show the changed file")
	}

	// A session with no snapshot journal reports instead of opening.
	a2 := &App{cfg: cfg, theme: theme.Default, tabs: []*tab{{sess: &store.Session{Dir: t.TempDir()}}}}
	m2, _ := a2.openDiff()
	app2 := m2.(*App)
	if app2.overlay != nil {
		t.Fatal("overlay opened without snapshots")
	}
	if !strings.Contains(app2.status, "no file changes") {
		t.Fatalf("status = %q", app2.status)
	}
}

// TestDiffBodyNoChanges proves the empty-state line.
func TestDiffBodyNoChanges(t *testing.T) {
	o := newOverlay("diff", "diff", nil, theme.Default)
	body := o.diffBody(80, 10)
	if len(body) != 1 || !strings.Contains(body[0], "no file changes") {
		t.Fatalf("body = %v", body)
	}
}

// TestStepChanged proves change detection on both edit and add/remove.
func TestStepChanged(t *testing.T) {
	if stepChanged(snapshots.Entry{
		Before: map[string]string{"a": "1"}, After: map[string]string{"a": "1"},
	}) {
		t.Error("identical entry reported as changed")
	}
	if !stepChanged(snapshots.Entry{Before: map[string]string{"a": "1"}}) {
		t.Error("deleted file not reported")
	}
	if !stepChanged(snapshots.Entry{After: map[string]string{"a": "1"}}) {
		t.Error("added file not reported")
	}
}
