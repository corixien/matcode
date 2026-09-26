package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// put creates a file under root and returns its path.
func put(t *testing.T, root, rel, text string) string {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadFolderAndFlat(t *testing.T) {
	root := t.TempDir()
	put(t, root, "release/SKILL.md", "---\nname: Git Release\ndescription: Prepare release notes\n---\n\n## Workflow\n1. Draft notes\n")
	put(t, root, "review.md", "---\ndescription: Review a change\n---\nReview it well.")
	put(t, root, "teams/api/SKILL.md", "---\ndescription: API conventions\n---\nUse /v2 paths.")

	set, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(set.IDs(), ",")
	if want := "api,release,review"; got != want {
		t.Fatalf("ids = %q, want %q", got, want)
	}

	sk, ok := set.Get("release")
	if !ok {
		t.Fatal("release not found")
	}
	if sk.Name != "Git Release" || sk.Description != "Prepare release notes" {
		t.Fatalf("frontmatter = %+v", sk)
	}
	if strings.Contains(sk.Body, "name: Git Release") || !strings.Contains(sk.Body, "## Workflow") {
		t.Fatalf("body must drop the frontmatter: %q", sk.Body)
	}
	if !filepath.IsAbs(sk.Dir) || filepath.Base(sk.Dir) != "release" {
		t.Fatalf("dir = %q, want the skill folder", sk.Dir)
	}

	// Nested folder: the containing folder names the ID, not the file.
	if _, ok := set.Get("api"); !ok {
		t.Fatal("nested skills/api/SKILL.md should yield id api")
	}

	// Flat file: the file stem names the ID and the source root is the dir.
	rv, _ := set.Get("review")
	if rv.Dir != root {
		t.Fatalf("flat skill dir = %q, want source root %q", rv.Dir, root)
	}
}

func TestLoadWithoutFrontmatterStillLoads(t *testing.T) {
	root := t.TempDir()
	put(t, root, "plain/SKILL.md", "Just instructions.\n")
	set, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	sk, ok := set.Get("plain")
	if !ok {
		t.Fatal("plain not found")
	}
	if sk.Name != "plain" || sk.Description != "" {
		t.Fatalf("defaults = %+v", sk)
	}
	if sk.Advertised {
		t.Fatal("a skill without a description must not be advertised")
	}
	if set.Advertise() != "" {
		t.Fatalf("advertise = %q, want empty", set.Advertise())
	}
}

func TestAdvertiseHidesAutoinvokeFalse(t *testing.T) {
	root := t.TempDir()
	put(t, root, "shown/SKILL.md", "---\ndescription: Visible skill\n---\nbody")
	put(t, root, "quiet/SKILL.md", "---\ndescription: Hidden skill\nmetadata:\n  opencode/autoinvoke: false\n---\nbody")
	set, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	block := set.Advertise()
	if !strings.Contains(block, "<available_skills>") || !strings.Contains(block, "</available_skills>") {
		t.Fatalf("block wrapper missing: %q", block)
	}
	if !strings.Contains(block, "<id>shown</id>") {
		t.Fatalf("shown missing: %q", block)
	}
	if strings.Contains(block, "quiet") {
		t.Fatalf("autoinvoke=false leaked: %q", block)
	}
	if !strings.Contains(block, "Visible skill") {
		t.Fatalf("description missing: %q", block)
	}
	if strings.Contains(block, "body") {
		t.Fatalf("the block must never carry the body: %q", block)
	}
}

func TestProjectOverridesGlobal(t *testing.T) {
	global, project := t.TempDir(), t.TempDir()
	put(t, global, "shared/SKILL.md", "---\ndescription: global\n---\nglobal body")
	put(t, project, "shared/SKILL.md", "---\ndescription: project\n---\nproject body")

	set, err := Load(global, project)
	if err != nil {
		t.Fatal(err)
	}
	sk, _ := set.Get("shared")
	if sk.Body != "project body" || sk.Description != "project" {
		t.Fatalf("later source must win: %+v", sk)
	}
	// A global-only skill survives alongside the override.
	put(t, global, "only-global/SKILL.md", "---\ndescription: g\n---\ng")
	set, err = Load(global, project)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := set.Get("only-global"); !ok {
		t.Fatal("global-only skill lost")
	}
}

func TestSupportingFilesCapped(t *testing.T) {
	root := t.TempDir()
	put(t, root, "s/SKILL.md", "---\ndescription: d\n---\nbody")
	for i := 0; i < 15; i++ {
		put(t, root, "s/ref/"+fmt.Sprintf("%02d", i)+".md", "x")
	}
	put(t, root, "s/scripts/run.sh", "echo hi")
	set, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	sk, _ := set.Get("s")
	if len(sk.Files) != MaxFiles {
		t.Fatalf("files = %d, want cap %d: %v", len(sk.Files), MaxFiles, sk.Files)
	}
	// Sorted and capped: the first ten alphabetically, SKILL.md never listed.
	want := []string{"ref/00.md", "ref/01.md", "ref/02.md", "ref/03.md", "ref/04.md",
		"ref/05.md", "ref/06.md", "ref/07.md", "ref/08.md", "ref/09.md"}
	if strings.Join(sk.Files, ",") != strings.Join(want, ",") {
		t.Fatalf("files = %v, want %v", sk.Files, want)
	}
	for _, f := range sk.Files {
		if filepath.IsAbs(f) || f == "SKILL.md" {
			t.Fatalf("bad supporting path %q", f)
		}
	}
}

func TestRootSKILLmdSkippedAndMissingRootsOK(t *testing.T) {
	root := t.TempDir()
	put(t, root, "SKILL.md", "---\ndescription: no id\n---\nbody")
	put(t, root, "real/SKILL.md", "---\ndescription: d\n---\nbody")

	set, err := Load(filepath.Join(root, "nope"), root)
	if err != nil {
		t.Fatalf("missing roots must not fail: %v", err)
	}
	if len(set.IDs()) != 1 || set.IDs()[0] != "real" {
		t.Fatalf("ids = %v", set.IDs())
	}
}

func TestGuidanceAndWithout(t *testing.T) {
	root := t.TempDir()
	put(t, root, "a/SKILL.md", "---\ndescription: First skill\n---\nbody a")
	put(t, root, "b/SKILL.md", "---\ndescription: Second skill\n---\nbody b")
	set, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if set.Guidance() != "" {
		t.Fatal("guidance must be empty before any load")
	}
	set.MarkLoaded("a")
	set.MarkLoaded("a") // idempotent
	g := set.Guidance()
	if !strings.Contains(g, "## Loaded skills") || !strings.Contains(g, "a (a): First skill") {
		t.Fatalf("guidance = %q", g)
	}
	if len(set.LoadedIDs()) != 1 {
		t.Fatalf("loaded = %v", set.LoadedIDs())
	}

	filtered := set.Without(func(id string) bool { return id == "a" })
	if _, ok := filtered.Get("a"); ok {
		t.Fatal("Without kept a filtered skill")
	}
	if _, ok := filtered.Get("b"); !ok {
		t.Fatal("Without dropped a kept skill")
	}
	if len(filtered.LoadedIDs()) != 0 {
		t.Fatalf("loaded marks must drop with the skill: %v", filtered.LoadedIDs())
	}
	if len(set.LoadedIDs()) != 1 {
		t.Fatal("Without must not mutate the original")
	}
}

func TestCollapsesAndEscapesDescription(t *testing.T) {
	root := t.TempDir()
	put(t, root, "x/SKILL.md", "---\ndescription: \"a    b <tag>\"\n---\nbody")
	set, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	block := set.Advertise()
	if !strings.Contains(block, "a b ") {
		t.Fatalf("runs of spaces must collapse: %q", block)
	}
	if strings.Contains(block, "<tag>") {
		t.Fatalf("markup must be escaped: %q", block)
	}
	if !strings.Contains(block, "&lt;tag&gt;") {
		t.Fatalf("escaped markup missing: %q", block)
	}
}
