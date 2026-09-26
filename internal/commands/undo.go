package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"matcode/internal/config"
	"matcode/internal/snapshots"
	"matcode/internal/store"
)

// openSession resolves config and opens the session named by args[0].
func openSession(args []string, usage string) (*config.Config, *store.Session, error) {
	if len(args) != 1 {
		return nil, nil, fmt.Errorf("usage: %s", usage)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, nil, err
	}
	cfg, err := config.Load(cwd)
	if err != nil {
		return nil, nil, err
	}
	s, err := store.Open(filepath.Join(cfg.SessionsDir(), args[0]))
	if err != nil {
		return nil, nil, err
	}
	return cfg, s, nil
}

// lastToolStep is the index of the last assistant message that requested
// tool calls — one undo step — or -1 when the session has none.
func lastToolStep(msgs []store.Message) int {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "assistant" && len(msgs[i].ToolCalls) > 0 {
			return i
		}
	}
	return -1
}

// restoreState puts the worktree recorded on entry back to state (Before
// for undo, After for redo). A nil state (step never captured: repo-less,
// snapshots=false) means transcript-only rollback. Returns paths changed,
// or -1 when no file restore happened.
func restoreState(cfg *config.Config, s *store.Session, entry *snapshots.Entry, state map[string]string) int {
	if state == nil {
		return -1
	}
	wd := ""
	if entry != nil && entry.WD != "" {
		wd = entry.WD
	}
	if wd == "" {
		wd, _ = os.Getwd()
	}
	// exclude the data dir exactly like the capture path does — otherwise
	// Restore enumerates live sessions/.mtc files and deletes them as
	// "created by the step being reverted".
	c := snapshots.New(wd, filepath.Join(cfg.DataDir(), "objects"), snapshots.Path(s.Dir), cfg.DataDir())
	n, err := c.Restore(state)
	if err != nil {
		return -1
	}
	return n
}

// Undo executes `mtc undo <session-id>`: revert the transcript to the
// message before the last tool-using assistant step and restore that
// step's file snapshots from the object store.
func Undo(ctx context.Context, args []string) error {
	cfg, s, err := openSession(args, "mtc undo <session-id>")
	if err != nil {
		return err
	}
	msgs, err := s.Messages()
	if err != nil {
		return err
	}
	step := lastToolStep(msgs)
	if step < 1 {
		return fmt.Errorf("nothing to undo in session %s", s.Meta.ID)
	}
	kept, backup, err := s.Revert(msgs[step-1].ID)
	if err != nil {
		return err
	}
	entry := snapshots.Find(snapshots.Load(snapshots.Path(s.Dir)), msgs[step].ID)
	files := restoreState(cfg, s, entry, entryBefore(entry))
	bak := ""
	if backup != "" {
		bak = ", backup in " + filepath.Base(backup)
	}
	if files < 0 {
		fmt.Printf("undone: %d messages kept%s (transcript only)\n", kept, bak)
		return nil
	}
	fmt.Printf("undone: %d messages kept, %d files restored%s (redo: mtc redo %s)\n",
		kept, files, bak, s.Meta.ID)
	return nil
}

// entryBefore guards against a nil entry.
func entryBefore(e *snapshots.Entry) map[string]string {
	if e == nil {
		return nil
	}
	return e.Before
}
