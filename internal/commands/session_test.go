package commands

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"matcode/internal/config"
	"matcode/internal/store"
)

// withProject chdirs into a fresh temp dir with a `.mtc` folder, so config
// resolves DataDir inside the test instead of the real global data tree.
func withProject(t *testing.T) *config.Config {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".mtc"), 0o755); err != nil {
		t.Fatal(err)
	}
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// seed creates n sessions, each with two messages and distinct metadata.
func seed(t *testing.T, cfg *config.Config, n int) []string {
	t.Helper()
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		s, err := store.Create(cfg.SessionsDir(), "mock/t")
		if err != nil {
			t.Fatal(err)
		}
		s.Meta.Title = "session " + strings.Repeat("x", i+1)
		s.Meta.Agent = "build"
		if err := s.Save(); err != nil {
			t.Fatal(err)
		}
		for _, role := range []string{"user", "assistant"} {
			if err := s.Append(&store.Message{Role: role, Content: "body"}); err != nil {
				t.Fatal(err)
			}
		}
		ids = append(ids, s.Meta.ID)
		// Distinct Updated stamps: same-millisecond sessions would tie and
		// fall back to random id ordering.
		time.Sleep(2 * time.Millisecond)
	}
	return ids
}

func TestSessionListJSON(t *testing.T) {
	cfg := withProject(t)
	ids := seed(t, cfg, 2)

	r, w, _ := os.Pipe()
	old := os.Stdout
	os.Stdout = w
	err := Session(context.Background(), []string{"list", "-format", "json"})
	os.Stdout = old
	w.Close()
	if err != nil {
		t.Fatal(err)
	}
	var out []struct {
		ID   string `json:"id"`
		Msgs int    `json:"msgs"`
	}
	if err := json.NewDecoder(r).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("want 2 rows, got %d", len(out))
	}
	// Newest first.
	if out[0].ID != ids[1] || out[1].ID != ids[0] {
		t.Fatalf("order: %s, %s", out[0].ID, out[1].ID)
	}
	if out[0].Msgs != 2 {
		t.Fatalf("msgs = %d, want 2", out[0].Msgs)
	}

	// -n truncates.
	r2, w2, _ := os.Pipe()
	os.Stdout = w2
	err = Session(context.Background(), []string{"list", "-format", "json", "-n", "1"})
	os.Stdout = old
	w2.Close()
	if err != nil {
		t.Fatal(err)
	}
	out = nil
	if err := json.NewDecoder(r2).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].ID != ids[1] {
		t.Fatalf("-n 1 gave %d rows (%+v)", len(out), out)
	}
}

func TestSessionDelete(t *testing.T) {
	cfg := withProject(t)
	ids := seed(t, cfg, 1)

	if err := Session(context.Background(), []string{"delete", ids[0]}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cfg.SessionsDir(), ids[0])); !os.IsNotExist(err) {
		t.Fatalf("folder still there: %v", err)
	}
	if err := Session(context.Background(), []string{"delete", ids[0]}); err == nil ||
		!strings.Contains(err.Error(), "no session") {
		t.Fatalf("second delete should fail: %v", err)
	}
	for _, bad := range []string{"..", "../etc", "a/b", ""} {
		err := Session(context.Background(), []string{"delete", bad})
		if err == nil || (!strings.Contains(err.Error(), "invalid session id") &&
			!strings.Contains(err.Error(), "usage")) {
			t.Fatalf("delete %q must be rejected, got %v", bad, err)
		}
	}
	if err := Session(context.Background(), []string{"delete"}); err == nil {
		t.Fatal("bare delete must fail")
	}
}

