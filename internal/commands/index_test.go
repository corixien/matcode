package commands

import (
	"strings"
	"testing"

	"matcode/internal/config"
	"matcode/internal/store"
)

// seedSession creates one session with a message containing word.
func seedSession(t *testing.T, word string) string {
	t.Helper()
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	s, err := store.Create(cfg.SessionsDir(), "mock/t")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Append(&store.Message{Role: "user", Content: word}); err != nil {
		t.Fatal(err)
	}
	return s.Meta.ID
}

func TestIndexRebuildThenSearch(t *testing.T) {
	withProjectConfig(t, "")
	id := seedSession(t, "zebra standoff")

	out := captureStdout(t, func() error { return Index([]string{"rebuild"}) })
	if !strings.Contains(out, "indexed 1 session") {
		t.Fatalf("rebuild output = %q", out)
	}

	out = captureStdout(t, func() error {
		return Index([]string{"search", "zebra"})
	})
	if !strings.Contains(out, id) {
		t.Fatalf("search output = %q, want session id %s", out, id)
	}
}

func TestIndexSearchWithoutRebuildIsError(t *testing.T) {
	withProjectConfig(t, "")
	err := Index([]string{"search", "zebra"})
	if err == nil {
		t.Fatal("search before rebuild must fail, not scan JSONL")
	}
	if !strings.Contains(err.Error(), "rebuild") {
		t.Fatalf("error should name the fix: %v", err)
	}
}

func TestIndexUsageErrors(t *testing.T) {
	withProjectConfig(t, "")
	for _, args := range [][]string{
		nil,
		{"frobnicate"},
		{"search"}, // missing query
	} {
		if err := Index(args); err == nil {
			t.Errorf("Index(%v) = nil error, want usage error", args)
		}
	}
}
