package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"matcode/internal/engine"
)

// Path resolves mcp.json for a data dir (project or global).
func Path(dataDir string) string { return filepath.Join(dataDir, "mcp.json") }

// ToolName namespaces a server tool as `server__tool` (spec §7 discovery).
func ToolName(server, tool string) string { return server + "__" + tool }

// DialTimeout bounds one server's dial + handshake. Every enabled
// server gets its own budget and dials run in parallel, so one hung
// command can't starve the rest of the build (§7 lifecycle).
const DialTimeout = 30 * time.Second

// LoadEnabled dials every enabled server in dataDir's mcp.json. A failing
// server is reported in errs but never fatal: the registry simply lacks it.
func LoadEnabled(ctx context.Context, dataDir string, known map[string]Server) ([]*Client, map[string]string, error) {
	if known == nil {
		var err error
		known, err = Load(Path(dataDir))
		if err != nil {
			return nil, nil, err
		}
	}
	type result struct {
		name string
		c    *Client
		err  error
	}
	names := make([]string, 0, len(known))
	for n := range known {
		names = append(names, n)
	}
	sort.Strings(names)
	enabled := make([]string, 0, len(names))
	for _, name := range names {
		if known[name].IsEnabled() {
			enabled = append(enabled, name)
		}
	}
	ch := make(chan result, len(enabled))
	for _, name := range enabled {
		name := name
		s := known[name]
		if dataDir != "" {
			// Per-server log: stdio stderr lands in <data>/logs (§7).
			s.LogDir = filepath.Join(dataDir, "logs")
		}
		go func() {
			dctx, cancel := context.WithTimeout(ctx, DialTimeout)
			defer cancel()
			c, err := Dial(dctx, name, s)
			ch <- result{name, c, err}
		}()
	}
	errs := map[string]string{}
	byName := make(map[string]*Client, len(enabled))
	for range enabled {
		r := <-ch
		if r.err != nil {
			errs[r.name] = r.err.Error()
			continue
		}
		byName[r.name] = r.c
	}
	// Stable order (sorted names) regardless of dial completion order.
	var clients []*Client
	for _, name := range enabled {
		if c := byName[name]; c != nil {
			clients = append(clients, c)
		}
	}
	return clients, errs, nil
}

// EngineTools converts a client's advertised tools into engine tools named
// `server__tool`, so permissions.toml can match them by that id.
func EngineTools(ctx context.Context, c *Client) ([]engine.Tool, error) {
	tools, err := c.ListTools(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]engine.Tool, 0, len(tools))
	for _, t := range tools {
		t := t
		id := ToolName(c.Name, t.Name)
		schema := t.InputSchema
		if schema == nil {
			schema = map[string]any{"type": "object"}
		}
		out = append(out, engine.Tool{
			ID:          id,
			Description: t.Description,
			Schema:      schema,
			Execute: func(ctx context.Context, input json.RawMessage) (engine.Result, error) {
				text, err := c.CallTool(ctx, t.Name, input)
				if err != nil {
					return engine.Result{}, fmt.Errorf("%s: %w", id, err)
				}
				return engine.Result{Text: text}, nil
			},
		})
	}
	return out, nil
}

// CloseAll closes every client, collecting nothing (best effort).
func CloseAll(cs []*Client) {
	for _, c := range cs {
		_ = c.Close()
	}
}

// Describe renders one server line for `mtc mcp list`.
func Describe(name string, s Server) string {
	state := "disabled"
	if s.IsEnabled() {
		state = "enabled"
	}
	target := s.URL
	if s.Transport() == "stdio" {
		target = strings.Join(s.Command, " ")
	}
	return fmt.Sprintf("%s\t%s\t%s\t%s", name, s.Transport(), state, orDash(target))
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// ErrNoMCP is returned when mcp.json is absent (callers may soften it).
var ErrNoMCP = fmt.Errorf("no mcp.json")

// Exists reports whether dataDir has an mcp.json.
func Exists(dataDir string) bool {
	_, err := os.Stat(Path(dataDir))
	return err == nil
}
