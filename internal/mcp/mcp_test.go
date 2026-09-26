package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- fixtures -------------------------------------------------------------

const fakeServerPY = `#!/usr/bin/env python3
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
            {"name":"echo","description":"echo back","inputSchema":{"type":"object","properties":{"text":{"type":"string"}}}},
            {"name":"boom","description":"always fails","inputSchema":{"type":"object"}}]}})
    elif method == "tools/call":
        p = msg.get("params", {})
        if p.get("name") == "echo":
            send({"jsonrpc":"2.0","id":id,"result":{"content":[{"type":"text","text":"echo:"+p.get("arguments",{}).get("text","")}]}})
        else:
            send({"jsonrpc":"2.0","id":id,"result":{"content":[{"type":"text","text":"nope"}],"isError":True}})
    else:
        send({"jsonrpc":"2.0","id":id,"result":{}})
`

func writeFakeServer(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "fake_mcp.py")
	if err := os.WriteFile(path, []byte(fakeServerPY), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// --- config ---------------------------------------------------------------

func TestLoadSaveRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := Path(dir)
	if got, err := Load(path); err != nil || len(got) != 0 {
		t.Fatalf("missing file: %v, %d servers", err, len(got))
	}
	on := true
	servers := map[string]Server{
		"local":  {Type: "local", Command: []string{"python3", "srv.py"}, Environment: map[string]string{"K": "{env:V}"}, Enabled: &on},
		"remote": {Type: "remote", URL: "https://example.com/mcp", Enabled: nil},
	}
	if err := Save(path, servers); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "ENABLE") {
		t.Error("save wrote wrong shape")
	}
	var f struct {
		MCP map[string]Server `json:"mcp"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatalf("saved file is not mcp.json shaped: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["local"].Command[0] != "python3" {
		t.Errorf("reload = %+v", got)
	}
	if got["local"].IsEnabled() != true || got["remote"].IsEnabled() != false {
		t.Error("enabled posture wrong: nil must mean disabled")
	}
	// malformed json reports the file
	if err := os.WriteFile(path, []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Error("bad json = nil error")
	}
}

func TestExpandEnv(t *testing.T) {
	t.Setenv("MCP_TEST_KEY", "sekret")
	if v, err := expandEnv("{env:MCP_TEST_KEY}"); err != nil || v != "sekret" {
		t.Errorf("expand = %q, %v", v, err)
	}
	if v, err := expandEnv("plain"); err != nil || v != "plain" {
		t.Errorf("plain = %q, %v", v, err)
	}
	if _, err := expandEnv("{env:MCP_MISSING_VAR_XYZ}"); err == nil {
		t.Error("missing env = nil error")
	}
	if _, err := expandEnv("{env:BROKEN"); err == nil {
		t.Error("malformed = nil error")
	}
}

func TestServerTransportAndName(t *testing.T) {
	if (Server{URL: "https://x"}).Transport() != "http" {
		t.Error("url server should be http")
	}
	if (Server{Command: []string{"a"}}).Transport() != "stdio" {
		t.Error("command server should be stdio")
	}
	if ToolName("gh", "search") != "gh__search" {
		t.Errorf("ToolName = %s", ToolName("gh", "search"))
	}
	if Exists(t.TempDir()) {
		t.Error("Exists on empty dir should be false")
	}
}

// --- stdio transport ------------------------------------------------------

func TestStdioDialListCall(t *testing.T) {
	path := writeFakeServer(t)
	on := true
	srv := Server{Type: "local", Command: []string{"python3", path}, Enabled: &on}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	c, err := Dial(ctx, "fake", srv)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.Close()

	tools, err := c.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) != 2 || tools[0].Name != "echo" {
		t.Errorf("tools = %+v", tools)
	}

	text, err := c.CallTool(ctx, "echo", json.RawMessage(`{"text":"hi"}`))
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if text != "echo:hi" {
		t.Errorf("text = %q", text)
	}

	if _, err := c.CallTool(ctx, "boom", nil); err == nil {
		t.Error("isError tool = nil error")
	}
	if _, err := c.CallTool(ctx, "nope", nil); err == nil {
		t.Error("unknown tool = nil error (server dependent)")
	}

	// engine conversion: ids are namespaced
	eng, err := EngineTools(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if eng[0].ID != "fake__echo" {
		t.Errorf("engine id = %s", eng[0].ID)
	}
	res, err := eng[0].Execute(ctx, json.RawMessage(`{"text":"x"}`))
	if err != nil || res.Text != "echo:x" {
		t.Errorf("execute = %+v, %v", res, err)
	}
}

func TestStdioBadCommand(t *testing.T) {
	ctx := context.Background()
	if _, err := Dial(ctx, "x", Server{Type: "local"}); err == nil {
		t.Error("no command = nil error")
	}
	if _, err := Dial(ctx, "x", Server{Type: "local", Command: []string{"/no/such/bin"}}); err == nil {
		t.Error("missing binary = nil error")
	}
}

// --- http transport -------------------------------------------------------

func TestHTTPDialListCall(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "unauthorized", 401)
			return
		}
		var msg struct {
			ID     *int            `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&msg)
		w.Header().Set("Content-Type", "application/json")
		if msg.ID == nil { // notification
			w.WriteHeader(202)
			return
		}
		res := map[string]any{}
		switch msg.Method {
		case "initialize":
			res = map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}}
		case "tools/list":
			res = map[string]any{"tools": []map[string]any{{"name": "ping", "description": "pong"}}}
		case "tools/call":
			res = map[string]any{"content": []map[string]any{{"type": "text", "text": "pong!"}}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": res})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	on := true
	s := Server{Type: "remote", URL: srv.URL, Headers: map[string]string{"Authorization": "Bearer tok"}, Enabled: &on}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := Dial(ctx, "web", s)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.Close()
	tools, err := c.ListTools(ctx)
	if err != nil || len(tools) != 1 || tools[0].Name != "ping" {
		t.Fatalf("tools = %+v, %v", tools, err)
	}
	text, err := c.CallTool(ctx, "ping", nil)
	if err != nil || text != "pong!" {
		t.Errorf("call = %q, %v", text, err)
	}
}

