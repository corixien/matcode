package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seed appends n messages with distinct ids, returning them in order.
func seed(t *testing.T, s *Session, n int, baseTime int64) []*Message {
	t.Helper()
	out := make([]*Message, n)
	for i := range out {
		m := &Message{Role: "user", Content: strings.Repeat("m", i+1), Time: baseTime + int64(i)}
		if err := s.Append(m); err != nil {
			t.Fatal(err)
		}
		out[i] = m
	}
	return out
}

// TestRevertDropsTailAndBacksUp proves Revert keeps messages up to toID,
// archives the pre-revert transcript as messages.jsonl.bak.1, and rewrites
// Meta.Updated to the last kept message.
func TestRevertDropsTailAndBacksUp(t *testing.T) {
	s := newSess(t)
	msgs := seed(t, s, 4, 1900000000000)
	original, err := os.ReadFile(filepath.Join(s.Dir, "messages.jsonl"))
	if err != nil {
		t.Fatal(err)
	}

	kept, bak, err := s.Revert(msgs[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	if kept != 2 {
		t.Fatalf("kept = %d, want 2", kept)
	}
	wantBak := filepath.Join(s.Dir, "messages.jsonl.bak.1")
	if bak != wantBak {
		t.Fatalf("backup = %q, want %q", bak, wantBak)
	}
	// The backup is the untouched pre-revert transcript.
	got, err := os.ReadFile(bak)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Fatalf("backup differs from original transcript (%d vs %d bytes)", len(got), len(original))
	}
	live, err := s.Messages()
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 2 || live[0].ID != msgs[0].ID || live[1].ID != msgs[1].ID {
		t.Fatalf("live transcript = %+v", live)
	}
	again, err := Open(s.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if again.Meta.Updated != msgs[1].Time {
		t.Fatalf("Updated = %d, want kept-last time %d", again.Meta.Updated, msgs[1].Time)
	}
	// The truncated session keeps accepting appends.
	if err := s.Append(&Message{Role: "user", Content: "again", Time: 1900000000100}); err != nil {
		t.Fatal(err)
	}
	live, err = s.Messages()
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 3 || live[2].Content != "again" {
		t.Fatalf("append after revert = %+v", live)
	}
}

// TestRevertLastMessageIsNoop proves reverting to the newest message has
// nothing to drop: no backup file, transcript intact.
func TestRevertLastMessageIsNoop(t *testing.T) {
	s := newSess(t)
	msgs := seed(t, s, 3, 1900000000000)
	kept, bak, err := s.Revert(msgs[2].ID)
	if err != nil {
		t.Fatal(err)
	}
	if kept != 3 || bak != "" {
		t.Fatalf("kept=%d backup=%q, want 3 and empty", kept, bak)
	}
	if matches, _ := filepath.Glob(filepath.Join(s.Dir, "messages.jsonl.bak.*")); len(matches) != 0 {
		t.Fatalf("noop revert wrote backups: %v", matches)
	}
	live, err := s.Messages()
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 3 {
		t.Fatalf("transcript changed: %d messages", len(live))
	}
}

// TestRevertErrors proves the two failure modes leave the transcript alone.
func TestRevertErrors(t *testing.T) {
	s := newSess(t)
	if _, _, err := s.Revert("msg_nope"); err == nil || !strings.Contains(err.Error(), "no transcript") {
		t.Fatalf("missing transcript error = %v", err)
	}
	seed(t, s, 2, 1900000000000)
	before, err := os.ReadFile(filepath.Join(s.Dir, "messages.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Revert("msg_missing"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("unknown id error = %v", err)
	}
	after, err := os.ReadFile(filepath.Join(s.Dir, "messages.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("failed Revert modified the transcript")
	}
}

// TestRevertBackupChainIsAppendOnly proves every revert archives under a
// fresh, monotonically increasing number and old backups keep their bytes;
// Restore round-trips and archives the displaced transcript too.
func TestRevertBackupChainIsAppendOnly(t *testing.T) {
	s := newSess(t)
	msgs := seed(t, s, 4, 1900000000000)
	full, err := os.ReadFile(filepath.Join(s.Dir, "messages.jsonl"))
	if err != nil {
		t.Fatal(err)
	}

	if _, bak, err := s.Revert(msgs[1].ID); err != nil || filepath.Base(bak) != "messages.jsonl.bak.1" {
		t.Fatalf("first revert: bak=%q err=%v", bak, err)
	}
	if err := s.Append(&Message{Role: "user", Content: "post", Time: 1900000000999}); err != nil {
		t.Fatal(err)
	}
	if _, bak, err := s.Revert(msgs[1].ID); err != nil || filepath.Base(bak) != "messages.jsonl.bak.2" {
		t.Fatalf("second revert: bak=%q err=%v", bak, err)
	}
	// bak.1 is untouched by the second revert.
	b1, err := os.ReadFile(filepath.Join(s.Dir, "messages.jsonl.bak.1"))
	if err != nil || string(b1) != string(full) {
		t.Fatalf("bak.1 mutated: err=%v len=%d", err, len(b1))
	}
	b2, err := os.ReadFile(filepath.Join(s.Dir, "messages.jsonl.bak.2"))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(b2), "\n"); n != 3 {
		t.Fatalf("bak.2 lines = %d, want 3 (state before second revert)", n)
	}

	// Restore(1) swaps the archived transcript back, archiving the current
	// one under the next free number first.
	if err := s.Restore(1); err != nil {
		t.Fatal(err)
	}
	live, err := s.Messages()
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 4 || live[3].ID != msgs[3].ID {
		t.Fatalf("restored transcript = %+v", live)
	}
	if _, err := os.Stat(filepath.Join(s.Dir, "messages.jsonl.bak.1")); !os.IsNotExist(err) {
		t.Fatalf("bak.1 should be renamed away, err=%v", err)
	}
	b3, err := os.ReadFile(filepath.Join(s.Dir, "messages.jsonl.bak.3"))
	if err != nil {
		t.Fatalf("displaced transcript not archived: %v", err)
	}
	if n := strings.Count(string(b3), "\n"); n != 2 {
		t.Fatalf("bak.3 lines = %d, want 2", n)
	}
	// Restoring a backup that does not exist errors cleanly.
	if err := s.Restore(99); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("Restore(99) = %v", err)
	}
}
