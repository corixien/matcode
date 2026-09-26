package config

import (
	"os"
	"path/filepath"
	"testing"
)

// loadWithHome points $HOME at a temp dir so Load resolves a global tree
// we fully control.
func loadWithHome(t *testing.T, home, cwd string) (*Config, error) {
	t.Helper()
	t.Setenv("HOME", home)
	return Load(cwd)
}

func writeCfg(t *testing.T, dir, text string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestDefaultAgentFallsBack proves `default_agent` works only as a
// substitute for `agent` (spec row 32), and that `agent` always wins.
func TestDefaultAgentFallsBack(t *testing.T) {
	cases := []struct {
		name, global, project, want string
	}{
		{"agent only", `agent = "plan"`, ``, "plan"},
		{"default_agent only", `default_agent = "explore"`, ``, "explore"},
		{"agent beats default_agent in one file", `agent = "plan"` + "\n" + `default_agent = "explore"`, ``, "plan"},
		{"project agent beats global default_agent", `default_agent = "explore"`, `agent = "plan"`, "plan"},
		{"project default_agent beats nothing", `agent = "plan"`, `default_agent = "explore"`, "plan"},
		{"neither", `model = "a/b"`, ``, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			home, cwd := t.TempDir(), t.TempDir()
			writeCfg(t, filepath.Join(home, ".config", "mtc"), c.global)
			writeCfg(t, filepath.Join(cwd, ".mtc"), c.project)
			cfg, err := loadWithHome(t, home, cwd)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Agent != c.want {
				t.Errorf("Agent = %q, want %q", cfg.Agent, c.want)
			}
		})
	}
}

// TestAgentsDirs is global→project, matching SkillDirs' precedence order so
// a project agent file can replace a global one by id.
func TestAgentsDirs(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwd, ".mtc"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadWithHome(t, home, cwd)
	if err != nil {
		t.Fatal(err)
	}
	dirs := cfg.AgentsDirs()
	want := []string{
		filepath.Join(home, ".config", "mtc", "agents"),
		filepath.Join(cwd, ".mtc", "agents"),
	}
	if len(dirs) != len(want) {
		t.Fatalf("AgentsDirs = %v", dirs)
	}
	for i := range want {
		if dirs[i] != want[i] {
			t.Errorf("AgentsDirs[%d] = %q, want %q", i, dirs[i], want[i])
		}
	}
}
