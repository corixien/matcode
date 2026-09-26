package engine

import (
	"context"
	"strings"
	"testing"
)

// TestCompleteWithNoProvider proves a turn with no credential fails with a
// fixable /key message instead of panicking — the TUI boots without a key
// and the engine guard reports it at turn time.
func TestCompleteWithNoProvider(t *testing.T) {
	e := &Engine{Model: "anthropic/claude-sonnet-4-5"}
	out, calls, _, err := e.completeWith(context.Background(), nil, "sys", nil)
	if err == nil {
		t.Fatal("want error without provider")
	}
	if !strings.Contains(err.Error(), "/key") {
		t.Errorf("error should mention /key, got: %v", err)
	}
	if !strings.Contains(err.Error(), "anthropic/claude-sonnet-4-5") {
		t.Errorf("error should name the model, got: %v", err)
	}
	if out != "" || calls != nil {
		t.Errorf("output = %q, calls = %v; want empty", out, calls)
	}
}
