package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"matcode/internal/config"
	"matcode/internal/permissions"
)

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

// TestSkillsForHidesDenied pins the rule that makes skill permissions real:
// a deny rule removes the skill from the registry the model is offered.
func TestSkillsForHidesDenied(t *testing.T) {
	global, project := t.TempDir(), t.TempDir()
	put(t, global, "skills/git-release/SKILL.md", "---\ndescription: public\n---\nbody")
	put(t, global, "skills/internal-docs/SKILL.md", "---\ndescription: private\n---\nbody")
	put(t, project, "skills/project-only/SKILL.md", "---\ndescription: local\n---\nbody")

	cfg := &config.Config{GlobalDir: global, ProjectDir: project}
	perms := &permissions.Set{Default: permissions.Allow, Rules: []permissions.Rule{
		{Actions: []string{"skill"}, Pattern: "internal-*", Decision: permissions.Deny},
	}}

	set, err := skillsFor(cfg, perms)
	if err != nil {
		t.Fatal(err)
	}
	ids := strings.Join(set.IDs(), ",")
	if want := "git-release,project-only"; ids != want {
		t.Fatalf("ids = %q, want %q (denied hidden, both roots merged)", ids, want)
	}
	if block := set.Advertise(); strings.Contains(block, "internal-docs") {
		t.Fatalf("denied skill still advertised: %q", block)
	}

	// No permissions (build agent bypasses): nothing is hidden.
	set, err = skillsFor(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(set.IDs()); got != 3 {
		t.Fatalf("bypass keeps all skills, got %d", got)
	}
}
