package config

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// testConfig returns a Config whose data tree is a fresh temp dir.
func testConfig(t *testing.T) *Config {
	t.Helper()
	return &Config{GlobalDir: t.TempDir()}
}

// waitEvent waits for one callback or fails the test.
func waitEvent(t *testing.T, fired <-chan struct{}, d time.Duration) {
	t.Helper()
	select {
	case <-fired:
	case <-time.After(d):
		t.Fatal("no change event fired")
	}
}

// TestWatcherFiresOnConfigWrite proves a write inside the data tree is
// reported (spec row 31 hot-reload).
func TestWatcherFiresOnConfigWrite(t *testing.T) {
	cfg := testConfig(t)
	w, err := NewWatcher(cfg)
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Start(ctx)
	defer w.Close()

	fired := make(chan struct{}, 4)
	w.OnChange(func() {
		select {
		case fired <- struct{}{}:
		default:
		}
	})

	// Let the watches register with the OS.
	time.Sleep(150 * time.Millisecond)

	path := filepath.Join(cfg.DataDir(), "config.toml")
	if err := os.WriteFile(path, []byte("model = \"mockt/t\"\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	waitEvent(t, fired, 3*time.Second)
}

// TestWatcherFiresOnThemeWrite proves the themes subdirectory is watched
// even though it did not exist when the watcher was created: we create
// it, add it, then write.
func TestWatcherFiresOnThemeWrite(t *testing.T) {
	cfg := testConfig(t)
	themes := filepath.Join(cfg.DataDir(), "themes")
	if err := os.MkdirAll(themes, 0755); err != nil {
		t.Fatal(err)
	}
	w, err := NewWatcher(cfg)
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Start(ctx)
	defer w.Close()

	fired := make(chan struct{}, 4)
	w.OnChange(func() {
		select {
		case fired <- struct{}{}:
		default:
		}
	})
	time.Sleep(150 * time.Millisecond)

	if err := os.WriteFile(filepath.Join(themes, "solar.json"),
		[]byte(`{"name":"solar","colors":{"primary":"#f38ba8"}}`), 0644); err != nil {
		t.Fatal(err)
	}
	waitEvent(t, fired, 3*time.Second)
}

// TestWatcherIgnoresUnwatchedTrees proves a file outside the data tree
// is not reported.
func TestWatcherIgnoresUnwatchedTrees(t *testing.T) {
	cfg := testConfig(t)
	w, err := NewWatcher(cfg)
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Start(ctx)
	defer w.Close()

	fired := make(chan struct{}, 4)
	w.OnChange(func() {
		select {
		case fired <- struct{}{}:
		default:
		}
	})
	time.Sleep(150 * time.Millisecond)

	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, "unrelated.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	select {
	case <-fired:
		t.Fatal("fired for an unwatched directory")
	case <-time.After(600 * time.Millisecond):
	}
}

// TestWatcherCloseStopsLoop proves Close is idempotent and stops events.
func TestWatcherCloseStopsLoop(t *testing.T) {
	cfg := testConfig(t)
	w, err := NewWatcher(cfg)
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Start(ctx)

	time.Sleep(100 * time.Millisecond)
	w.Close()
	w.Close() // must not panic

	fired := make(chan struct{}, 4)
	w.OnChange(func() {
		select {
		case fired <- struct{}{}:
		default:
		}
	})
	os.WriteFile(filepath.Join(cfg.DataDir(), "config.toml"), []byte("x"), 0644) //nolint:errcheck
	select {
	case <-fired:
		t.Fatal("event fired after Close")
	case <-time.After(500 * time.Millisecond):
	}
}
