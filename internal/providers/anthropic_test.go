package providers

import (
	"encoding/json"
	"testing"
)

// The system prompt travels as a cached text block; empty stays absent.
func TestAnthropicSystemCachesLastBlock(t *testing.T) {
	if anthropicSystem("") != nil {
		t.Fatal("empty system should be omitted")
	}
	b, err := json.Marshal(anthropicSystem("sys prompt"))
	if err != nil {
		t.Fatal(err)
	}
	var blocks []map[string]any
	if err := json.Unmarshal(b, &blocks); err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 1 || blocks[0]["text"] != "sys prompt" {
		t.Fatalf("blocks = %v", blocks)
	}
	cc, ok := blocks[0]["cache_control"].(map[string]any)
	if !ok || cc["type"] != "ephemeral" {
		t.Fatalf("cache_control = %#v", blocks[0]["cache_control"])
	}
}

// A thinking budget must sit strictly below max_tokens; the helper lifts
// max_tokens to budget + headroom.
func TestAnthropicThinkingBudgetRaisesMaxTokens(t *testing.T) {
	m := map[string]any{
		"max_tokens": float64(4096),
		"thinking":   map[string]any{"type": "enabled", "budget_tokens": float64(100000)},
	}
	got := anthropicThinkingBudget(m).(map[string]any)
	if got["max_tokens"].(float64) < 100000 {
		t.Fatalf("max_tokens = %v, want > 100000", got["max_tokens"])
	}
	// No thinking → untouched; already large → untouched.
	plain := map[string]any{"max_tokens": float64(4096)}
	if anthropicThinkingBudget(plain).(map[string]any)["max_tokens"].(float64) != 4096 {
		t.Fatal("plain payload was modified")
	}
	big := map[string]any{
		"max_tokens": float64(200000),
		"thinking":   map[string]any{"budget_tokens": float64(10000)},
	}
	if anthropicThinkingBudget(big).(map[string]any)["max_tokens"].(float64) != 200000 {
		t.Fatal("max_tokens should stay untouched when already sufficient")
	}
}
