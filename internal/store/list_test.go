package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestListEmptyAndMissing(t *testing.T) {
	root := t.TempDir()
	got, err := List(filepath.Join(root, "nope"))
	if err != nil || len(got) != 0 {
		t.Fatalf("missing root: got %d, err %v", len(got), err)
	}
	if got, err = List(root); err != nil || len(got) != 0 {
		t.Fatalf("empty root: got %d, err %v", len(got), err)
	}
}

func TestListOrderAndLatest(t *testing.T) {
	root := t.TempDir()
	ids := make([]string, 3)
	for i := range ids {
		s, err := Create(root, "mock/t")
		if err != nil {
			t.Fatal(err)
		}
		ids[i] = s.Meta.ID
		// List tie-breaks equal Updated stamps on the id's random suffix,
		// so creation order only survives with distinct milliseconds.
		if err := s.Append(&Message{Role: "user", Content: "hi"}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond)
	}
	metas, err := List(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 3 {
		t.Fatalf("want 3 sessions, got %d", len(metas))
	}
	// Newest first: the last created id sorts at index 0.
	if metas[0].ID != ids[2] || metas[2].ID != ids[0] {
		t.Fatalf("order wrong: %s, %s, %s", metas[0].ID, metas[1].ID, metas[2].ID)
	}
	latest, err := Latest(root)
	if err != nil {
		t.Fatal(err)
	}
	if latest.Meta.ID != ids[2] {
		t.Fatalf("Latest = %s, want %s", latest.Meta.ID, ids[2])
	}
	if latest.Meta.Created == 0 || latest.Meta.Updated == 0 {
		t.Fatalf("timestamps not stamped: %+v", latest.Meta)
	}
}

func TestListSkipsUnreadable(t *testing.T) {
	root := t.TempDir()
	good, err := Create(root, "mock/t")
	if err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(root, "ses_broken")
	if err := os.MkdirAll(bad, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bad, "session.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "stray.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	metas, err := List(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 1 || metas[0].ID != good.Meta.ID {
		t.Fatalf("want only the good session, got %+v", metas)
	}
}

func TestLatestWithoutSessions(t *testing.T) {
	if _, err := Latest(t.TempDir()); err == nil {
		t.Fatal("expected an error for an empty sessions dir")
	}
}

func TestSaveMeta(t *testing.T) {
	root := t.TempDir()
	s, err := Create(root, "mock/t")
	if err != nil {
		t.Fatal(err)
	}
	s.Meta.Title = "Refactor the build"
	s.Meta.Agent = "plan"
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	again, err := Open(s.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if again.Meta.Title != "Refactor the build" || again.Meta.Agent != "plan" {
		t.Fatalf("meta not persisted: %+v", again.Meta)
	}
}
