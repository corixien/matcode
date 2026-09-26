// Package snapshots captures per-step file states in an internal git-style
// object store so undo/redo can restore the working tree next to the
// transcript. Capture is best-effort: it only runs inside a git worktree,
// skips git-ignored files and files over 2 MiB, and degrades to
// transcript-only rollback outside a repo or when capture is disabled.
package snapshots

import (
	"bytes"
	"compress/zlib"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// maxFileSize is the largest file captured (2 MiB, per the reference docs).
const maxFileSize = 2 << 20

// Entry is one manifest line: the file state before and after a single
// tool-using assistant step. Maps are worktree-relative path → blob hash.
type Entry struct {
	ID     string            `json:"id"`
	WD     string            `json:"wd"` // absolute worktree root at capture time
	Before map[string]string `json:"before,omitempty"`
	After  map[string]string `json:"after,omitempty"`
}

// Path is the snapshot journal inside a session folder.
func Path(sessionDir string) string {
	return filepath.Join(sessionDir, "snapshots.jsonl")
}

// Capturer records one before/after pair per tool-using assistant step.
type Capturer struct {
	workDir  string
	objects  string // object store root: <data dir>/objects
	manifest string // journal in the session folder
	exclude  string // data dir, never captured (it holds the session itself)
	pending  map[string]map[string]string
	repo     bool
	probed   bool
}

// New builds a capturer for workDir. objects is the shared object-store
// root, manifest the session journal, exclude the data dir ("" = nothing).
func New(workDir, objects, manifest, exclude string) *Capturer {
	return &Capturer{
		workDir:  workDir,
		objects:  objects,
		manifest: manifest,
		exclude:  exclude,
		pending:  map[string]map[string]string{},
	}
}

// Step records the file state before a tool-using assistant step.
func (c *Capturer) Step(id string) {
	if !c.inRepo() {
		return
	}
	before, err := c.enumerate()
	if err != nil {
		return
	}
	c.pending[id] = before
}

// Finish records the after state and journals the pair.
func (c *Capturer) Finish(id string) {
	if !c.inRepo() {
		return
	}
	after, err := c.enumerate()
	if err != nil {
		return
	}
	e := Entry{ID: id, WD: c.workDir, Before: c.pending[id], After: after}
	delete(c.pending, id)
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	f, err := os.OpenFile(c.manifest, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(b, '\n'))
}

// Restore puts the worktree back to state: paths missing from state are
// removed (created by the steps being reverted), differing paths are
// rewritten from the object store. Returns how many paths changed.
func (c *Capturer) Restore(state map[string]string) (int, error) {
	if !c.inRepo() {
		return 0, fmt.Errorf("not a git worktree; transcript-only rollback")
	}
	cur, err := c.enumerate()
	if err != nil {
		return 0, err
	}
	changed := 0
	for rel := range cur {
		if _, ok := state[rel]; !ok {
			full := filepath.Join(c.workDir, rel)
			// never touch the object store or the journal itself
			if c.objects != "" && strings.HasPrefix(full, c.objects+string(filepath.Separator)) {
				continue
			}
			if c.manifest != "" && full == c.manifest {
				continue
			}
			if err := os.Remove(full); err == nil {
				changed++
			}
		}
	}
	for rel, h := range state {
		if cur[rel] == h {
			continue
		}
		data, err := c.readObject(h)
		if err != nil {
			continue // best-effort: a missing blob skips, never aborts
		}
		full := filepath.Join(c.workDir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			continue
		}
		if err := os.WriteFile(full, data, 0o644); err != nil {
			continue
		}
		changed++
	}
	return changed, nil
}

// Load reads a session's snapshot journal; a session without one (repo-less
// run, capture disabled) yields no entries.
func Load(manifest string) []Entry {
	b, err := os.ReadFile(manifest)
	if err != nil {
		return nil
	}
	var out []Entry
	for _, line := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		var e Entry
		if json.Unmarshal([]byte(line), &e) == nil && e.ID != "" {
			out = append(out, e)
		}
	}
	return out
}

