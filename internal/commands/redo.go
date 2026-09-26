package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"matcode/internal/snapshots"
	"matcode/internal/store"
)

// Redo executes `mtc redo <session-id>`: re-apply the most recent undo by
// restoring its archived transcript and that step's after-state files.
// An archive qualifies only when it holds strictly more messages than the
// live transcript, so redo can never run twice without an undo between.
func Redo(ctx context.Context, args []string) error {
	cfg, s, err := openSession(args, "mtc redo <session-id>")
	if err != nil {
		return err
	}
	msgs, err := s.Messages()
	if err != nil {
		return err
	}
	n, err := redoBackup(s, len(msgs))
	if err != nil {
		return err
	}
	if err := s.Restore(n); err != nil {
		return err
	}
	if msgs, err = s.Messages(); err != nil {
		return err
	}
	files := -1
	if step := lastToolStep(msgs); step >= 0 {
		entry := snapshots.Find(snapshots.Load(snapshots.Path(s.Dir)), msgs[step].ID)
		files = restoreState(cfg, s, entry, entryAfter(entry))
	}
	if files < 0 {
		fmt.Printf("redone: %d messages (transcript only)\n", len(msgs))
		return nil
	}
	fmt.Printf("redone: %d messages, %d files restored\n", len(msgs), files)
	return nil
}

// entryAfter guards against a nil entry.
func entryAfter(e *snapshots.Entry) map[string]string {
	if e == nil {
		return nil
	}
	return e.After
}

// redoBackup picks the highest messages.jsonl.bak.<n> holding more
// messages than the live transcript — the archive an undo just wrote.
func redoBackup(s *store.Session, live int) (int, error) {
	matches, _ := filepath.Glob(filepath.Join(s.Dir, "messages.jsonl.bak.*"))
	best := -1
	for _, m := range matches {
		n, err := strconv.Atoi(strings.TrimPrefix(filepath.Base(m), "messages.jsonl.bak."))
		if err != nil {
			continue
		}
		if n > best && countLines(m) > live {
			best = n
		}
	}
	if best < 0 {
		return 0, fmt.Errorf("nothing to redo in session %s", s.Meta.ID)
	}
	return best, nil
}

// countLines counts non-empty JSONL lines in a transcript file.
func countLines(path string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n := 0
	for _, ln := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(ln) != "" {
			n++
		}
	}
	return n
}
