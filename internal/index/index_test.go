package index

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// mkSession writes one session folder: session.json meta + JSONL transcript.
func mkSession(t *testing.T, dataDir, id, title, text string, updated int64) string {
	t.Helper()
	dir := filepath.Join(dataDir, "sessions", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	meta, _ := json.Marshal(map[string]any{
		"id": id, "title": title, "model": "openai/gpt-x", "agent": "build", "updated": updated,
	})
	if err := os.WriteFile(filepath.Join(dir, "session.json"), meta, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "messages.jsonl"),
		[]byte(text+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestRebuildAndSearch(t *testing.T) {
	data := t.TempDir()
	mkSession(t, data, "s1", "login page fix", "the login form broke on safari", 100)
	mkSession(t, data, "s2", "refactor parser", "moved the tokenizer into its own package", 200)
	mkSession(t, data, "s3", "", "tokenize everything", 300)

	n, err := Rebuild(data)
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if n != 3 {
		t.Fatalf("indexed %d sessions, want 3", n)
	}

	// token hit in body
	hits, err := Search(data, "tokenizer", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 || hits[0].ID != "s2" {
		t.Fatalf("tokenizer hits = %+v, want s2", hits)
	}

	// title substring gets a boost over a body-only token match
	hits, err = Search(data, "login", 10)
	if err != nil {
		t.Fatalf("Search login: %v", err)
	}
	if len(hits) != 1 || hits[0].ID != "s1" {
		t.Fatalf("login hits = %+v, want s1", hits)
	}
	if hits[0].Score < 3 { // 1 body token + 2 title boost
		t.Fatalf("title boost missing, score=%d", hits[0].Score)
	}

	// multi-token: sessions must match any token (OR semantics via counts)
	hits, err = Search(data, "safari", 10)
	if err != nil {
		t.Fatalf("Search safari: %v", err)
	}
	if len(hits) != 1 || hits[0].ID != "s1" {
		t.Fatalf("safari hits = %+v, want s1", hits)
	}
}

func TestSearchRankingAndLimit(t *testing.T) {
	data := t.TempDir()
	mkSession(t, data, "old", "grep usage", "grep usage pattern", 100)
	mkSession(t, data, "new", "grep integration", "grep", 900)
	if _, err := Rebuild(data); err != nil {
		t.Fatal(err)
	}
	all, err := Search(data, "grep usage", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("want both sessions, got %d", len(all))
	}
	if all[0].ID != "old" || all[0].Score <= all[1].Score {
		t.Fatalf("want multi-token match ranked first, got %+v", all)
	}
	hits, err := Search(data, "grep usage", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].ID != "old" {
		t.Fatalf("limit ignored or wrong order: %+v", hits)
	}
}

func TestSearchTitleSubstring(t *testing.T) {
	data := t.TempDir()
	mkSession(t, data, "a", "Fix the login bug", "unrelated body", 1)
	if _, err := Rebuild(data); err != nil {
		t.Fatal(err)
	}
	// not tokenizable into ≥2-char tokens? it is; but multi-word title
	// substring must still match when no body token does.
	hits, err := Search(data, "login bug", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].ID != "a" {
		t.Fatalf("hits = %+v, want a", hits)
	}
}

func TestSearchMissingIndexIsError(t *testing.T) {
	data := t.TempDir() // no index.db
	if _, err := Search(data, "x", 10); err == nil {
		t.Fatal("missing index must error, not silently scan")
	}
}

func TestRebuildReflectsDeletion(t *testing.T) {
	data := t.TempDir()
	dir := mkSession(t, data, "gone", "temporary", "later deleted", 5)
	if _, err := Rebuild(data); err != nil {
		t.Fatal(err)
	}
	if hits, _ := Search(data, "deleted", 10); len(hits) != 1 {
		t.Fatalf("pre-delete hits = %d, want 1", len(hits))
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := Rebuild(data); err != nil {
		t.Fatal(err)
	}
	if hits, err := Search(data, "deleted", 10); err != nil || len(hits) != 0 {
		t.Fatalf("post-delete hits = %v err=%v, want none", hits, err)
	}
}

func TestRebuildSkipsJunkDirs(t *testing.T) {
	data := t.TempDir()
	mkSession(t, data, "good", "real session", "hello world", 1)
	junk := filepath.Join(data, "sessions", "junk")
	if err := os.MkdirAll(junk, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(junk, "session.json"), []byte("{bad"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "sessions", "loose.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	n, err := Rebuild(data)
	if err != nil {
		t.Fatalf("Rebuild must survive junk: %v", err)
	}
	if n != 1 {
		t.Fatalf("indexed %d, want 1 (junk skipped)", n)
	}
}
