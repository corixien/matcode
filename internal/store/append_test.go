package store

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newSess creates a session rooted in a fresh temp dir (spec: a folder is the
// truth), mirroring engine/compaction_test's newSession.
func newSess(t *testing.T) *Session {
	t.Helper()
	s, err := Create(t.TempDir(), "mock/t")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestAppendAssignsIDsAndKeepsOrder proves Append fills id/time only when
// unset, keeps a caller-set identity, and writes lines in call order.
func TestAppendAssignsIDsAndKeepsOrder(t *testing.T) {
	s := newSess(t)
	in := []*Message{
		{Role: "user", Content: "first"},
		{Role: "assistant", Content: "reply", ToolCalls: []ToolCall{{ID: "call_1", Name: "read", Arguments: `{"p":1}`}}},
		{Role: "tool", Content: "body", ToolCallID: "call_1", Media: []Media{{Type: "image/png", Data: "QUJD", Name: "a.png"}}},
	}
	for _, m := range in {
		if err := s.Append(m); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	for i, m := range in {
		if !strings.HasPrefix(m.ID, "msg_") || len(m.ID) != 29 {
			t.Errorf("msg %d id = %q, want msg_ + 25 chars", i, m.ID)
		}
		if seen[m.ID] {
			t.Errorf("duplicate id %q", m.ID)
		}
		seen[m.ID] = true
		if m.Time == 0 {
			t.Errorf("msg %d time not stamped", i)
		}
	}
	// Caller-supplied identity survives (side data keys off it).
	custom := &Message{ID: "msg_custom", Role: "user", Content: "pinned"}
	if err := s.Append(custom); err != nil {
		t.Fatal(err)
	}
	if custom.ID != "msg_custom" {
		t.Fatalf("Append overwrote a set id: %q", custom.ID)
	}

	got, err := s.Messages()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(in)+1 || got[3].ID != "msg_custom" {
		t.Fatalf("Messages = %d, want %d including the pinned id", len(got), len(in)+1)
	}
	for i := range in {
		if got[i].ID != in[i].ID || got[i].Role != in[i].Role || got[i].Content != in[i].Content {
			t.Errorf("order/content broken at %d: %+v", i, got[i])
		}
	}
	if got[1].ToolCalls[0].Arguments != `{"p":1}` {
		t.Errorf("tool call round trip: %+v", got[1].ToolCalls)
	}
	if len(got[2].Media) != 1 || got[2].Media[0].Data != "QUJD" || got[2].ToolCallID != "call_1" {
		t.Errorf("media/tool id round trip: %+v", got[2])
	}
	// The file itself is JSONL: one object per line, newline-terminated.
	raw, err := os.ReadFile(filepath.Join(s.Dir, "messages.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) != len(in)+1 || !strings.HasSuffix(string(raw), "\n") {
		t.Fatalf("not JSONL: %d lines, trailing newline=%v", len(lines), strings.HasSuffix(string(raw), "\n"))
	}
	for _, ln := range lines {
		if !json.Valid([]byte(ln)) {
			t.Fatalf("invalid JSON line: %q", ln)
		}
	}
}

// TestAppendPersistsAcrossReopen proves the transcript and metadata survive
// Open, and that a reopened session appends (O_APPEND) instead of rewriting.
func TestAppendPersistsAcrossReopen(t *testing.T) {
	s := newSess(t)
	const t1, t2, t3 int64 = 1900000000001, 1900000000002, 1900000000003
	for _, m := range []*Message{
		{Role: "user", Content: "one", Time: t1},
		{Role: "assistant", Content: "two", Time: t2},
	} {
		if err := s.Append(m); err != nil {
			t.Fatal(err)
		}
	}
	again, err := Open(s.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if again.Meta.Model != "mock/t" || again.Meta.ID != s.Meta.ID {
		t.Fatalf("meta not persisted: %+v", again.Meta)
	}
	if again.Meta.Updated != t2 {
		t.Fatalf("Updated = %d, want last message time %d", again.Meta.Updated, t2)
	}
	if err := again.Append(&Message{Role: "user", Content: "three", Time: t3}); err != nil {
		t.Fatal(err)
	}
	// First handle still sees the full transcript: append never truncates.
	all, err := s.Messages()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 || all[2].Content != "three" {
		t.Fatalf("append after reopen lost data: %+v", all)
	}
	if all[0].ID == "" || all[0].ID == all[1].ID {
		t.Fatalf("ids not persisted: %q %q", all[0].ID, all[1].ID)
	}
	final, err := Open(s.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if final.Meta.Updated != t3 {
		t.Fatalf("Updated = %d, want %d", final.Meta.Updated, t3)
	}
}

// TestAppendUpdatesMetaEvenWithSetTime pins current behavior: Updated always
// becomes the appended message's time, even a caller-supplied one.
func TestAppendUpdatesMetaEvenWithSetTime(t *testing.T) {
	s := newSess(t)
	if err := s.Append(&Message{Role: "user", Content: "hi", Time: 1900000000050}); err != nil {
		t.Fatal(err)
	}
	if s.Meta.Updated != 1900000000050 {
		t.Fatalf("Updated = %d, want 1900000000050", s.Meta.Updated)
	}
	b, err := os.ReadFile(filepath.Join(s.Dir, "session.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m Meta
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m.Updated != 1900000000050 {
		t.Fatalf("session.json Updated = %d", m.Updated)
	}
}

// TestMessagesSkipsTruncatedTail pins the documented crash resilience: a
// partial final line (killed mid-write) is skipped, not an error.
func TestMessagesSkipsTruncatedTail(t *testing.T) {
	s := newSess(t)
	for _, c := range []string{"a", "b"} {
		if err := s.Append(&Message{Role: "user", Content: c}); err != nil {
			t.Fatal(err)
		}
	}
	f, err := os.OpenFile(filepath.Join(s.Dir, "messages.jsonl"), os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"id":"msg_cut","role":"user","cont`); err != nil {
		t.Fatal(err)
	}
	f.Close()
	got, err := s.Messages()
	if err != nil {
		t.Fatalf("truncated tail must not error: %v", err)
	}
	if len(got) != 2 || got[1].Content != "b" {
		t.Fatalf("Messages = %+v, want the 2 complete messages", got)
	}
}

// TestAddUsageAccumulates proves totals fold across turns and persist.
func TestAddUsageAccumulates(t *testing.T) {
	s := newSess(t)
	steps := []struct {
		in, out int
		cost    float64
	}{
		{100, 20, 0.25},
		{50, 10, 0.5},
		{0, 0, 0},
	}
	for i, st := range steps {
		if err := s.AddUsage(st.in, st.out, st.cost); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
	}
	if s.Meta.TokensIn != 150 || s.Meta.TokensOut != 30 {
		t.Fatalf("totals = %d/%d, want 150/30", s.Meta.TokensIn, s.Meta.TokensOut)
	}
	if math.Abs(s.Meta.Cost-0.75) > 1e-9 {
		t.Fatalf("cost = %v, want 0.75", s.Meta.Cost)
	}
	again, err := Open(s.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if again.Meta.TokensIn != 150 || again.Meta.TokensOut != 30 {
		t.Fatalf("usage not persisted: %+v", again.Meta)
	}
	if math.Abs(again.Meta.Cost-0.75) > 1e-9 {
		t.Fatalf("cost not persisted: %v", again.Meta.Cost)
	}
	// Usage never touches the transcript.
	if _, err := os.Stat(filepath.Join(s.Dir, "messages.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("AddUsage must not create a transcript, err=%v", err)
	}
}
