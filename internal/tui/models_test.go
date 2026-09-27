package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"matcode/internal/config"
	"matcode/internal/store"
	"matcode/internal/tui/theme"
)

// TestModelChoicesVariants proves a provider's `models` list becomes
// one picker choice per model (row 35), that default_model falls back
// to the first entry, and that a provider without a credential stays
// out of the list.
func TestModelChoicesVariants(t *testing.T) {
	cwd := t.TempDir()
	mtc := filepath.Join(cwd, ".mtc")
	if err := os.MkdirAll(mtc, 0o755); err != nil {
		t.Fatal(err)
	}
	toml := `
[providers.mock]
dialect = "openai"
base_url = "http://127.0.0.1:9"
api_key = { env = "MTC_TEST_MOCK_KEY" }
models = ["t1", "t2", "t3"]

[providers.keyless]
dialect = "openai"
base_url = "http://127.0.0.1:9"
api_key = { env = "MTC_TEST_KEYLESS_KEY" }
models = ["x1"]
`
	if err := os.WriteFile(filepath.Join(mtc, "config.toml"), []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MTC_TEST_MOCK_KEY", "k")
	t.Setenv("MTC_TEST_KEYLESS_KEY", "")

	cfg, err := config.Load(cwd)
	if err != nil {
		t.Fatal(err)
	}
	// default_model defaults to the first declared model.
	if got := cfg.Providers["mock"].DefaultModel; got != "t1" {
		t.Fatalf("DefaultModel = %q, want t1", got)
	}

	a := &App{cfg: cfg, cwd: cwd, theme: theme.Default}
	var mocks []string
	for _, c := range a.modelChoices() {
		if strings.HasPrefix(c, "mock/") {
			mocks = append(mocks, c)
		}
		if strings.HasPrefix(c, "keyless/") {
			t.Errorf("keyless provider leaked into choices: %q", c)
		}
	}
	if strings.Join(mocks, ",") != "mock/t1,mock/t2,mock/t3" {
		t.Fatalf("mock choices = %v", mocks)
	}
}

// TestModelChoicesKeepsCurrentModel proves the active session model is
// offered even when its provider entry would not list it.
func TestModelChoicesKeepsCurrentModel(t *testing.T) {
	cwd := t.TempDir()
	cfg, err := config.Load(cwd)
	if err != nil {
		t.Fatal(err)
	}
	a := &App{cfg: cfg, cwd: cwd, theme: theme.Default}
	if len(a.modelChoices()) == 0 {
		t.Skip("no provider has a credential in this environment")
	}
	a.tabs = []*tab{{sess: sessWithModel("ghost/none")}}
	if got := a.modelChoices()[0]; got != "ghost/none" {
		t.Fatalf("first choice = %q, want ghost/none", got)
	}
}

// TestModelChoicesCatalogFallback: a keyed provider with no models or
// default falls back to the embedded catalog, filtered to chat models.
func TestModelChoicesCatalogFallback(t *testing.T) {
	t.Setenv("GROQ_API_KEY", "k")
	cwd := t.TempDir()
	cfg, err := config.Load(cwd)
	if err != nil {
		t.Fatal(err)
	}
	a := &App{cfg: cfg, cwd: cwd, theme: theme.Default}
	var groq []string
	for _, c := range a.modelChoices() {
		if strings.HasPrefix(c, "groq/") {
			groq = append(groq, c)
		}
	}
	if len(groq) == 0 {
		t.Fatal("no groq catalog choices")
	}
	for _, c := range groq {
		for _, bad := range []string{"image", "audio", "embed", "whisper"} {
			if strings.Contains(c, bad) {
				t.Errorf("non-chat id %q in choices", c)
			}
		}
	}
}

// sessWithModel builds a session carrying one model id.
func sessWithModel(model string) *store.Session {
	return &store.Session{Meta: store.Meta{ID: "ses_ghost", Model: model}}
}

// TestMergeLiveModels proves the fetched catalogue replaces the preset
// block only for providers the fetch reached, keeps presets for the
// rest, drops non-chat ids, and leaves the active model first.
func TestMergeLiveModels(t *testing.T) {
	static := []string{
		"openai/gpt-4o-mini",
		"anthropic/claude-fable-5",
		"orphan/preset-1",
	}
	live := map[string][]string{
		"openai":           {"gpt-5", "gpt-image-1", "text-embedding-3-small", "gpt-4o-mini"},
		"zz-fresh/unknown": {"alpha", "beta"},
	}
	got := mergeLiveModels(static, live, "openai/gpt-5")
	want := []string{
		"openai/gpt-5",             // active model first
		"openai/gpt-4o-mini",       // live ids, chat-capable only
		"anthropic/claude-fable-5", // preset kept: not fetched
		"orphan/preset-1",          // preset kept: not fetched
		"zz-fresh/unknown/alpha",   // live-only provider appended
		"zz-fresh/unknown/beta",
	}
	if len(got) != len(want) {
		t.Fatalf("mergeLiveModels = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("mergeLiveModels = %v, want %v", got, want)
		}
	}
}

// TestMergeLiveModelsNoFetch: without live data the static list is the
// answer, untouched.
func TestMergeLiveModelsNoFetch(t *testing.T) {
	static := []string{"openai/gpt-4o-mini"}
	if got := mergeLiveModels(static, nil, ""); len(got) != 1 || got[0] != static[0] {
		t.Fatalf("mergeLiveModels = %v, want %v", got, static)
	}
}
