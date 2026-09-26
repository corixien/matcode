package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Revert truncates the transcript after the message toID. The full transcript
// as it stood before the revert is archived as messages.jsonl.bak.<n>, so the
// revert itself is undoable via Restore. Returns how many messages were kept
// and the backup path (empty when nothing followed toID).
func (s *Session) Revert(toID string) (kept int, backup string, err error) {
	path := filepath.Join(s.Dir, "messages.jsonl")
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, "", fmt.Errorf("session %s has no transcript", s.Meta.ID)
	}
	if err != nil {
		return 0, "", err
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	idx := -1
	for i, ln := range lines {
		var m Message
		if json.Unmarshal([]byte(ln), &m) == nil && m.ID == toID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return 0, "", fmt.Errorf("message %s not found in session %s", toID, s.Meta.ID)
	}
	if idx == len(lines)-1 {
		return len(lines), "", nil
	}
	backup = filepath.Join(s.Dir, fmt.Sprintf("messages.jsonl.bak.%d", s.nextBackup()))
	if err := os.WriteFile(backup, b, 0o644); err != nil {
		return 0, "", err
	}
	keep := strings.Join(lines[:idx+1], "\n") + "\n"
	if err := os.WriteFile(path, []byte(keep), 0o644); err != nil {
		return 0, "", err
	}
	var last Message
	if json.Unmarshal([]byte(lines[idx]), &last) == nil && last.Time != 0 {
		s.Meta.Updated = last.Time
	}
	if err := s.saveMeta(); err != nil {
		return 0, "", err
	}
	return idx + 1, backup, nil
}

// Restore puts backup <n> back as the transcript; the current transcript is
// archived under a fresh backup number first, so restore is also undoable.
func (s *Session) Restore(n int) error {
	bak := filepath.Join(s.Dir, fmt.Sprintf("messages.jsonl.bak.%d", n))
	if _, err := os.Stat(bak); err != nil {
		return fmt.Errorf("backup %d not found in session %s", n, s.Meta.ID)
	}
	path := filepath.Join(s.Dir, "messages.jsonl")
	if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
		cur := filepath.Join(s.Dir, fmt.Sprintf("messages.jsonl.bak.%d", s.nextBackup()))
		if err := os.WriteFile(cur, b, 0o644); err != nil {
			return err
		}
	}
	return os.Rename(bak, path)
}

// nextBackup returns one past the highest messages.jsonl.bak.<n> in the folder.
func (s *Session) nextBackup() int {
	matches, _ := filepath.Glob(filepath.Join(s.Dir, "messages.jsonl.bak.*"))
	max := 0
	for _, m := range matches {
		suffix := strings.TrimPrefix(filepath.Base(m), "messages.jsonl.bak.")
		if n, err := strconv.Atoi(suffix); err == nil && n > max {
			max = n
		}
	}
	return max + 1
}
