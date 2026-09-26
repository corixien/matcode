package commands

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"matcode/internal/mcp"
)

// MCP implements `mtc mcp add|list|rm` over mcp.json (spec §7).
//
//	mtc mcp add <name> --command "cmd args…" [--env K=V]… [--header k:v]… [--global]
//	mtc mcp add <name> --url <https://…> [--header k:v]… [--global]
//	mtc mcp list [-json] [--all]
//	mtc mcp rm <name> [--global]
func MCP(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: mtc mcp add|list|rm … (try: mtc mcp list)")
	}
	switch args[0] {
	case "add":
		return mcpAdd(args[1:])
	case "list", "ls":
		return mcpList(args[1:])
	case "rm", "remove":
		return mcpRm(args[1:])
	default:
		return fmt.Errorf("unknown mcp command %q (want add, list, rm)", args[0])
	}
}

// mcpFile picks the config file location: project .mtc/mcp.json, or the
// global tree with --global.
func mcpFile(global bool, explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	cfg, _, err := sessionConfig()
	if err != nil {
		return "", err
	}
	if global || cfg.ProjectDir == "" {
		return filepath.Join(cfg.GlobalDir, "mcp.json"), nil
	}
	return filepath.Join(cfg.ProjectDir, "mcp.json"), nil
}