func TestSessionExportImportRoundtrip(t *testing.T) {
	cfg := withProject(t)
	ids := seed(t, cfg, 1)
	s, err := store.Open(filepath.Join(cfg.SessionsDir(), ids[0]))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Dir, "checkpoint.md"), []byte("## Objective\nbuild\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	file := filepath.Join(t.TempDir(), "dump.json")
	if err := Session(context.Background(), []string{"export", ids[0], "-o", file}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var doc exportDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Version != 1 || doc.Session.ID != ids[0] || len(doc.Messages) != 2 {
		t.Fatalf("doc: version=%d id=%s msgs=%d", doc.Version, doc.Session.ID, len(doc.Messages))
	}
	if doc.Files["checkpoint.md"] != "## Objective\nbuild\n" {
		t.Fatalf("checkpoint not exported: %q", doc.Files["checkpoint.md"])
	}

	if err := Session(context.Background(), []string{"import", file}); err != nil {
		t.Fatal(err)
	}
	metas, err := store.List(cfg.SessionsDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 2 {
		t.Fatalf("want 2 sessions after import, got %d", len(metas))
	}
	var imported *store.Meta
	for i := range metas {
		if metas[i].ID != ids[0] {
			imported = &metas[i]
		}
	}
	if imported == nil {
		t.Fatal("no new session created")
	}
	if imported.Title == "" || imported.Agent != "build" {
		t.Fatalf("metadata lost: %+v", imported)
	}
	got, err := store.Open(filepath.Join(cfg.SessionsDir(), imported.ID))
	if err != nil {
		t.Fatal(err)
	}
	msgs, err := got.Messages()
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 || msgs[0].Role != "user" || msgs[1].Role != "assistant" {
		t.Fatalf("messages: %+v", msgs)
	}
	// Every imported message has an id (undo/revert key on it).
	for _, m := range msgs {
		if m.ID == "" {
			t.Fatalf("message without id: %+v", m)
		}
	}
	if b, err := os.ReadFile(filepath.Join(got.Dir, "checkpoint.md")); err != nil ||
		string(b) != "## Objective\nbuild\n" {
		t.Fatalf("checkpoint file lost: %q %v", b, err)
	}
	// -export of the import equals the original transcript.
	file2 := filepath.Join(t.TempDir(), "dump2.json")
	if err := Session(context.Background(), []string{"export", imported.ID, "-o", file2}); err != nil {
		t.Fatal(err)
	}
	raw2, err := os.ReadFile(file2)
	if err != nil {
		t.Fatal(err)
	}
	var doc2 exportDoc
	if err := json.Unmarshal(raw2, &doc2); err != nil {
		t.Fatal(err)
	}
	if len(doc2.Messages) != 2 || doc2.Messages[0].Content != "body" {
		t.Fatalf("roundtrip messages: %+v", doc2.Messages)
	}
}

func TestSessionExportErrors(t *testing.T) {
	cfg := withProject(t)
	// No sessions at all.
	if err := Session(context.Background(), []string{"export"}); err == nil {
		t.Fatal("export with no sessions must fail")
	}
	seed(t, cfg, 1)
	if err := Session(context.Background(), []string{"export", "../etc"}); err == nil {
		t.Fatal("traversal id must be rejected")
	}
	if err := Session(context.Background(), []string{"export", "a", "b"}); err == nil {
		t.Fatal("too many args must be rejected")
	}
	if err := Session(context.Background(), []string{"import"}); err == nil {
		t.Fatal("import without file must fail")
	}
	if err := Session(context.Background(), []string{"import", "/nope.json"}); err == nil {
		t.Fatal("missing file must fail")
	}
	if err := Session(context.Background(), []string{"import", "-"}); err == nil {
		t.Fatal("stdin import with garbage must fail")
	}
	if err := Session(context.Background(), []string{"nope"}); err == nil {
		t.Fatal("unknown verb must fail")
	}
	if err := Session(context.Background(), nil); err == nil {
		t.Fatal("bare session must fail")
	}
	// Unsupported version.
	bad := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(bad, []byte(`{"version":9}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Session(context.Background(), []string{"import", bad}); err == nil ||
		!strings.Contains(err.Error(), "version") {
		t.Fatalf("want version error, got %v", err)
	}
}

func TestSanitize(t *testing.T) {
	cases := []struct{ in, want, forbid string }{
		{"api_key = sk-abcdefghijklmnop", "***redacted***", "sk-abcdefghijklmnop"},
		{`"token":"ghp_abcdefghij1234567890abcd"`, "***redacted***", "ghp_abcdefghij"},
		{"authorization: Bearer 1234567890abcdef1234567890abcdef", "***redacted***", "abcdef1234567890"},
		{"password: hunter2hunter2", "***redacted***", "hunter2hunter2"},
		{"AWSKey AKIA1234567890ABCDEF", "***redacted***", "AKIA1234567890ABCDEF"},
		{"-----BEGIN RSA PRIVATE KEY-----\nMIIE\n-----END RSA PRIVATE KEY-----",
			"***redacted private key***", "MIIE"},
	}
	for _, c := range cases {
		got := Sanitize(c.in)
		if !strings.Contains(got, c.want) {
			t.Errorf("Sanitize(%q) = %q, want %q", c.in, got, c.want)
		}
		if strings.Contains(got, c.forbid) {
			t.Errorf("Sanitize(%q) leaked %q: %q", c.in, c.forbid, got)
		}
	}
	// Home directory is folded to ~; unrelated text is untouched.
	home, _ := os.UserHomeDir()
	if home != "" && home != "/" {
		got := Sanitize("see " + home + "/secret.txt")
		if strings.Contains(got, home) || !strings.Contains(got, "~/secret.txt") {
			t.Errorf("home not folded: %q", got)
		}
	}
	if got := Sanitize("plain text, no secrets"); got != "plain text, no secrets" {
		t.Errorf("clean text changed: %q", got)
	}
}
