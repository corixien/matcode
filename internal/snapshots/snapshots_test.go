package snapshots

import (
	"bytes"
	"compress/zlib"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// requireGit pins git config to /dev/null so global excludes/rewrites can
// never change what the capturer enumerates.
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
}

func gitInit(t *testing.T, dir string) {
	t.Helper()
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
}

// blobHash is the git loose-object hash the store promises: sha1 of
// "blob <len>\0<data>".
func blobHash(data []byte) string {
	h := sha1.New()
	fmt.Fprintf(h, "blob %d\x00", len(data))
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

type snapEnv struct {
	workDir, objects, manifest string
	c                          *Capturer
}

// newSnapEnv mirrors production: a git worktree with the data dir (object
// store + journal) nested inside it and excluded from capture.
func newSnapEnv(t *testing.T, excludeData bool) *snapEnv {
	t.Helper()
	requireGit(t)
	root := t.TempDir()
	workDir := filepath.Join(root, "repo")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	gitInit(t, workDir)
	dataDir := filepath.Join(workDir, ".data")
	objects := filepath.Join(dataDir, "objects")
	manifest := filepath.Join(dataDir, "session", "snapshots.jsonl")
	if err := os.MkdirAll(filepath.Dir(manifest), 0o755); err != nil {
		t.Fatal(err)
	}
	exclude := ""
	if excludeData {
		exclude = dataDir
	}
	return &snapEnv{
		workDir:  workDir,
		objects:  objects,
		manifest: manifest,
		c:        New(workDir, objects, manifest, exclude),
	}
}

func TestPath(t *testing.T) {
	if got, want := Path("/x/sess"), filepath.Join("/x/sess", "snapshots.jsonl"); got != want {
		t.Errorf("Path = %q, want %q", got, want)
	}
}

func TestCaptureBeforeAfterAndReadStep(t *testing.T) {
	e := newSnapEnv(t, true)
	mustWrite(t, filepath.Join(e.workDir, "a.txt"), "v1\n")
	mustWrite(t, filepath.Join(e.workDir, "sub", "deep.txt"), "deep\n")
	mustWrite(t, filepath.Join(e.workDir, ".gitignore"), "ign.txt\n")
	mustWrite(t, filepath.Join(e.workDir, "ign.txt"), "secret\n")
	mustWrite(t, filepath.Join(e.workDir, "real.txt"), "real\n")
	if err := os.WriteFile(filepath.Join(e.workDir, "big.bin"),
		bytes.Repeat([]byte("x"), (2<<20)+1), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real.txt", filepath.Join(e.workDir, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	e.c.Step("s1")
	mustWrite(t, filepath.Join(e.workDir, "a.txt"), "v2\n")
	if err := os.Remove(filepath.Join(e.workDir, "sub", "deep.txt")); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(e.workDir, "made.txt"), "made\n")
	e.c.Finish("s1")

	entries := Load(e.manifest)
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	ent := entries[0]
	if ent.ID != "s1" || ent.WD != e.workDir {
		t.Fatalf("entry = %+v, want ID s1 WD %s", ent, e.workDir)
	}

	if got, want := ent.Before["a.txt"], blobHash([]byte("v1\n")); got != want {
		t.Fatalf("before a.txt = %s, want git blob hash %s", got, want)
	}
	if _, ok := ent.Before["sub/deep.txt"]; !ok {
		t.Error("before must list sub/deep.txt")
	}
	if _, ok := ent.Before[".gitignore"]; !ok {
		t.Error("before must list .gitignore")
	}
	for _, absent := range []string{"made.txt", "ign.txt", "big.bin", "link"} {
		if _, ok := ent.Before[absent]; ok {
			t.Errorf("before must not contain %s", absent)
		}
	}
	for p := range ent.Before {
		if strings.Contains(p, ".data/") {
			t.Errorf("data dir leaked into state: %s", p)
		}
	}
	if got, want := ent.After["a.txt"], blobHash([]byte("v2\n")); got != want {
		t.Errorf("after a.txt = %s, want %s", got, want)
	}
	if got, want := ent.After["made.txt"], blobHash([]byte("made\n")); got != want {
		t.Errorf("after made.txt = %s, want %s", got, want)
	}
	if _, ok := ent.After["sub/deep.txt"]; ok {
		t.Error("after must not contain deleted sub/deep.txt")
	}

	// The blob lives in the shared object store at <objects>/xx/yyyy...
	obj := filepath.Join(e.objects, ent.Before["a.txt"][:2], ent.Before["a.txt"][2:])
	if _, err := os.Stat(obj); err != nil {
		t.Fatalf("loose object missing: %v", err)
	}

	before, after := ReadStep(e.objects, &ent)
	if string(before["a.txt"]) != "v1\n" || string(after["a.txt"]) != "v2\n" {
		t.Errorf("ReadStep a.txt = %q -> %q, want v1\\n -> v2\\n", before["a.txt"], after["a.txt"])
	}
	if string(after["made.txt"]) != "made\n" {
		t.Errorf("ReadStep after made.txt = %q", after["made.txt"])
	}
	if _, ok := before["made.txt"]; ok {
		t.Error("before side must not carry made.txt")
	}

	// A no-op step journals a second entry whose sides match.
	e.c.Step("s2")
	e.c.Finish("s2")
	all := Load(e.manifest)
	if len(all) != 2 {
		t.Fatalf("entries = %d, want 2", len(all))
	}
	s2 := Find(all, "s2")
	if s2 == nil {
		t.Fatal("Find(s2) = nil")
	}
	if !maps.Equal(s2.Before, s2.After) {
		t.Errorf("unchanged step: before %v != after %v", s2.Before, s2.After)
	}
	if Find(all, "nope") != nil {
		t.Error("Find of unknown id must be nil")
	}
}

func TestRestoreRoundTrip(t *testing.T) {
	e := newSnapEnv(t, true)
	mustWrite(t, filepath.Join(e.workDir, "a.txt"), "v1\n")
	mustWrite(t, filepath.Join(e.workDir, "sub", "deep.txt"), "deep\n")

	e.c.Step("s1")
	mustWrite(t, filepath.Join(e.workDir, "a.txt"), "v2\n")
	mustWrite(t, filepath.Join(e.workDir, "made.txt"), "made\n")
	if err := os.Remove(filepath.Join(e.workDir, "sub", "deep.txt")); err != nil {
		t.Fatal(err)
	}
	e.c.Finish("s1")

	entries := Load(e.manifest)
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	ent := entries[0]

	// Drift further, then roll back to the before state: rewritten file,
	// step-created file removed, step-deleted file resurrected = 3 changes.
	mustWrite(t, filepath.Join(e.workDir, "a.txt"), "junk\n")
	changed, err := e.c.Restore(ent.Before)
	if err != nil {
		t.Fatal(err)
	}
	if changed != 3 {
		t.Errorf("changed = %d, want 3", changed)
	}
	assertContent(t, filepath.Join(e.workDir, "a.txt"), "v1\n")
	assertContent(t, filepath.Join(e.workDir, "sub", "deep.txt"), "deep\n")
	if _, err := os.Stat(filepath.Join(e.workDir, "made.txt")); !os.IsNotExist(err) {
		t.Errorf("made.txt must be removed, stat err = %v", err)
	}

	// Forward to the after state again.
	changed, err = e.c.Restore(ent.After)
	if err != nil {
		t.Fatal(err)
	}
	if changed != 3 {
		t.Errorf("forward changed = %d, want 3", changed)
	}
	assertContent(t, filepath.Join(e.workDir, "a.txt"), "v2\n")
	assertContent(t, filepath.Join(e.workDir, "made.txt"), "made\n")
	if _, err := os.Stat(filepath.Join(e.workDir, "sub", "deep.txt")); !os.IsNotExist(err) {
		t.Errorf("sub/deep.txt must be removed again, stat err = %v", err)
	}

	// Restoring the current state is a no-op.
	changed, err = e.c.Restore(ent.After)
	if err != nil {
		t.Fatal(err)
	}
	if changed != 0 {
		t.Errorf("idempotent changed = %d, want 0", changed)
	}
}

func TestCaptureNoopsOutsideRepo(t *testing.T) {
	requireGit(t)
	workDir := t.TempDir() // never git-init'd
	objects := filepath.Join(t.TempDir(), "objects")
	manifest := filepath.Join(t.TempDir(), "session", "snapshots.jsonl")
	c := New(workDir, objects, manifest, "")

	c.Step("s1")
	c.Finish("s1")
	if _, err := os.Stat(manifest); !os.IsNotExist(err) {
		t.Fatalf("journal must stay unwritten outside a repo, stat err = %v", err)
	}

	changed, err := c.Restore(map[string]string{"a.txt": blobHash([]byte("x"))})
	if err == nil || !strings.Contains(err.Error(), "transcript-only rollback") {
		t.Fatalf("err = %v, want transcript-only rollback", err)
	}
	if changed != 0 {
		t.Errorf("changed = %d, want 0", changed)
	}
}

func TestRestoreNeverTouchesStoreOrJournal(t *testing.T) {
	// exclude="" on purpose: object store and journal sit inside the
	// worktree and must survive a restore that "loses" them.
	e := newSnapEnv(t, false)
	mustWrite(t, filepath.Join(e.workDir, "a.txt"), "hello\n")

	e.c.Step("s1")
	e.c.Finish("s1")
	entries := Load(e.manifest)
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	ent := entries[0]
	obj := filepath.Join(e.objects, ent.Before["a.txt"][:2], ent.Before["a.txt"][2:])

	changed, err := e.c.Restore(ent.Before)
	if err != nil {
		t.Fatal(err)
	}
	if changed != 0 {
		t.Errorf("changed = %d, want 0 (state already current, guards hold)", changed)
	}
	if _, err := os.Stat(obj); err != nil {
		t.Errorf("object store touched by Restore: %v", err)
	}
	if got := Load(e.manifest); len(got) != 1 {
		t.Errorf("journal touched by Restore: %d entries, want 1", len(got))
	}
}

func TestLoadMalformedLinesSkipped(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "nope.jsonl")
	if got := Load(missing); got != nil {
		t.Errorf("Load(missing) = %v, want nil", got)
	}

	path := Path(dir)
	raw := "not json\n" +
		"\n" +
		`{"id":"","wd":"/w"}` + "\n" +
		`{"id":"good","wd":"/w","before":{"a":"h1"},"after":{"a":"h2","b":"h3"}}` + "\n"
	mustWrite(t, path, raw)

	got := Load(path)
	if len(got) != 1 || got[0].ID != "good" {
		t.Fatalf("Load = %+v, want one entry good", got)
	}
	if got[0].Before["a"] != "h1" || got[0].After["b"] != "h3" {
		t.Errorf("states not parsed: %+v", got[0])
	}
	if Find(got, "missing") != nil {
		t.Error("Find of unknown id must be nil")
	}
}