// Find returns the entry for one assistant step, or nil.
func Find(entries []Entry, id string) *Entry {
	for i := range entries {
		if entries[i].ID == id {
			return &entries[i]
		}
	}
	return nil
}

// inRepo probes git once: outside a worktree every capture and restore
// call becomes a no-op (conversation-only rollback).
func (c *Capturer) inRepo() bool {
	if !c.probed {
		c.probed = true
		c.repo = exec.Command("git", "-C", c.workDir, "rev-parse", "--is-inside-work-tree").Run() == nil
	}
	return c.repo
}

// enumerate hashes every in-scope file (tracked + non-ignored untracked,
// ≤2 MiB, excluding the data dir), storing each blob as it goes.
func (c *Capturer) enumerate() (map[string]string, error) {
	out, err := exec.Command("git", "-C", c.workDir, "ls-files", "-z", "--cached", "--others", "--exclude-standard").Output()
	if err != nil {
		return nil, fmt.Errorf("snapshots: git ls-files failed in %s", c.workDir)
	}
	state := map[string]string{}
	for _, rel := range strings.Split(strings.TrimRight(string(out), "\x00"), "\x00") {
		if rel == "" {
			continue
		}
		full := filepath.Join(c.workDir, rel)
		if c.exclude != "" && strings.HasPrefix(full, c.exclude+string(filepath.Separator)) {
			continue
		}
		st, err := os.Lstat(full)
		if err != nil || !st.Mode().IsRegular() || st.Size() > maxFileSize {
			continue
		}
		b, err := os.ReadFile(full)
		if err != nil {
			continue
		}
		state[rel] = c.putObject(b)
	}
	return state, nil
}

// putObject stores data as a git-style loose object (sha1 of
// "blob <len>\0<data>", zlib-compressed) and returns its hash.
func (c *Capturer) putObject(data []byte) string {
	h := sha1.New()
	fmt.Fprintf(h, "blob %d\x00", len(data))
	h.Write(data)
	sum := hex.EncodeToString(h.Sum(nil))
	path := filepath.Join(c.objects, sum[:2], sum[2:])
	if _, err := os.Stat(path); err == nil {
		return sum
	}
	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf)
	fmt.Fprintf(zw, "blob %d\x00", len(data))
	if _, err := zw.Write(data); err != nil {
		return sum
	}
	if zw.Close() != nil {
		return sum
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return sum
	}
	_ = os.WriteFile(path, buf.Bytes(), 0o644)
	return sum
}

// readObject loads a loose object's payload back out.
func (c *Capturer) readObject(sum string) ([]byte, error) {
	return readObjectAt(c.objects, sum)
}

// readObjectAt loads a blob from an object-store root (objects is the
// same root Capturer gets: <data dir>/objects).
func readObjectAt(objects, sum string) ([]byte, error) {
	if len(sum) < 3 {
		return nil, fmt.Errorf("snapshots: bad object hash %q", sum)
	}
	f, err := os.Open(filepath.Join(objects, sum[:2], sum[2:]))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	zr, err := zlib.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	raw, err := io.ReadAll(zr)
	if err != nil {
		return nil, err
	}
	sep := bytes.IndexByte(raw, 0)
	if sep < 0 || !bytes.HasPrefix(raw, []byte("blob ")) {
		return nil, fmt.Errorf("snapshots: corrupt object %s", sum)
	}
	var n int
	if _, err := fmt.Sscanf(string(raw[:sep]), "blob %d", &n); err != nil || n != len(raw)-sep-1 {
		return nil, fmt.Errorf("snapshots: corrupt object %s", sum)
	}
	return raw[sep+1:], nil
}

// ReadStep loads one step's before/after file contents (row 35 diff
// view): worktree-relative path → content for both sides. Missing blobs
// (gc, foreign object store) are skipped rather than failing the diff.
func ReadStep(objects string, e *Entry) (before, after map[string][]byte) {
	if e == nil {
		return nil, nil
	}
	read := func(state map[string]string) map[string][]byte {
		out := make(map[string][]byte, len(state))
		for path, sum := range state {
			if b, err := readObjectAt(objects, sum); err == nil {
				out[path] = b
			}
		}
		return out
	}
	return read(e.Before), read(e.After)
}
