package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"matcode/internal/config"
	"matcode/internal/store"
)

// Revert executes `mtc revert <session-id> <message-id>`: keep the transcript
// through message-id; the full pre-revert transcript is archived as an
// undoable backup.
func Revert(ctx context.Context, args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("usage: mtc revert <session-id> <message-id>")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	cfg, err := config.Load(cwd)
	if err != nil {
		return err
	}
	s, err := store.Open(filepath.Join(cfg.SessionsDir(), args[0]))
	if err != nil {
		return err
	}
	kept, backup, err := s.Revert(args[1])
	if err != nil {
		return err
	}
	if backup == "" {
		fmt.Printf("nothing follows %s; %d messages kept\n", args[1], kept)
		return nil
	}
	fmt.Printf("reverted: %d messages kept, backup in %s (undo: mtc restore %s %s)\n",
		kept, filepath.Base(backup), args[0], filepath.Base(backup)[len("messages.jsonl.bak."):])
	return nil
}