func TestHTTPDialErrors(t *testing.T) {
	ctx := context.Background()
	if _, err := Dial(ctx, "x", Server{Type: "remote"}); err == nil {
		t.Error("no url = nil error")
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", 500)
	}))
	defer bad.Close()
	if _, err := Dial(ctx, "x", Server{Type: "remote", URL: bad.URL}); err == nil {
		t.Error("HTTP 500 = nil error")
	}
	// unauthorized header path
	denied := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", 401)
	}))
	defer denied.Close()
	if _, err := Dial(ctx, "x", Server{Type: "remote", URL: denied.URL}); err == nil {
		t.Error("HTTP 401 = nil error")
	}
}

// fakeCrashPY answers initialize and dies on its first incarnation;
// later incarnations stay up — exercises redial + backoff (row 43).
const fakeCrashPY = `#!/usr/bin/env python3
import sys, json, os
def send(obj):
    sys.stdout.write(json.dumps(obj) + "\n"); sys.stdout.flush()
state = sys.argv[1] if len(sys.argv) > 1 else ""
first = state and not os.path.exists(state)
if state:
    open(state, "w").close()
for line in sys.stdin:
    line = line.strip()
    if not line: continue
    msg = json.loads(line)
    if "id" not in msg: continue
    method, id = msg.get("method"), msg["id"]
    if method == "initialize":
        send({"jsonrpc":"2.0","id":id,"result":{"protocolVersion":"2024-11-05","capabilities":{},"serverInfo":{"name":"crash","version":"0"}}})
        if first:
            sys.exit(0)
    elif method == "tools/list":
        send({"jsonrpc":"2.0","id":id,"result":{"tools":[{"name":"stay","description":"alive","inputSchema":{"type":"object"}}]}})
    else:
        send({"jsonrpc":"2.0","id":id,"result":{}})
`

// TestStdioReconnectAfterCrash: the first server process dies right
// after initialize; the next request must redial (second incarnation
// stays alive) instead of failing.
func TestStdioReconnectAfterCrash(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "crash_mcp.py")
	if err := os.WriteFile(path, []byte(fakeCrashPY), 0o755); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(dir, "state")
	on := true
	srv := Server{Type: "local", Command: []string{"python3", path, state}, Enabled: &on}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c, err := Dial(ctx, "crash", srv)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.Close()

	tools, err := c.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools after crash (no redial?): %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "stay" {
		t.Errorf("tools = %+v", tools)
	}
	// The new incarnation keeps serving: a second exchange succeeds.
	if _, err := c.ListTools(ctx); err != nil {
		t.Fatalf("ListTools after reconnect: %v", err)
	}
}

// --- registry helpers -----------------------------------------------------

func TestLoadEnabledFiltersDisabled(t *testing.T) {
	path := writeFakeServer(t)
	on, off := true, false
	servers := map[string]Server{
		"run":     {Type: "local", Command: []string{"python3", path}, Enabled: &on},
		"stopped": {Type: "local", Command: []string{"python3", path}, Enabled: &off},
		"absent":  {Type: "local", Command: []string{"python3", path}}, // nil = off
		"broken":  {Type: "local", Command: []string{"/no/such/bin"}, Enabled: &on},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	clients, errs, err := LoadEnabled(ctx, t.TempDir(), servers)
	if err != nil {
		t.Fatal(err)
	}
	defer CloseAll(clients)
	if len(clients) != 1 || clients[0].Name != "run" {
		t.Errorf("clients = %+v", clients)
	}
	if len(errs) != 1 || errs["broken"] == "" {
		t.Errorf("errs = %v", errs)
	}
}
