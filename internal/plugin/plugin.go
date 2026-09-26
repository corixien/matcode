// Package plugin hosts external plugins as JSON-RPC subprocesses (§8): one
// folder per plugin, newline-delimited JSON-RPC 2.0 over stdin/stdout, a
// crash-isolated hook bus. A failing plugin degrades to the default
// behavior — it never breaks a session.
package plugin

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Hook methods the host sends. Every name is a §8 hook surface entry,
// prefixed with "hook." as in the spec's protocol example.
const (
	HookSessionCompaction  = "hook.session.compaction"
	HookSessionContext     = "hook.session.context"
	HookToolBefore         = "hook.tool.execute.before"
	HookToolAfter          = "hook.tool.execute.after"
	HookPermissionEvaluate = "hook.permission.evaluate"
	HookShellBefore        = "hook.shell.create.before"
)

// plugin.toml permissions gate what a plugin may register; the host refuses
// anything undeclared (§8), so these are the complete v1 set.
const (
	PermSession       = "hooks.session"    // session.compaction, session.context
	PermTool          = "hooks.tool"       // tool.execute.before / .after
	PermPermission    = "hooks.permission" // permission.evaluate
	PermShell         = "hooks.shell"      // shell.create.before
	PermToolsRegister = "tools.register"   // reserved: plugin-provided tools
)

// AllPermissions is the declared set `mtc plugin check` validates against.
func AllPermissions() []string {
	return []string{PermSession, PermTool, PermPermission, PermShell, PermToolsRegister}
}

// permissionFor maps a hook method to the permission that gates it.
func permissionFor(method string) string {
	switch method {
	case HookSessionCompaction, HookSessionContext:
		return PermSession
	case HookToolBefore, HookToolAfter:
		return PermTool
	case HookPermissionEvaluate:
		return PermPermission
	case HookShellBefore:
		return PermShell
	default:
		return method // unknown methods are never granted
	}
}

const (
	defaultTimeout = 5 * time.Second
	maxTimeout     = 60 * time.Second
)

// Manifest is the parsed plugin.toml.
type Manifest struct {
	ID          string   `toml:"id"`
	Version     string   `toml:"version"`
	Enabled     *bool    `toml:"enabled"` // absent = enabled
	Permissions []string `toml:"permissions"`
	Cmd         []string `toml:"cmd"`
	Timeout     int      `toml:"timeout"` // seconds per hook call (0 = default)
}

// Plugin is one resolved plugin folder.
type Plugin struct {
	ID          string
	Version     string
	Enabled     bool
	Permissions []string
	Cmd         []string
	Timeout     time.Duration
	Dir         string // folder holding plugin.toml
	Manifest    string // plugin.toml path
	Problems    []string
}

// Allows reports whether the plugin declared perm ("*" grants everything).
func (p Plugin) Allows(perm string) bool {
	for _, g := range p.Permissions {
		switch {
		case g == "*", g == perm:
			return true
		case strings.HasSuffix(g, "*"):
			if strings.HasPrefix(perm, strings.TrimSuffix(g, "*")) {
				return true
			}
		}
	}
	return false
}

// Hooks lists the hook methods this plugin is granted (for `plugin list`).
func (p Plugin) Hooks() []string {
	var out []string
	for _, m := range []string{
		HookSessionContext, HookSessionCompaction, HookToolBefore,
		HookToolAfter, HookPermissionEvaluate, HookShellBefore,
	} {
		if p.Allows(permissionFor(m)) {
			out = append(out, m)
		}
	}
	return out
}

// Load reads every plugins/<name>/plugin.toml under dirs, lowest precedence
// first (global then project): a project folder with the same id replaces
// the global one. A folder that does not parse is skipped, never fatal —
// one broken plugin must not stop the rest (§8 failure isolation).
func Load(dirs ...string) []Plugin {
	byID := map[string]Plugin{}
	var order []string
	for _, root := range dirs {
		if root == "" {
			continue
		}
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			dir := filepath.Join(root, e.Name())
			p, ok := readManifest(dir)
			if !ok {
				continue
			}
			if _, seen := byID[p.ID]; !seen {
				order = append(order, p.ID)
			}
			byID[p.ID] = p
		}
	}
	out := make([]Plugin, 0, len(order))
	for _, id := range order {
		out = append(out, byID[id])
	}
	return out
}

// readManifest parses <dir>/plugin.toml and fills in the defaults.
func readManifest(dir string) (Plugin, bool) {
	path := filepath.Join(dir, "plugin.toml")
	var m Manifest
	if _, err := toml.DecodeFile(path, &m); err != nil {
		return Plugin{}, false
	}
	p := Plugin{
		ID:          strings.TrimSpace(m.ID),
		Version:     strings.TrimSpace(m.Version),
		Enabled:     m.Enabled == nil || *m.Enabled,
		Permissions: m.Permissions,
		Cmd:         m.Cmd,
		Dir:         dir,
		Manifest:    path,
	}
	if p.ID == "" {
		p.ID = filepath.Base(dir) // a missing id falls back to the folder name
	}
	if len(p.Cmd) == 0 {
		p.Cmd = inferCmd(dir)
	}
	switch {
	case m.Timeout <= 0:
		p.Timeout = defaultTimeout
	case m.Timeout > int(maxTimeout/time.Second):
		p.Timeout = maxTimeout
	default:
		p.Timeout = time.Duration(m.Timeout) * time.Second
	}
	return p, true
}

