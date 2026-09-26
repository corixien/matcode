package references

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// requireGit pins git config to /dev/null so user-level excludes/rewrites
// can never influence clones or fetches under test.
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	if os.Getenv("GIT_CONFIG_GLOBAL") != os.DevNull {
		t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
		t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	}
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out))
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

// initRepo creates a git repo at dir with one committed README ("one\n").
func initRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "init", "-q")
	mustWrite(t, filepath.Join(dir, "README.md"), "one\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "-c", "user.email=t@t", "-c", "user.name=matcode-test",
		"commit", "-q", "-m", "one")
}

func commitAll(t *testing.T, dir, msg string) {
	t.Helper()
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "-c", "user.email=t@t", "-c", "user.name=matcode-test",
		"commit", "-q", "-m", msg)
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(b) != want {
		t.Errorf("%s = %q, want %q", path, b, want)
	}
}

// waitFor polls until cond holds; the async Sync API forces bounded polling.
func waitFor(t *testing.T, what string, limit time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// assertStays fails if cond ever becomes true within the window.
func assertStays(t *testing.T, what string, window time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(window)
	for time.Now().Before(deadline) {
		if cond() {
			t.Fatalf("%s: condition became true", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestParseLocalShorthandAndTable(t *testing.T) {
	base := t.TempDir()
	data := t.TempDir()
	raw := map[string]any{
		"docs":  "./docs",
		"abs":   "/srv/data",
		"table": map[string]any{"path": "rel/dir", "description": "Team notes", "hidden": true, "branch": "kept"},
	}
	home, homeErr := os.UserHomeDir()
	if homeErr == nil {
		raw["home"] = "~/proj"
		raw["root"] = "~"
	}

	refs, err := Parse(base, data, raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != len(raw) {
		t.Fatalf("refs = %d, want %d", len(refs), len(raw))
	}

	docs := refs["docs"]
	if docs.Alias != "docs" || docs.Path != "./docs" || docs.Repository != "" {
		t.Errorf("docs = %+v, want alias/path echo, no repository", docs)
	}
	if want := filepath.Join(base, "docs"); docs.Dir != want {
		t.Errorf("docs.Dir = %q, want %q", docs.Dir, want)
	}
	if want := filepath.Clean("/srv/data"); refs["abs"].Dir != want {
		t.Errorf("abs.Dir = %q, want %q", refs["abs"].Dir, want)
	}

	tab := refs["table"]
	if tab.Dir != filepath.Join(base, "rel", "dir") || tab.Description != "Team notes" ||
		!tab.Hidden || tab.Path != "rel/dir" || tab.Branch != "kept" {
		t.Errorf("table = %+v, want parsed table fields", tab)
	}

	if homeErr == nil {
		if want := filepath.Join(home, "proj"); refs["home"].Dir != want {
			t.Errorf("home.Dir = %q, want %q", refs["home"].Dir, want)
		}
		if want := filepath.Clean(home); refs["root"].Dir != want {
			t.Errorf("root.Dir = %q, want %q", refs["root"].Dir, want)
		}
	}
}

func TestParseErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  map[string]any
		want string
	}{
		{"empty alias", map[string]any{"": "./x"}, `invalid reference alias ""`},
		{"space in alias", map[string]any{"my alias": "./x"}, `invalid reference alias "my alias"`},
		{"comma in alias", map[string]any{"a,b": "./x"}, "invalid reference alias"},
		{"slash in alias", map[string]any{"a/b": "./x"}, "invalid reference alias"},
		{"unknown key", map[string]any{"x": map[string]any{"path": "p", "foo": "y"}}, `references.x: unknown key "foo"`},
		{"both forms", map[string]any{"x": map[string]any{"path": "p", "repository": "o/r"}}, "path and repository are mutually exclusive"},
		{"neither form", map[string]any{"x": map[string]any{"description": "d"}}, "references.x: missing path or repository"},
		{"scalar wrong type", map[string]any{"x": 42}, "want a path string or a table"},
		{"nil value", map[string]any{"x": nil}, "want a path string or a table"},
		{"path not string", map[string]any{"x": map[string]any{"path": 5}}, "path wants a string"},
		{"repository not string", map[string]any{"x": map[string]any{"repository": 5}}, "repository wants a string"},
		{"hidden not bool", map[string]any{"x": map[string]any{"path": "p", "hidden": "yes"}}, "hidden wants a bool"},
		{"plain name reads as repo", map[string]any{"x": "justname"}, "bad repository"},
		{"file url rejected", map[string]any{"x": "file:///tmp/x"}, "local file: repositories are not supported"},
		{"url without host", map[string]any{"x": "https:///nohost"}, "bad repository"},
		{"scp empty path", map[string]any{"x": "host:"}, "bad repository"},
		{"invalid branch", map[string]any{"x": map[string]any{"repository": "o/r", "branch": "bad branch"}}, "invalid branch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			refs, err := Parse(t.TempDir(), t.TempDir(), tc.raw)
			if err == nil {
				t.Fatal("err = nil, want failure")
			}
			if refs != nil {
				t.Errorf("refs = %v, want nil map on error", refs)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestParseRepositoryNormalizes(t *testing.T) {
	data := t.TempDir()
	join := func(parts ...string) string {
		return filepath.Join(append([]string{data, "repos"}, parts...)...)
	}
	for _, tc := range []struct {
		name       string
		raw        map[string]any
		wantURL    string
		wantDir    string
		wantBranch string
	}{
		{"github shorthand", map[string]any{"r": "owner/repo"},
			"https://github.com/owner/repo", join("github.com", "owner", "repo"), ""},
		{"github shorthand keeps .git in URL", map[string]any{"r": "owner/repo.git"},
			"https://github.com/owner/repo.git", join("github.com", "owner", "repo"), ""},
		{"host/path form", map[string]any{"r": "gitlab.com/team/tool"},
			"https://gitlab.com/team/tool", join("gitlab.com", "team", "tool"), ""},
		{"https URL", map[string]any{"r": "https://example.com/team/tool.git"},
			"https://example.com/team/tool.git", join("example.com", "team", "tool"), ""},
		{"scp URL", map[string]any{"r": "git@github.com:team/tool.git"},
			"git@github.com:team/tool.git", join("github.com", "team", "tool"), ""},
		{"branch checkout suffix", map[string]any{"r": map[string]any{"repository": "owner/repo", "branch": "release"}},
			"https://github.com/owner/repo", join("github.com", "owner", "repo") + "@release", "release"},
		{"branch with slash is escaped", map[string]any{"r": map[string]any{"repository": "owner/repo", "branch": "feat/x"}},
			"https://github.com/owner/repo", join("github.com", "owner", "repo") + "@feat%2Fx", "feat/x"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			refs, err := Parse(t.TempDir(), data, tc.raw)
			if err != nil {
				t.Fatal(err)
			}
			r := refs["r"]
			if r.Repository != tc.wantURL {
				t.Errorf("Repository = %q, want %q", r.Repository, tc.wantURL)
			}
			if r.Dir != tc.wantDir {
				t.Errorf("Dir = %q, want %q", r.Dir, tc.wantDir)
			}
			if r.Branch != tc.wantBranch {
				t.Errorf("Branch = %q, want %q", r.Branch, tc.wantBranch)
			}
			if r.Path != "" {
				t.Errorf("Path = %q, want empty for git refs", r.Path)
			}
		})
	}
}

func TestNamesAndBlock(t *testing.T) {
	refs := map[string]Ref{
		"beta":  {Alias: "beta", Dir: "/d/beta", Description: "Beta docs"},
		"alpha": {Alias: "alpha", Dir: "/d/alpha", Description: "Alpha docs"},
		"plain": {Alias: "plain", Dir: "/d/plain"},
		"ghost": {Alias: "ghost", Dir: "/d/ghost", Description: "Hidden one", Hidden: true},
	}

	if got, want := Names(refs, false), []string{"alpha", "beta", "plain"}; !slices.Equal(got, want) {
		t.Errorf("Names(all) = %v, want %v", got, want)
	}
	if got, want := Names(refs, true), []string{"alpha", "beta"}; !slices.Equal(got, want) {
		t.Errorf("Names(described) = %v, want %v", got, want)
	}

	want := "\n\nReferences (attach with ref:<alias>):" +
		"\n- alpha -> /d/alpha: Alpha docs" +
		"\n- beta -> /d/beta: Beta docs"
	if got := Block(refs); got != want {
		t.Errorf("Block = %q, want %q", got, want)
	}

	if got := Block(map[string]Ref{"plain": refs["plain"]}); got != "" {
		t.Errorf("Block without descriptions = %q, want empty", got)
	}
	if got := Block(nil); got != "" {
		t.Errorf("Block(nil) = %q, want empty", got)
	}
}

func TestSyncLocalRefsNoop(t *testing.T) {
	data := t.TempDir()
	refs, err := Parse(t.TempDir(), data, map[string]any{"docs": "./docs"})
	if err != nil {
		t.Fatal(err)
	}

	Sync(data, refs) // local-only: returns without spawning anything
	if _, err := os.Stat(filepath.Join(data, "repos")); !os.IsNotExist(err) {
		t.Errorf("repos tree created for local-only refs, stat err = %v", err)
	}
}

func TestRefreshClonesResetsAndBranches(t *testing.T) {
	requireGit(t)
	root := t.TempDir()
	src := filepath.Join(root, "src")
	initRepo(t, src)

	dataDir := filepath.Join(root, "data")
	r := Ref{Alias: "origin", Repository: src, Dir: filepath.Join(dataDir, "repos", "local", "tool")}

	// Missing checkout: cloned (parents created on the way).
	if err := refresh(r); err != nil {
		t.Fatal(err)
	}
	assertFileContent(t, filepath.Join(r.Dir, "README.md"), "one\n")

	// Stale checkout: fetched and hard-reset to the remote default branch.
	mustWrite(t, filepath.Join(src, "README.md"), "two\n")
	commitAll(t, src, "two")
	if err := refresh(r); err != nil {
		t.Fatal(err)
	}
	assertFileContent(t, filepath.Join(r.Dir, "README.md"), "two\n")

	// Branch checkout: cloned with -b, then reset to origin/<branch>.
	gitRun(t, src, "checkout", "-q", "-b", "release")
	mustWrite(t, filepath.Join(src, "rel.txt"), "r1\n")
	commitAll(t, src, "release one")
	rel := Ref{Alias: "release", Repository: src, Branch: "release",
		Dir: filepath.Join(dataDir, "repos", "local", "tool@release")}
	if err := refresh(rel); err != nil {
		t.Fatal(err)
	}
	assertFileContent(t, filepath.Join(rel.Dir, "rel.txt"), "r1\n")
	if got := gitOut(t, rel.Dir, "rev-parse", "--abbrev-ref", "HEAD"); got != "release" {
		t.Errorf("HEAD = %q, want release", got)
	}

	mustWrite(t, filepath.Join(src, "rel.txt"), "r2\n")
	commitAll(t, src, "release two")
	if err := refresh(rel); err != nil {
		t.Fatal(err)
	}
	assertFileContent(t, filepath.Join(rel.Dir, "rel.txt"), "r2\n")
}

func TestSyncClonesIntoDataDirAndSkipsFresh(t *testing.T) {
	requireGit(t)
	root := t.TempDir()
	src := filepath.Join(root, "src")
	initRepo(t, src)

	dataDir := filepath.Join(root, "data")
	refs, err := Parse(filepath.Join(root, "config"), dataDir,
		map[string]any{"origin": "owner/repo"})
	if err != nil {
		t.Fatal(err)
	}
	r := refs["origin"]
	if want := filepath.Join(dataDir, "repos", "github.com", "owner", "repo"); r.Dir != want {
		t.Fatalf("Dir = %q, want %q", r.Dir, want)
	}
	// Local stand-in for the normalized https remote; Sync never re-parses it.
	r.Repository = src
	gitRefs := map[string]Ref{"origin": r}

	start := time.Now().Unix()
	Sync(dataDir, gitRefs)
	waitFor(t, "clone into the data dir", 10*time.Second, func() bool {
		b, err := os.ReadFile(filepath.Join(r.Dir, "README.md"))
		return err == nil && string(b) == "one\n"
	})

	// The attempt is journaled before the clone runs.
	b, err := os.ReadFile(filepath.Join(dataDir, "repos", "refresh.json"))
	if err != nil {
		t.Fatalf("refresh.json: %v", err)
	}
	attempts := map[string]int64{}
	if err := json.Unmarshal(b, &attempts); err != nil {
		t.Fatalf("refresh.json: %v", err)
	}
	if attempts[r.Dir] < start {
		t.Errorf("attempt for %s = %d, want >= %d", r.Dir, attempts[r.Dir], start)
	}

	// Remote advances; the checkout is fresh (<24h), so Sync must not touch it.
	mustWrite(t, filepath.Join(src, "README.md"), "two\n")
	commitAll(t, src, "two")
	Sync(dataDir, gitRefs)
	assertStays(t, "fresh checkout refreshed inside the 24h window", 300*time.Millisecond, func() bool {
		b, err := os.ReadFile(filepath.Join(r.Dir, "README.md"))
		return err == nil && string(b) == "two\n"
	})
}

func TestSyncStaleRefRefreshesAndLogsFailure(t *testing.T) {
	requireGit(t)
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	dest := filepath.Join(dataDir, "repos", "broken.example", "team", "tool")

	// Existing checkout whose origin is a missing local path: the fetch
	// fails fast, offline, and lands in refresh.log.
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dest, "init", "-q")
	gitRun(t, dest, "remote", "add", "origin", filepath.Join(root, "nonexistent-remote.git"))

	refreshJSON := filepath.Join(dataDir, "repos", "refresh.json")
	if err := os.MkdirAll(filepath.Dir(refreshJSON), 0o755); err != nil {
		t.Fatal(err)
	}
	stale := time.Now().Add(-25 * time.Hour).Unix()
	if err := os.WriteFile(refreshJSON, []byte(fmt.Sprintf("{%q: %d}\n", dest, stale)), 0o644); err != nil {
		t.Fatal(err)
	}

	Sync(dataDir, map[string]Ref{"broken": {
		Alias:      "broken",
		Repository: "https://broken.example/team/tool",
		Dir:        dest,
	}})
	logPath := filepath.Join(dataDir, "repos", "refresh.log")
	waitFor(t, "stale ref refresh attempt", 10*time.Second, func() bool {
		b, err := os.ReadFile(logPath)
		return err == nil && strings.Contains(string(b), "reference broken")
	})

	// Failure still records the attempt: the next retry waits another 24h.
	b, err := os.ReadFile(refreshJSON)
	if err != nil {
		t.Fatalf("refresh.json: %v", err)
	}
	attempts := map[string]int64{}
	if err := json.Unmarshal(b, &attempts); err != nil {
		t.Fatalf("refresh.json: %v", err)
	}
	if attempts[dest] <= stale {
		t.Errorf("attempt = %d, want refreshed to now (was %d)", attempts[dest], stale)
	}
}
