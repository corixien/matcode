package commands

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"matcode/internal/agents"
	"matcode/internal/catalog"
	"matcode/internal/cmds"
	"matcode/internal/config"
	"matcode/internal/mcp"
	"matcode/internal/permissions"
	"matcode/internal/plugin"
	"matcode/internal/skills"
	"matcode/internal/tools"
	"matcode/internal/tui/theme"
)

// Doctor implements `mtc doctor`: validate config, paths, env, and deps.
func Doctor(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "output as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	cfg, err := config.Load(cwd)
	if err != nil {
		return fmt.Errorf("config load: %w", err)
	}

	type check struct {
		Name   string `json:"name"`
		Status string `json:"status"` // ok, warn, error
		Detail string `json:"detail"`
	}
	var checks []check

	// 1. Config file
	cfgPath, _ := cfg.Find("config.toml")
	if cfgPath != "" {
		checks = append(checks, check{"config file", "ok", cfgPath})
	} else {
		checks = append(checks, check{"config file", "warn", "using defaults (no config.toml found)"})
	}

	// 2. Data directory
	dataDir := cfg.DataDir()
	if st, err := os.Stat(dataDir); err != nil {
		checks = append(checks, check{"data dir", "error", fmt.Sprintf("%s: %v", dataDir, err)})
	} else if !st.IsDir() {
		checks = append(checks, check{"data dir", "error", fmt.Sprintf("%s exists but is not a directory", dataDir)})
	} else {
		// writable?
		test := filepath.Join(dataDir, ".mtc-write-test")
		if err := os.WriteFile(test, []byte("x"), 0600); err != nil {
			checks = append(checks, check{"data dir", "error", fmt.Sprintf("%s not writable: %v", dataDir, err)})
		} else {
			os.Remove(test)
			checks = append(checks, check{"data dir", "ok", dataDir})
		}
	}

	// 3. Sessions directory
	sessDir := cfg.SessionsDir()
	if st, err := os.Stat(sessDir); err != nil {
		checks = append(checks, check{"sessions dir", "warn", fmt.Sprintf("%s: %v (created on first session)", sessDir, err)})
	} else if !st.IsDir() {
		checks = append(checks, check{"sessions dir", "error", fmt.Sprintf("%s exists but is not a directory", sessDir)})
	} else {
		checks = append(checks, check{"sessions dir", "ok", sessDir})
	}

	// 4. Themes directory
	themeDir := cfg.ThemesDir()
	if st, err := os.Stat(themeDir); err != nil {
		checks = append(checks, check{"themes dir", "warn", fmt.Sprintf("%s: %v (using built-in default)", themeDir, err)})
	} else if !st.IsDir() {
		checks = append(checks, check{"themes dir", "error", fmt.Sprintf("%s exists but is not a directory", themeDir)})
	} else {
		names := theme.Names(themeDir)
		checks = append(checks, check{"themes dir", "ok", fmt.Sprintf("%s (%d themes)", themeDir, len(names)-1)})
	}

	// 5. Providers: key resolution
	for name, p := range cfg.Providers {
		if _, err := p.ResolveKey(); err != nil {
			checks = append(checks, check{fmt.Sprintf("provider %s key", name), "warn", err.Error()})
		} else {
			checks = append(checks, check{fmt.Sprintf("provider %s key", name), "ok", "resolved from env"})
		}
	}

	// 6. Model addressable
	if cfg.Model == "" {
		checks = append(checks, check{"model", "warn", "no default model set (config.toml: model)"})
	} else {
		ok := false
		for name, p := range cfg.Providers {
			if p.DefaultModel != "" && cfg.Model == name+"/"+p.DefaultModel {
				ok = true
				break
			}
			if cfg.Model == name+"/"+p.DefaultModel || (p.DefaultModel != "" && cfg.Model == name+"/"+p.DefaultModel) {
				ok = true
				break
			}
			// config may directly address a model
			if cfg.Model == name+"/"+p.DefaultModel || (strings.HasPrefix(cfg.Model, name+"/") && p.DefaultModel != "") {
				ok = true
				break
			}
		}
		if ok {
			detail := cfg.Model
			if _, _, m, found := catalog.Lookup(cfg.Model); found {
				// Spec §4: doctor shows which source a model came from —
				// the embedded models.dev catalog or the config/provider.
				detail = fmt.Sprintf("%s (%s, %d tok ctx, models.dev embedded)", cfg.Model, m.Name, m.Context)
			} else {
				detail += " (source: config/provider; not in embedded catalog)"
			}
			checks = append(checks, check{"model", "ok", detail})
		} else {
			checks = append(checks, check{"model", "warn", fmt.Sprintf("%s (no matching provider/default_model)", cfg.Model)})
		}
	}

	// 7. Skills
	skillDirs := cfg.SkillDirs()
	if len(skillDirs) == 0 {
		checks = append(checks, check{"skills", "warn", "no skill dirs configured"})
	} else {
		_, err := skills.Load(skillDirs...)
		if err != nil {
			checks = append(checks, check{"skills", "warn", err.Error()})
		} else {
			checks = append(checks, check{"skills", "ok", fmt.Sprintf("%d dirs", len(skillDirs))})
		}
	}

	// 8. Agents: overlays parse and the default agent resolves.
	if err := agents.Load(cfg.AgentsDirs()...); err != nil {
		checks = append(checks, check{"agents", "warn", err.Error()})
	} else {
		defaultID := cfg.Agent
		if defaultID == "" {
			defaultID = "build"
		}
		if ag, err := agents.Get(defaultID); err != nil {
			checks = append(checks, check{"agents", "warn",
				fmt.Sprintf("default agent %q does not resolve: %v", defaultID, err)})
		} else if ag.Disabled {
			checks = append(checks, check{"agents", "warn", fmt.Sprintf("default agent %q is disabled", ag.ID)})
		} else {
			detail := fmt.Sprintf("%d selectable", len(agents.IDs()))
			if ag.Hidden {
				detail += fmt.Sprintf(" (default %q is hidden)", ag.ID)
			}
			checks = append(checks, check{"agents", "ok", detail})
		}
	}

	// 9. Slash commands (data tree, row 33)
	if list, err := cmds.Load(cfg.CommandsDirs()...); err != nil {
		checks = append(checks, check{"commands", "warn", err.Error()})
	} else {
		checks = append(checks, check{"commands", "ok", fmt.Sprintf("%d loaded", len(list))})
	}

	// 10. User tools (data tree, row 33): tool.toml + exec.sh per folder.
	if n, problems := tools.Validate(cfg.ToolsDirs()...); len(problems) > 0 {
		checks = append(checks, check{"user tools", "warn",
			fmt.Sprintf("%d loadable, %s", n, strings.Join(problems, "; "))})
	} else {
		checks = append(checks, check{"user tools", "ok", fmt.Sprintf("%d loaded", n)})
	}

	// 11. Plugins (data tree, §8): manifest, grants, entry file.
	if n, problems := plugin.Validate(cfg.PluginsDirs()...); len(problems) > 0 {
		checks = append(checks, check{"plugins", "warn",
			fmt.Sprintf("%d loadable, %s", n, strings.Join(problems, "; "))})
	} else {
		checks = append(checks, check{"plugins", "ok", fmt.Sprintf("%d loaded", n)})
	}

	// 12. Permissions
	permsPath := filepath.Join(dataDir, "permissions.toml")
	if _, err := permissions.Load(permsPath); err != nil {
		checks = append(checks, check{"permissions", "warn", err.Error()})
	} else {
		checks = append(checks, check{"permissions", "ok", permsPath})
	}

	// 13. MCP servers (if mcp.json exists)
	mcpPath := filepath.Join(dataDir, "mcp.json")
	if _, err := mcp.Load(mcpPath); err != nil {
		if os.IsNotExist(err) {
			checks = append(checks, check{"mcp servers", "ok", "none (no mcp.json)"})
		} else {
			checks = append(checks, check{"mcp servers", "warn", err.Error()})
		}
	} else {
		checks = append(checks, check{"mcp servers", "ok", mcpPath})
	}

	// 14. Go version / binary
	checks = append(checks, check{"go version", "ok", runtime.Version()})

	// Output
	if *jsonOut {
		b, _ := json.MarshalIndent(checks, "", "  ")
		fmt.Println(string(b))
		return nil
	}

	// Human-readable
	fmt.Println("matcode doctor")
	fmt.Println("==============")
	hasError := false
	for _, c := range checks {
		sym := "✓"
		if c.Status == "warn" {
			sym = "⚠"
		} else if c.Status == "error" {
			sym = "✗"
			hasError = true
		}
		fmt.Printf("  %s %-20s %s\n", sym, c.Name+":", c.Detail)
	}
	if hasError {
		return fmt.Errorf("doctor found errors")
	}
	return nil
}
