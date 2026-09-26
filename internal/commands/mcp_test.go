package commands

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMCPUnknown(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"bogus"},
		{"add"},
		{"add", "a", "b"},
		{"add", "x"}, // no command/url
		{"add", "x", "--command", "c", "--url", "u"}, // both
		{"add", "bad__name", "--command", "c"},       // __ reserved
		{"add", "y", "--command", "c", "--env", "NOEQ"},
		{"add", "y2", "--command", "c", "--env", "K=not-a-name"}, // secret-ish literal rejected
		{"add", "z", "--command", "c", "--header", "nocolon"},
		{"rm"},
		{"rm", "nope"},
		{"list", "extra"},
	} {
		if err := MCP(args); err == nil {
			t.Errorf("MCP(%q) = nil, want error", args)
		}
	}
}

func TestMCPAddListRm(t *testing.T) {
	dir := withAPIProject(t, "")
	out := captureStdout(t, func() error {
		return MCP([]string{"add", "local1", "--command", "python3 fake.py", "--env", "KEY=MCP_TEST_SECRET"})
	})
	if !strings.Contains(out, "added local1") {
		t.Errorf("add output = %q", out)
	}
	path := filepath.Join(dir, ".mtc", "mcp.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "sk-") {
		t.Error("mcp.json must never hold a secret value")
	}
	if !strings.Contains(string(b), "{env:MCP_TEST_SECRET}") {
		t.Errorf("mcp.json should store {env:MCP_TEST_SECRET}: %s", b)
	}
	var f struct {
		MCP map[string]*struct {
			Enabled     *bool             `json:"enabled"`
			Environment map[string]string `json:"environment"`
		} `json:"mcp"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatalf("bad mcp.json: %v", err)
	}
	if f.MCP["local1"] == nil || f.MCP["local1"].Enabled == nil || !*f.MCP["local1"].Enabled {
		t.Errorf("server should be enabled by default: %s", b)
	}
	if f.MCP["local1"].Environment["KEY"] != "{env:MCP_TEST_SECRET}" {
		t.Errorf("env = %v", f.MCP["local1"].Environment)
	}

	// duplicate rejected
	if err := MCP([]string{"add", "local1", "--command", "x"}); err == nil {
		t.Error("duplicate add = nil error")
	}

	// remote variant — header values are env refs only (row 44)
	out = captureStdout(t, func() error {
		return MCP([]string{"add", "web", "--url", "https://ex.com/mcp", "--header", "Authorization: MCP_TEST_AUTH", "--enabled=false"})
	})
	if !strings.Contains(out, "added web") {
		t.Errorf("remote add = %q", out)
	}
	if err := MCP([]string{"add", "leak", "--url", "https://ex.com/mcp", "--header", "Authorization: Bearer t"}); err == nil {
		t.Error("literal header value should be rejected")
	}

	// list table + json
	out = captureStdout(t, func() error { return MCP([]string{"list"}) })
	if !strings.Contains(out, "local1") || !strings.Contains(out, "disabled") {
		t.Errorf("list = \n%s", out)
	}
	out = captureStdout(t, func() error { return MCP([]string{"list", "-json"}) })
	var payload struct {
		File    string `json:"file"`
		Servers []struct {
			Name    string `json:"name"`
			Enabled bool   `json:"enabled"`
		} `json:"servers"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("bad json %q: %v", out, err)
	}
	if len(payload.Servers) != 2 {
		t.Errorf("servers = %+v", payload.Servers)
	}

	// rm
	out = captureStdout(t, func() error { return MCP([]string{"rm", "web"}) })
	if !strings.Contains(out, "removed web") {
		t.Errorf("rm = %q", out)
	}
	if err := MCP([]string{"rm", "web"}); err == nil {
		t.Error("rm missing = nil error")
	}
	out = captureStdout(t, func() error { return MCP([]string{"list"}) })
	if strings.Contains(out, "web") {
		t.Errorf("web still listed:\n%s", out)
	}
}

func TestMCPListEmpty(t *testing.T) {
	withAPIProject(t, "")
	out := captureStdout(t, func() error { return MCP([]string{"list"}) })
	if !strings.Contains(out, "no MCP servers") {
		t.Errorf("list empty = %q", out)
	}
	// -tools with only disabled servers: no dials, no rows beyond header
	out = captureStdout(t, func() error { return MCP([]string{"add", "off", "--command", "x", "--enabled=false"}) })
	_ = out
	out = captureStdout(t, func() error { return MCP([]string{"list", "-tools"}) })
	if strings.Contains(out, "off\t") {
		t.Errorf("disabled server should not be dialed:\n%s", out)
	}
}

func TestMCPListToolsStdio(t *testing.T) {
	withAPIProject(t, "")
	dir := t.TempDir()
	srv := filepath.Join(dir, "fake.py")
	if err := os.WriteFile(srv, []byte(fakeMCPServerPY), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := MCP([]string{"add", "fake", "--command", "python3 " + srv}); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() error { return MCP([]string{"list", "-tools"}) })
	if !strings.Contains(out, "fake__echo") || !strings.Contains(out, "fake__boom") {
		t.Errorf("tools list = \n%s", out)
	}
	out = captureStdout(t, func() error { return MCP([]string{"list", "-tools", "-json"}) })
	var rows []struct {
		Server string   `json:"server"`
		Tools  []string `json:"tools"`
	}
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("bad json %q: %v", out, err)
	}
	if len(rows) != 1 || len(rows[0].Tools) != 2 {
		t.Errorf("rows = %+v", rows)
	}
}

// fakeMCPServerPY is a minimal MCP stdio server (also used by internal/mcp
// tests in spirit: newline JSON-RPC, initialize/tools/list/tools/call).
const fakeMCPServerPY = `#!/usr/bin/env python3
import sys, json
def send(obj):
    sys.stdout.write(json.dumps(obj) + "\n"); sys.stdout.flush()
for line in sys.stdin:
    line = line.strip()
    if not line: continue
    msg = json.loads(line)
    if "id" not in msg: continue
    method, id = msg.get("method"), msg["id"]
    if method == "initialize":
        send({"jsonrpc":"2.0","id":id,"result":{"protocolVersion":"2024-11-05","capabilities":{},"serverInfo":{"name":"fake","version":"0"}}})
    elif method == "tools/list":
        send({"jsonrpc":"2.0","id":id,"result":{"tools":[
            {"name":"echo","description":"echo back","inputSchema":{"type":"object"}},
            {"name":"boom","description":"fails","inputSchema":{"type":"object"}}]}})
    elif method == "tools/call":
        p = msg.get("params", {})
        if p.get("name") == "echo":
            send({"jsonrpc":"2.0","id":id,"result":{"content":[{"type":"text","text":"echo:"+p.get("arguments",{}).get("text","")}]}})
        else:
            send({"jsonrpc":"2.0","id":id,"result":{"content":[{"type":"text","text":"nope"}],"isError":True}})
    else:
        send({"jsonrpc":"2.0","id":id,"result":{}})
`
