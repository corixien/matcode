package commands

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"matcode/internal/agents"
	"matcode/internal/config"
)

// Debug implements `mtc debug agents|config|paths [selector]`.
// Bare `mtc debug` prints every labeled path.
func Debug(args []string) error {
	fs := flag.NewFlagSet("debug", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "machine-readable JSON")
	if err := parse(fs, args); err != nil {
		return err
	}
	rest := fs.Args()
	what := "paths"
	if len(rest) > 0 {
		what = rest[0]
		rest = rest[1:]
	}
	switch what {
	case "agents":
		if len(rest) != 0 {
			return fmt.Errorf("usage: mtc debug agents [-json]")
		}
		return debugAgents(*jsonOut)
	case "config":
		if len(rest) != 0 {
			return fmt.Errorf("usage: mtc debug config [-json]")
		}
		return debugConfig(*jsonOut)
	case "paths":
		if len(rest) > 1 {
			return fmt.Errorf("usage: mtc debug paths [selector]")
		}
		selector := ""
		if len(rest) == 1 {
			selector = rest[0]
		}
		return debugPaths(selector, *jsonOut)
	default:
		return fmt.Errorf("unknown debug target %q (want agents, config, paths)", what)
	}
}

// debugPaths prints the resolved data tree. With a selector only that path is
// printed (bare output is script-friendly: `value` alone).
func debugPaths(selector string, jsonOut bool) error {
	cfg, cwd, err := sessionConfig()
	if err != nil {
		return err
	}
	bin, err := os.Executable()
	if err != nil {
		bin = ""
	}
	type entry struct {
		Path   string `json:"path"`
		Exists bool   `json:"exists"`
	}
	order := []string{"home", "config", "project", "data", "sessions", "logs", "skills", "db", "bin", "tmp"}
	paths := map[string]string{
		"home":     mustHome(),
		"config":   cfg.GlobalDir,
		"project":  projectOrDash(cfg),
		"data":     cfg.DataDir(),
		"sessions": cfg.SessionsDir(),
		"logs":     filepath.Join(cfg.DataDir(), "logs"),
		"skills":   strings.Join(cfg.SkillDirs(), string(filepath.ListSeparator)),
		"db":       filepath.Join(cfg.DataDir(), "index.db"),
		"bin":      bin,
		"tmp":      os.TempDir(),
	}
	if selector != "" {
		p, ok := paths[selector]
		if !ok {
			return fmt.Errorf("unknown path selector %q (want %s)", selector, strings.Join(order, ", "))
		}
		if jsonOut {
			return printJSON(entry{Path: p, Exists: pathExists(p)})
		}
		fmt.Println(p)
		return nil
	}
	if jsonOut {
		rows := make([]map[string]any, 0, len(order))
		for _, k := range order {
			rows = append(rows, map[string]any{
				"name": k, "path": paths[k], "exists": pathExists(paths[k]),
			})
		}
		return printJSON(map[string]any{"cwd": cwd, "paths": rows})
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tPATH\tEXISTS")
	for _, k := range order {
		fmt.Fprintf(w, "%s\t%s\t%s\n", k, paths[k], yesNo(pathExists(paths[k])))
	}
	return w.Flush()
}

// debugAgents prints the compiled agent roster: role, tool allowlist, and the
// flags that change runtime behavior.
func debugAgents(jsonOut bool) error {
	cfg, _, err := sessionConfig()
	if err != nil {
		return err
	}
	if err := agents.Load(cfg.AgentsDirs()...); err != nil {
		return err
	}
	type row struct {
		ID        string   `json:"id"`
		Default   bool     `json:"default"`
		Tools     []string `json:"tools"`
		Bypass    bool     `json:"bypass_permissions"`
		Stateless bool     `json:"stateless"`
		Model     string   `json:"model,omitempty"`
		Steps     int      `json:"steps,omitempty"`
		Color     string   `json:"color,omitempty"`
		Source    string   `json:"source"`
		System    int      `json:"system_chars"`
	}
	rows := make([]row, 0, len(agents.IDs()))
	for _, id := range agents.IDs() {
		a, err := agents.Get(id)
		if err != nil {
			return err
		}
		src := "builtin"
		if _, ok := agents.FromFile(id); ok {
			src = "file"
		}
		rows = append(rows, row{
			ID: id, Default: id == defaultAgent(cfg), Tools: a.ToolIDs,
			Bypass: a.BypassPermissions, Stateless: a.Stateless,
			Model: a.Model, Steps: a.Steps, Color: a.Color, Source: src,
			System: len(a.System),
		})
	}
	if jsonOut {
		return printJSON(rows)
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tDEFAULT\tSOURCE\tTOOLS\tBYPASS\tSTATELESS\tMODEL\tSTEPS\tSYSTEM CHARS")
	for _, r := range rows {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%d\n",
			r.ID, yesNo(r.Default), r.Source, strings.Join(r.Tools, ","),
			yesNo(r.Bypass), yesNo(r.Stateless), orDash(r.Model), yesNo(r.Steps > 0), r.System)
	}
	return w.Flush()
}

// debugConfig prints the resolved config. Secrets are never shown: providers
// report their key variable name and whether it is set.
func debugConfig(jsonOut bool) error {
	cfg, cwd, err := sessionConfig()
	if err != nil {
		return err
	}
	type provider struct {
		Name    string `json:"name"`
		Dialect string `json:"dialect"`
		BaseURL string `json:"base_url"`
		KeyEnv  string `json:"key_env,omitempty"`
		KeySet  bool   `json:"key_set"`
	}
	names := make([]string, 0, len(cfg.Providers))
	for n := range cfg.Providers {
		names = append(names, n)
	}
	sort.Strings(names)
	provs := make([]provider, 0, len(names))
	for _, n := range names {
		p := cfg.Providers[n]
		d := p.Dialect
		if d == "" {
			d = "openai"
		}
		set := false
		if p.APIKey.Env != "" {
			_, err := p.ResolveKey()
			set = err == nil
		}
		provs = append(provs, provider{Name: n, Dialect: d, BaseURL: p.BaseURL, KeyEnv: p.APIKey.Env, KeySet: set})
	}
	refs := make([]string, 0, len(cfg.References))
	for a := range cfg.References {
		refs = append(refs, a)
	}
	sort.Strings(refs)

	payload := map[string]any{
		"cwd": cwd, "model": cfg.Model, "agent": defaultAgent(cfg),
		"formatter": cfg.Formatter, "snapshots": cfg.Snapshots,
		"auto_compact": cfg.AutoCompact, "context_limit": cfg.ContextLimit,
		"api_port": cfg.APIPort, "media": cfg.Media,
		"global_dir": cfg.GlobalDir, "project_dir": cfg.ProjectDir,
		"data_dir": cfg.DataDir(), "references": refs, "providers": provs,
	}
	if jsonOut {
		return printJSON(payload)
	}
	fmt.Printf("cwd            %s\n", cwd)
	fmt.Printf("model          %s\n", orDash(cfg.Model))
	fmt.Printf("agent          %s\n", defaultAgent(cfg))
	fmt.Printf("formatter      %s\n", yesNo(cfg.Formatter))
	fmt.Printf("snapshots      %s\n", yesNo(cfg.Snapshots))
	fmt.Printf("auto_compact   %s\n", yesNo(cfg.AutoCompact))
	fmt.Printf("context_limit  %d\n", cfg.ContextLimit)
	fmt.Printf("api_port       %d\n", cfg.APIPort)
	fmt.Printf("global_dir     %s\n", cfg.GlobalDir)
	fmt.Printf("project_dir    %s\n", projectOrDash(cfg))
	fmt.Printf("data_dir       %s\n", cfg.DataDir())
	fmt.Printf("references     %s\n", orDash(strings.Join(refs, ", ")))
	fmt.Printf("providers      %d\n", len(provs))
	for _, p := range provs {
		key := "-"
		if p.KeyEnv != "" {
			if p.KeySet {
				key = p.KeyEnv + " (set)"
			} else {
				key = p.KeyEnv + " (missing)"
			}
		}
		fmt.Printf("  %-12s %s  %s  key=%s\n", p.Name, p.Dialect, orDash(p.BaseURL), key)
	}
	return nil
}

// defaultAgent mirrors the agent selection the run command uses.
func defaultAgent(cfg *config.Config) string {
	if cfg.Agent != "" {
		return cfg.Agent
	}
	return "build"
}

func mustHome() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return h
}

func projectOrDash(cfg *config.Config) string {
	if cfg.ProjectDir == "" {
		return "-"
	}
	return cfg.ProjectDir
}

func pathExists(p string) bool {
	if p == "" || p == "-" {
		return false
	}
	// skills may be a list; any existing entry counts
	for _, one := range strings.Split(p, string(filepath.ListSeparator)) {
		if _, err := os.Stat(one); err == nil {
			return true
		}
	}
	return false
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func printJSON(v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(b))
	return nil
}
