package catalog

import "testing"

func TestLookup(t *testing.T) {
	ref := "anthropic/claude-sonnet-4-5"
	prov, model, m, ok := Lookup(ref)
	if !ok {
		t.Fatalf("Lookup(%s) not found", ref)
	}
	if prov != "anthropic" || model != "claude-sonnet-4-5" {
		t.Fatalf("split = %q/%q", prov, model)
	}
	if m.Context <= 0 || m.Cost == nil || m.Cost.Input <= 0 {
		t.Fatalf("entry missing context/cost: %+v", m)
	}
	if !m.Caps.ToolCall {
		t.Fatalf("expected tool_call capability: %+v", m.Caps)
	}
}

func TestLookupOpenRouterSlashes(t *testing.T) {
	// openrouter model ids contain slashes: provider splits at first only.
	_, model, _, ok := Lookup("openrouter/anthropic/claude-sonnet-4.5")
	if !ok {
		// pick any real key to keep the test data-independent
		p, pok := LookupProvider("openrouter")
		if !pok || len(p.Models) == 0 {
			t.Fatal("openrouter block missing")
		}
		for k := range p.Models {
			if _, _, _, ok := Lookup("openrouter/" + k); !ok {
				t.Fatalf("openrouter/%s not resolvable", k)
			}
			model = k
			break
		}
	}
	if model == "" {
		t.Fatal("no openrouter model resolved")
	}
}

func TestLookupMisses(t *testing.T) {
	for _, ref := range []string{
		"",              // empty
		"nodash",        // no slash
		"/leading",      // empty provider
		"trailing/",     // empty model
		"nosuch/model",  // provider absent
		"anthropic/nah", // model absent
	} {
		if _, _, _, ok := Lookup(ref); ok {
			t.Errorf("Lookup(%q) = found, want miss", ref)
		}
	}
}

func TestPrice(t *testing.T) {
	// claude-sonnet-4-5: input 3, output 15 per 1M (models.dev snapshot).
	usd, ok := Price("anthropic/claude-sonnet-4-5", 1_000_000, 100_000)
	if !ok {
		t.Fatal("no price")
	}
	expected := 3.0 + 0.1*15.0 // 1M in + 100k out
	if diff := usd - expected; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("usd = %v, want %v", usd, expected)
	}
	if _, ok := Price("nosuch/model", 1, 1); ok {
		t.Fatal("unknown model must not price")
	}
	if usd, ok := Price("anthropic/claude-sonnet-4-5", 0, 0); !ok || usd != 0 {
		t.Fatalf("zero usage: %v %v", usd, ok)
	}
}

func TestContextAndNames(t *testing.T) {
	if Context("anthropic/claude-sonnet-4-5") <= 0 {
		t.Fatal("context missing")
	}
	if Context("nope/nope") != 0 {
		t.Fatal("miss must be 0")
	}
	names := Names()
	if len(names) != 9 {
		t.Fatalf("embedded providers = %d, want 9", len(names))
	}
}
