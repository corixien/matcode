package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"matcode/internal/engine"
	"matcode/internal/skills"
)

func skillSet(t *testing.T) *skills.Set {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "git-release")
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	md := "---\nname: Git Release\ndescription: Prepare release notes\n---\n\n## Workflow\n1. Draft notes\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(md), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scripts", "changelog.sh"), []byte("echo"), 0o644); err != nil {
		t.Fatal(err)
	}
	set, err := skills.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	return set
}

func TestSkillAdvertisesAndLoads(t *testing.T) {
	set := skillSet(t)
	tool := Skill(set)

	if !strings.Contains(tool.Description, "<available_skills>") ||
		!strings.Contains(tool.Description, "<id>git-release</id>") ||
		!strings.Contains(tool.Description, "Prepare release notes") {
		t.Fatalf("description must advertise skills: %q", tool.Description)
	}
	if strings.Contains(tool.Description, "## Workflow") {
		t.Fatal("the description must never carry a body")
	}

	res, err := tool.Execute(context.Background(), json.RawMessage(`{"id":"git-release"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "## Workflow") {
		t.Fatalf("body missing: %q", res.Text)
	}
	if strings.Contains(res.Text, "name: Git Release") {
		t.Fatalf("frontmatter leaked: %q", res.Text)
	}
	if !strings.Contains(res.Text, "\nBase directory: /") {
		t.Fatalf("base dir line missing: %q", res.Text)
	}
	if !strings.Contains(res.Text, filepath.Join("scripts", "changelog.sh")) {
		t.Fatalf("supporting files missing: %q", res.Text)
	}
	if len(set.LoadedIDs()) != 1 || set.LoadedIDs()[0] != "git-release" {
		t.Fatalf("loaded = %v", set.LoadedIDs())
	}
}

func TestSkillUnknownID(t *testing.T) {
	tool := Skill(skillSet(t))
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"id":"nope"}`))
	if err == nil || !strings.Contains(err.Error(), "unknown skill") ||
		!strings.Contains(err.Error(), "git-release") {
		t.Fatalf("err = %v, want unknown skill listing the available ids", err)
	}
}

func TestBuiltinSkipsSkillWithoutDefinitions(t *testing.T) {
	wd := t.TempDir()
	for _, ids := range [][]string{
		toIDs(Builtin(wd, wd, false, nil)),
		toIDs(Builtin(wd, wd, false, &skills.Set{})),
	} {
		if contains(ids, "skill") {
			t.Fatal("a dead skill tool must not be registered")
		}
	}
	if !contains(toIDs(Builtin(wd, wd, false, skillSet(t))), "skill") {
		t.Fatal("skill tool missing when skills exist")
	}
}

func toIDs(list []engine.Tool) []string {
	out := make([]string, 0, len(list))
	for _, t := range list {
		out = append(out, t.ID)
	}
	return out
}

func contains(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}
