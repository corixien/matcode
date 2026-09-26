package app

import (
	"context"
	"fmt"
	"path/filepath"

	"matcode/internal/mcp"
)

// SetMCPEnabled flips one server's enabled flag in mcp.json (row 46).
// The change is config-only: call RestartMCP to apply it live.
func (b *Built) SetMCPEnabled(name string, on bool) error {
	path := mcp.Path(b.Cfg.DataDir())
	servers, err := mcp.Load(path)
	if err != nil {
		return err
	}
	s, ok := servers[name]
	if !ok {
		return fmt.Errorf("mcp: no such server %q", name)
	}
	enabled := on
	s.Enabled = &enabled
	servers[name] = s
	return mcp.Save(path, servers)
}

// RestartMCP re-dials one server from mcp.json and swaps its `server__`
// tools into the live registry (§7 lifecycle, rows 43/46):
//
//   - enabled: dial + tools/list → ReplaceMCP(name, tools); the old
//     client is closed and the Clients entry replaced;
//   - disabled or dial failure: the server's tools are dropped.
//
// A failure after an enabled server is reported to the caller and
// leaves that server's tools removed — the registry never keeps a
// stale, non-working slice.
func (b *Built) RestartMCP(name string) error {
	servers, err := mcp.Load(mcp.Path(b.Cfg.DataDir()))
	if err != nil {
		return err
	}
	s, ok := servers[name]
	if !ok {
		return fmt.Errorf("mcp: no such server %q", name)
	}
	idx := -1
	for i, c := range b.Clients {
		if c.Name == name {
			idx = i
			break
		}
	}
	if idx >= 0 {
		_ = b.Clients[idx].Close()
	}
	if !s.IsEnabled() {
		b.Engine.ReplaceMCP(name, nil)
		if idx >= 0 {
			b.Clients = append(b.Clients[:idx], b.Clients[idx+1:]...)
		}
		return nil
	}
	if s.LogDir == "" {
		s.LogDir = filepath.Join(b.Cfg.DataDir(), "logs")
	}
	ctx, cancel := context.WithTimeout(context.Background(), mcp.DialTimeout)
	defer cancel()
	c, err := mcp.Dial(ctx, name, s)
	if err != nil {
		b.Engine.ReplaceMCP(name, nil)
		return fmt.Errorf("mcp %s: %w", name, err)
	}
	mt, err := mcp.EngineTools(ctx, c)
	if err != nil {
		_ = c.Close()
		b.Engine.ReplaceMCP(name, nil)
		return fmt.Errorf("mcp %s: tools/list: %w", name, err)
	}
	// Same live-swap wiring as the initial build (app.go New).
	c.OnToolsChanged = func() { reloadMCPTools(b.Engine, c) }
	b.Engine.ReplaceMCP(name, mt)
	if idx >= 0 {
		b.Clients[idx] = c
	} else {
		b.Clients = append(b.Clients, c)
	}
	return nil
}