func mcpAdd(args []string) error {
	fs := flag.NewFlagSet("mcp add", flag.ContinueOnError)
	var envs, headers stringList
	var command, url string
	var global, enabled bool
	fs.StringVar(&command, "command", "", "stdio server command line (split on spaces)")
	fs.StringVar(&url, "url", "", "remote server URL (streamable HTTP)")
	fs.Var(&envs, "env", "environment variable the server inherits: K=VARNAME (repeatable; mcp.json stores {env:VARNAME})")
	fs.Var(&headers, "header", "request header \"k: v\" (repeatable)")
	fs.BoolVar(&global, "global", false, "write the global mcp.json instead of the project one")
	fs.BoolVar(&enabled, "enabled", true, "start disabled with -enabled=false")
	if err := parse(fs, args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) != 1 {
		return fmt.Errorf("usage: mtc mcp add <name> [--command …|--url …] [flags]")
	}
	name := rest[0]
	if strings.Contains(name, "__") {
		return fmt.Errorf("mcp: server name %q may not contain __ (it separates server__tool names)", name)
	}
	if command == "" && url == "" {
		return fmt.Errorf("mcp add: need --command (stdio) or --url (http)")
	}
	if command != "" && url != "" {
		return fmt.Errorf("mcp add: --command and --url are mutually exclusive")
	}
	srv := mcp.Server{URL: url, Enabled: &enabled}
	if command != "" {
		srv.Type = "local"
		srv.Command = strings.Fields(command)
		srv.Environment = map[string]string{}
		for _, e := range envs {
			k, v, ok := strings.Cut(e, "=")
			if !ok || k == "" {
				return fmt.Errorf("mcp add: -env must be K=V, got %q", e)
			}
			// mcp.json never stores secret values: V names the host
			// environment variable the server process inherits.
			if inner, isRef := strings.CutPrefix(v, "{env:"); isRef {
				v, _ = strings.CutSuffix(inner, "}")
			}
			if !validEnvName(v) {
				return fmt.Errorf("mcp add: -env value %q must be an environment variable name (secrets live in the environment, not mcp.json)", v)
			}
			srv.Environment[k] = "{env:" + v + "}"
		}
	} else {
		srv.Type = "remote"
	}
	if len(headers) > 0 {
		srv.Headers = map[string]string{}
		for _, h := range headers {
			k, v, ok := strings.Cut(h, ":")
			if !ok || strings.TrimSpace(k) == "" {
				return fmt.Errorf("mcp add: -header must be \"k: v\", got %q", h)
			}
			v = strings.TrimSpace(v)
			// Header values are secrets too (Authorization, X-Api-Key):
			// mcp.json stores only the name of the host environment
			// variable holding the value — same rule as -env (§7).
			inner, isRef := strings.CutPrefix(v, "{env:")
			if isRef {
				v, _ = strings.CutSuffix(inner, "}")
			}
			if !validEnvName(v) {
				return fmt.Errorf("mcp add: -header value for %q must be {env:VARNAME} (or a bare variable name); secrets live in the environment, not mcp.json", strings.TrimSpace(k))
			}
			srv.Headers[strings.TrimSpace(k)] = "{env:" + v + "}"
		}
	}

	path, err := mcpFile(global, "")
	if err != nil {
		return err
	}
	servers, err := mcp.Load(path)
	if err != nil {
		return err
	}
	if _, dup := servers[name]; dup {
		return fmt.Errorf("mcp: server %q already exists in %s (rm it first)", name, path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	servers[name] = srv
	if err := mcp.Save(path, servers); err != nil {
		return err
	}
	fmt.Printf("added %s (%s) to %s\n", name, srv.Transport(), path)
	return nil
}

func mcpList(args []string) error {
	fs := flag.NewFlagSet("mcp list", flag.ContinueOnError)
	var global bool
	jsonOut := fs.Bool("json", false, "machine-readable JSON")
	fs.BoolVar(&global, "global", false, "read the global mcp.json only")
	showTools := fs.Bool("tools", false, "dial enabled servers and list their tools")
	if err := parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("usage: mtc mcp list [-json] [-tools] [-global]")
	}
	servers, path, err := mcpReadAll(global)
	if err != nil {
		return err
	}
	if *showTools {
		return mcpListTools(servers, *jsonOut)
	}
	names := make([]string, 0, len(servers))
	for n := range servers {
		names = append(names, n)
	}
	sort.Strings(names)

	type row struct {
		Name      string `json:"name"`
		Transport string `json:"transport"`
		Enabled   bool   `json:"enabled"`
		Target    string `json:"target"`
	}
	rows := make([]row, 0, len(names))
	for _, n := range names {
		s := servers[n]
		target := s.URL
		if s.Transport() == "stdio" {
			target = strings.Join(s.Command, " ")
		}
		rows = append(rows, row{Name: n, Transport: s.Transport(), Enabled: s.IsEnabled(), Target: target})
	}
	if *jsonOut {
		payload := struct {
			File    string `json:"file"`
			Servers []row  `json:"servers"`
		}{File: path, Servers: rows}
		return printJSON(payload)
	}
	if len(rows) == 0 {
		fmt.Printf("no MCP servers in %s\n", path)
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tTRANSPORT\tSTATE\tTARGET")
	for _, r := range rows {
		state := "disabled"
		if r.Enabled {
			state = "enabled"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", r.Name, r.Transport, state, orDash(r.Target))
	}
	return w.Flush()
}

// mcpListTools dials every enabled server and prints its namespaced tools.
func mcpListTools(servers map[string]mcp.Server, jsonOut bool) error {
	type row struct {
		Server string   `json:"server"`
		Errors string   `json:"error,omitempty"`
		Tools  []string `json:"tools"`
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	clients, errs, err := mcp.LoadEnabled(ctx, "", servers)
	if err != nil {
		return err
	}
	defer mcp.CloseAll(clients)
	var rows []row
	for _, c := range clients {
		tools, err := mcp.EngineTools(ctx, c)
		if err != nil {
			rows = append(rows, row{Server: c.Name, Errors: err.Error()})
			continue
		}
		ids := make([]string, 0, len(tools))
		for _, t := range tools {
			ids = append(ids, t.ID)
		}
		rows = append(rows, row{Server: c.Name, Tools: ids})
	}
	for name, e := range errs {
		rows = append(rows, row{Server: name, Errors: e})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Server < rows[j].Server })
	if jsonOut {
		return printJSON(rows)
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "SERVER\tTOOLS\tERROR")
	for _, r := range rows {
		fmt.Fprintf(w, "%s\t%s\t%s\n", r.Server, strings.Join(r.Tools, ", "), orDash(r.Errors))
	}
	return w.Flush()
}

func mcpRm(args []string) error {
	fs := flag.NewFlagSet("mcp rm", flag.ContinueOnError)
	var global bool
	fs.BoolVar(&global, "global", false, "remove from the global mcp.json")
	if err := parse(fs, args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) != 1 {
		return fmt.Errorf("usage: mtc mcp rm <name> [-global]")
	}
	path, err := mcpFile(global, "")
	if err != nil {
		return err
	}
	servers, err := mcp.Load(path)
	if err != nil {
		return err
	}
	if _, ok := servers[rest[0]]; !ok {
		return fmt.Errorf("mcp: no server %q in %s", rest[0], path)
	}
	delete(servers, rest[0])
	if err := mcp.Save(path, servers); err != nil {
		return err
	}
	fmt.Printf("removed %s from %s\n", rest[0], path)
	return nil
}

// validEnvName reports whether s is a plausible environment variable name
// (letters, digits, underscore; must start with a letter or underscore).
func validEnvName(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'):
		case r >= '0' && r <= '9':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// mcpReadAll loads project + global registries (project wins per name).
func mcpReadAll(global bool) (map[string]mcp.Server, string, error) {
	cfg, _, err := sessionConfig()
	if err != nil {
		return nil, "", err
	}
	files := []string{}
	if !global && cfg.ProjectDir != "" {
		files = append(files, filepath.Join(cfg.ProjectDir, "mcp.json"))
	}
	files = append(files, filepath.Join(cfg.GlobalDir, "mcp.json"))
	merged := map[string]mcp.Server{}
	primary := files[0]
	for _, f := range files {
		got, err := mcp.Load(f)
		if err != nil {
			return nil, "", err
		}
		for n, s := range got {
			merged[n] = s
		}
	}
	return merged, primary, nil
}