// inferCmd picks an entry file when plugin.toml declares no cmd, so the
// scaffold works before anyone reads the manifest format.
func inferCmd(dir string) []string {
	for _, c := range [][]string{
		{"main.sh"}, {"main.py"}, {"main.go"},
	} {
		if _, err := os.Stat(filepath.Join(dir, c[0])); err == nil {
			switch c[0] {
			case "main.sh":
				return []string{"bash", "main.sh"}
			case "main.py":
				return []string{"python3", "main.py"}
			default:
				return []string{"go", "run", "main.go"}
			}
		}
	}
	if st, err := os.Stat(filepath.Join(dir, "main")); err == nil && st.Mode()&0o111 != 0 {
		return []string{"./main"}
	}
	return nil
}

// entryFile names the script argument of cmd that must exist on disk
// ("python3 main.py" → main.py, "./main" → ./main, "mtc-plugin" → "").

func entryFile(cmd []string) string {
	if len(cmd) == 0 {
		return ""
	}
	if len(cmd) == 1 {
		if strings.Contains(cmd[0], ".") {
			return cmd[0]
		}
		return ""
	}
	for _, a := range cmd[1:] {
		if strings.Contains(a, ".") {
			return a
		}
	}
	return ""
}

// Validate checks the data tree without spawning anything: it reports what
// `mtc plugin check` and `mtc doctor` would refuse to run. count is how many
// plugin folders were found (valid or not).
func Validate(dirs ...string) (count int, problems []string) {
	seen := map[string]string{} // id -> folder
	for _, root := range dirs {
		if root == "" {
			continue
		}
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			count++
			dir := filepath.Join(root, e.Name())
			var m Manifest
			if _, err := toml.DecodeFile(filepath.Join(dir, "plugin.toml"), &m); err != nil {
				problems = append(problems, fmt.Sprintf("%s: plugin.toml: %v", e.Name(), err))
				continue
			}
			id := strings.TrimSpace(m.ID)
			if id == "" {
				id = e.Name()
				problems = append(problems, fmt.Sprintf("%s: plugin.toml has no id", e.Name()))
			}
			if prev, dup := seen[id]; dup {
				problems = append(problems, fmt.Sprintf("%s: id %q already used by %s", e.Name(), id, prev))
			}
			seen[id] = dir
			for _, perm := range m.Permissions {
				if !knownPermission(perm) {
					problems = append(problems, fmt.Sprintf("%s: unknown permission %q", e.Name(), perm))
				}
			}
			cmd := m.Cmd
			if len(cmd) == 0 {
				cmd = inferCmd(dir)
			}
			if len(cmd) == 0 {
				problems = append(problems, fmt.Sprintf("%s: no cmd in plugin.toml and no main.{sh,py,go} to infer", e.Name()))
				continue
			}
			if bin := cmd[0]; strings.Contains(bin, "/") {
				if _, err := os.Stat(filepath.Join(dir, bin)); err != nil {
					problems = append(problems, fmt.Sprintf("%s: command %q not found", e.Name(), bin))
					continue
				}
			} else if _, err := exec.LookPath(bin); err != nil {
				problems = append(problems, fmt.Sprintf("%s: command %q not found", e.Name(), bin))
				continue
			}
			if entry := entryFile(cmd); entry != "" {
				if _, err := os.Stat(filepath.Join(dir, entry)); err != nil {
					problems = append(problems, fmt.Sprintf("%s: entry %s: %v", e.Name(), entry, err))
				}
			}
			if m.Timeout < 0 || m.Timeout > int(maxTimeout/time.Second) {
				problems = append(problems, fmt.Sprintf("%s: timeout %ds out of range (0-%d)", e.Name(), m.Timeout, int(maxTimeout/time.Second)))
			}
		}
	}
	return count, problems
}

// knownPermission accepts the declared set plus the two grant shapes
// Allows understands: "*" for everything, and a prefix "*" such as
// "hooks.*" (§8 grant matching).
func knownPermission(p string) bool {
	p = strings.TrimSpace(p)
	if p == "" {
		return false
	}
	if p == "*" {
		return true
	}
	prefix := strings.TrimSuffix(p, "*")
	for _, k := range AllPermissions() {
		if k == p || (strings.HasSuffix(p, "*") && strings.HasPrefix(k, prefix)) {
			return true
		}
	}
	return false
}
