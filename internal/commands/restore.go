package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"matcode/internal/config"
	"matcode/internal/store"
)

// Restore executes `mtc restore <session-id> <backup-number>`: undo a revert
// by putting its archived tail back as the transcript.
func Restore(ctx context.Context, args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("usage: mtc restore <session-id> <backup-number>")
	}
	n, err := strconv.Atoi(args[1])
	if err != nil || n < 1 {
		return fmt.Errorf("backup number must be a positive integer, got %q", args[1])
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
	if err := s.Restore(n); err != nil {
		return err
	}
	fmt.Printf("restored backup %d into session %s\n", n, s.Meta.ID)
	return nil
}