func TestReadStepSkipsUnreadableObjects(t *testing.T) {
	b, a := ReadStep(t.TempDir(), nil)
	if b != nil || a != nil {
		t.Errorf("ReadStep(nil) = %v, %v, want nil, nil", b, a)
	}

	objects := filepath.Join(t.TempDir(), "objects")
	sum := "aabbccddeeff00112233445566778899aabbccdd"

	// Valid zlib stream, corrupt payload.
	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf)
	_, _ = zw.Write([]byte("not a blob"))
	_ = zw.Close()
	writeObjectFile(t, objects, sum, buf.Bytes())
	// Not zlib at all.
	writeObjectFile(t, objects, "11223456789abcdef0123456789abcdef012345", []byte("plain text"))

	e := &Entry{ID: "x",
		Before: map[string]string{"a": sum, "b": "11223456789abcdef0123456789abcdef012345", "c": "ab"},
		After:  map[string]string{"a": sum, "gone": "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"},
	}
	before, after := ReadStep(objects, e)
	if len(before) != 0 {
		t.Errorf("before = %v, want everything skipped (corrupt/short hashes)", before)
	}
	if len(after) != 0 {
		t.Errorf("after = %v, want corrupt + missing blobs skipped", after)
	}
}

func writeObjectFile(t *testing.T, objects, sum string, data []byte) {
	t.Helper()
	path := filepath.Join(objects, sum[:2], sum[2:])
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertContent(t *testing.T, path, want string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(b) != want {
		t.Errorf("%s = %q, want %q", path, b, want)
	}
}
