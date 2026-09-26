package tui

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"matcode/internal/config"
	"matcode/internal/snapshots"
	"matcode/internal/store"
)

// lastAssistantToolStep returns the index of the newest assistant message
// carrying tool calls, or -1.
func lastAssistantToolStep(msgs []store.Message) int {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "assistant" && len(msgs[i].ToolCalls) > 0 {
			return i
		}
	}
	return -1
}

// restoreStepFiles puts files back from a step's snapshot: Before for
// undo, After for redo. Returns paths changed (-1 = transcript only).

// restoreStepFiles puts files back from a step's snapshot: Before for
// undo, After for redo. Returns paths changed (-1 = transcript only).
func restoreStepFiles(cfg *config.Config, s *store.Session, stepID string, after bool) int {
	entry := snapshots.Find(snapshots.Load(snapshots.Path(s.Dir)), stepID)
	if entry == nil {
		return -1
	}
	state := entry.Before
	if after {
		state = entry.After
	}
	if state == nil {
		return -1
	}
	wd := entry.WD
	if wd == "" {
		wd, _ = filepath.Abs(".")
	}
	// exclude must match the capture side: with it unset enumerate() also
	// sees the data dir, and every live state/session file missing from the
	// recorded state gets deleted (including the journal itself).
	c := snapshots.New(wd, filepath.Join(cfg.DataDir(), "objects"), snapshots.Path(s.Dir), cfg.DataDir())
	n, err := c.Restore(state)
	if err != nil {
		return -1
	}
	return n
}

// globBackups lists a session's transcript backups, oldest first.

// globBackups lists a session's transcript backups, oldest first.
func globBackups(dir string) []string {
	m, _ := filepath.Glob(filepath.Join(dir, "messages.jsonl.bak.*"))
	sort.Strings(m)
	return m
}

// countLines counts non-empty JSONL lines in a transcript file.

// countLines counts non-empty JSONL lines in a transcript file.
func countLines(p string) int {
	b, err := os.ReadFile(p)
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
