package config

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Watcher watches the data trees (config, themes, commands, agents) for
// changes so a running TUI/serve can hot-reload them (spec row 31).
// Directories that do not exist yet are skipped; creating them later is
// picked up by the next Watch/Add cycle the caller may trigger.
type Watcher struct {
	w        *fsnotify.Watcher
	mu       sync.Mutex
	onChange []func()
	done     chan struct{}
}

// NewWatcher watches cfg.DataDir() and its reloadable subdirectories.
// Both the project and global trees are watched when they differ.
func NewWatcher(cfg *Config) (*Watcher, error) {
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	w := &Watcher{w: fw, done: make(chan struct{})}
	roots := []string{cfg.DataDir()}
	if cfg.GlobalDir != "" && cfg.GlobalDir != cfg.DataDir() {
		roots = append(roots, cfg.GlobalDir)
	}
	for _, root := range roots {
		w.watchTree(root)
	}
	return w, nil
}

// watchTree adds root plus the reloadable subdirs, silently skipping
// paths that do not exist (a fresh install has no themes/ yet).
func (w *Watcher) watchTree(root string) {
	if root == "" {
		return
	}
	dirs := []string{
		root,
		filepath.Join(root, "themes"),
		filepath.Join(root, "commands"),
		filepath.Join(root, "agents"),
		filepath.Join(root, "tools"),
		filepath.Join(root, "plugins"),
		filepath.Join(root, "skills"),
	}
	// Plugin entry files live one level deeper (plugins/<name>/main.py), so
	// each plugin folder is watched on its own too. Skills follow the same
	// layout (skills/<name>/SKILL.md) — a file change inside one does not
	// touch the parent dir's mtime, so those are watched as well (row 37).
	for _, base := range []string{"plugins", "skills"} {
		baseDir := filepath.Join(root, base)
		if entries, err := os.ReadDir(baseDir); err == nil {
			for _, e := range entries {
				if e.IsDir() {
					dirs = append(dirs, filepath.Join(baseDir, e.Name()))
				}
			}
		}
	}
	for _, d := range dirs {
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			w.w.Add(d) //nolint:errcheck // a failed add is not fatal
		}
	}
}

// Add registers another directory to watch (used by callers that create
// a data tree after startup).
func (w *Watcher) Add(dir string) {
	if st, err := os.Stat(dir); err == nil && st.IsDir() {
		w.w.Add(dir) //nolint:errcheck
	}
}

// OnChange registers a callback fired on any watched write/create/remove.
func (w *Watcher) OnChange(fn func()) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.onChange = append(w.onChange, fn)
}

// Start runs the event loop until ctx is done or Close is called.
func (w *Watcher) Start(ctx context.Context) {
	defer w.closeOnce()
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.done:
			return
		case event, ok := <-w.w.Events:
			if !ok {
				return
			}
			if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove|fsnotify.Rename) == 0 {
				continue
			}
			// Coalesce: editors write a file in several syscalls (and
			// mv-based saves fire rename+create). One burst, one reload.
			debounce(w.w.Events, 150*time.Millisecond)
			w.mu.Lock()
			fns := append([]func(){}, w.onChange...)
			w.mu.Unlock()
			for _, fn := range fns {
				fn()
			}
		case _, ok := <-w.w.Errors:
			if !ok {
				return
			}
			// Watch errors are transient (a deleted dir); keep going.
		}
	}
}

// Close stops the loop and releases the OS watcher.
func (w *Watcher) Close() { w.closeOnce() }

func (w *Watcher) closeOnce() {
	select {
	case <-w.done:
	default:
		close(w.done)
		w.w.Close() //nolint:errcheck
	}
}

// debounce drains further events for d after the first one, so one save
// (write+rename+chmod, or a multi-chunk write) yields a single reload.
func debounce(ch <-chan fsnotify.Event, d time.Duration) {
	deadline := time.Now().Add(d)
	for {
		select {
		case <-ch:
			if time.Now().After(deadline) {
				return
			}
		case <-time.After(time.Until(deadline)):
			return
		}
	}
}
