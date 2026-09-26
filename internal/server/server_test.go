package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"matcode/internal/agents"
	"matcode/internal/config"
	"matcode/internal/store"
)

// fakeOpenAI speaks just enough of /chat/completions (SSE) for a turn.
func fakeOpenAI(t *testing.T, reply string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", reply)
		fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":3}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv
}

// newTestServer builds a Server rooted at a temp data tree wired to fake.
func newTestServer(t *testing.T, fake *httptest.Server) (*Server, string) {
	t.Helper()
	cwd := t.TempDir()
	data := filepath.Join(cwd, ".mtc")
	if err := os.MkdirAll(filepath.Join(data, "themes"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfgTOML := fmt.Sprintf(`agent = "build"
model = "mockt/t"
api_port = 8799

[providers.mockt]
base_url = %q
api_key = { env = "MOCK_KEY" }
`, fake.URL+"/v1")
	if err := os.WriteFile(filepath.Join(data, "config.toml"), []byte(cfgTOML), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "themes", "solar.json"),
		[]byte(`{"name":"solar"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MOCK_KEY", "x")
	srv, err := New(cwd)
	if err != nil {
		t.Fatal(err)
	}
	return srv, cwd
}

func get(t *testing.T, h http.Handler, path string) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	var m map[string]any
	body, _ := io.ReadAll(rec.Result().Body)
	_ = json.Unmarshal(body, &m)
	return rec.Code, m
}

func post(t *testing.T, h http.Handler, path, body string) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, req)
	var m map[string]any
	b, _ := io.ReadAll(rec.Result().Body)
	_ = json.Unmarshal(b, &m)
	return rec.Code, m
}

// postURL posts JSON at an absolute base URL (live httptest servers).
func postURL(t *testing.T, base, path, body string) (int, map[string]any) {
	t.Helper()
	resp, err := http.Post(base+path, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return resp.StatusCode, m
}

func TestReadEndpoints(t *testing.T) {
	srv, _ := newTestServer(t, fakeOpenAI(t, "hi"))
	h := srv.Handler()

	if code, m := get(t, h, "/api/health"); code != 200 || m["ok"] != true {
		t.Fatalf("health: %d %v", code, m)
	}
	code, cfg := get(t, h, "/api/config")
	if code != 200 || cfg["model"] != "mockt/t" || cfg["api_port"] != float64(8799) {
		t.Fatalf("config: %d %v", code, cfg)
	}
	// Secrets never appear: api_key_set is a bool, env name is exposed.
	prov := cfg["providers"].(map[string]any)["mockt"].(map[string]any)
	if prov["api_key_set"] != true || prov["api_key_env"] != "MOCK_KEY" {
		t.Fatalf("provider key flags: %v", prov)
	}
	// Every selectable agent (and nothing else) is published, so this stays
	// correct as the roster grows (row 32 added explore).
	if code, m := get(t, h, "/api/agent"); code != 200 ||
		len(m["agents"].([]any)) != len(agents.IDs()) {
		t.Fatalf("agents: %d %v, want %d", code, m, len(agents.IDs()))
	}
	if code, m := get(t, h, "/api/skill"); code != 200 {
		t.Fatalf("skills: %d %v", code, m)
	}
	code, tm := get(t, h, "/api/tool")
	if code != 200 || tm["agent"] != "build" {
		t.Fatalf("tools: %d %v", code, tm)
	}
	tools := tm["tools"].([]any)
	if len(tools) < 5 {
		t.Fatalf("builtin tools missing: %d", len(tools))
	}
	code, th := get(t, h, "/api/theme")
	if code != 200 || th["current"] != "default" {
		t.Fatalf("themes: %d %v", code, th)
	}
	found := false
	for _, n := range th["themes"].([]any) {
		if n == "solar" {
			found = true
		}
	}
	if !found {
		t.Fatalf("solar theme not listed: %v", th["themes"])
	}
	if code, m := get(t, h, "/api/mcp"); code != 200 {
		t.Fatalf("mcp: %d %v", code, m)
	}
}

func TestSessionLifecycle(t *testing.T) {
	srv, _ := newTestServer(t, fakeOpenAI(t, "hello from model"))
	h := srv.Handler()

	// create
	code, meta := post(t, h, "/api/session", `{"agent":"build"}`)
	if code != 201 {
		t.Fatalf("create: %d %v", code, meta)
	}
	id, _ := meta["id"].(string)
	if id == "" {
		t.Fatalf("no id: %v", meta)
	}

	// list
	code, list := get(t, h, "/api/session")
	if code != 200 || len(list["sessions"].([]any)) != 1 {
		t.Fatalf("list: %d %v", code, list)
	}

	// prompt (blocking) streams through the engine and replies
	code, pm := post(t, h, "/api/session/"+id+"/prompt", `{"text":"say hi"}`)
	if code != 200 {
		t.Fatalf("prompt: %d %v", code, pm)
	}
	if pm["reply"] != "hello from model" {
		t.Fatalf("reply = %v", pm["reply"])
	}
	usage := pm["usage"].(map[string]any)
	if usage["input"] != float64(7) || usage["output"] != float64(3) {
		t.Fatalf("usage = %v", usage)
	}

	// transcript now holds user + assistant
	code, tx := get(t, h, "/api/session/"+id+"/message")
	if code != 200 {
		t.Fatalf("messages: %d", code)
	}
	msgs := tx["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("want 2 messages, got %d", len(msgs))
	}

	// context shows the assembled payload
	code, ctxm := get(t, h, "/api/session/"+id+"/context")
	if code != 200 {
		t.Fatalf("context: %d %v", code, ctxm)
	}
	if ctxm["system"] == "" || ctxm["tokens"].(float64) <= 0 {
		t.Fatalf("context payload: %v", ctxm)
	}
	if len(ctxm["messages"].([]any)) != 2 {
		t.Fatalf("context messages: %v", ctxm["messages"])
	}

	// compact folds the transcript (deterministic, no model call)
	code, cm := post(t, h, "/api/session/"+id+"/compact", ``)
	if code != 200 {
		t.Fatalf("compact: %d %v", code, cm)
	}

	// errors: unknown session, missing text, bad JSON
	if code, _ := get(t, h, "/api/session/ses_nope/message"); code != 404 {
		t.Fatalf("unknown session: %d", code)
	}
	if code, _ := post(t, h, "/api/session/"+id+"/prompt", `{}`); code != 400 {
		t.Fatalf("empty prompt: %d", code)
	}
	if code, _ := post(t, h, "/api/session/"+id+"/prompt", `{oops`); code != 400 {
		t.Fatalf("bad json: %d", code)
	}
	if code, _ := post(t, h, "/api/session", `{"agent":"ghost"}`); code != 400 {
		t.Fatalf("bad agent: %d", code)
	}
	// traversal guard
	if code, _ := get(t, h, "/api/session/..%2F..%2Fetc/message"); code == 200 {
		t.Fatal("traversal accepted")
	}
}

// TestEventStream asserts the SSE endpoint carries a turn's milestones to a
// concurrent subscriber — the one stream rule (§9).
func TestEventStream(t *testing.T) {
	srv, _ := newTestServer(t, fakeOpenAI(t, "streamed"))
	h := httptest.NewServer(srv.Handler())
	defer h.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Open the SSE stream first so no event is missed.
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, h.URL+"/api/event", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type = %q", ct)
	}

	events := make(chan map[string]any, 16)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			line := sc.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var m map[string]any
			if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &m) == nil {
				events <- m
			}
		}
		close(events)
	}()

	// hello arrives without any request.
	select {
	case m := <-events:
		if m["ok"] != true {
			t.Fatalf("hello = %v", m)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no hello event")
	}

	// Create + prompt; the turn must produce text/done on the stream.
	code, meta := postURL(t, h.URL, "/api/session", `{}`)
	if code != 201 {
		t.Fatalf("create: %d", code)
	}
	id := meta["id"].(string)
	if code, pm := postURL(t, h.URL, "/api/session/"+id+"/prompt", `{"text":"hi"}`); code != 200 {
		t.Fatalf("prompt: %d %v", code, pm)
	}

	sawText, sawDone, sawSession := false, false, false
	deadline := time.After(5 * time.Second)
	for !(sawText && sawDone && sawSession) {
		select {
		case m, ok := <-events:
			if !ok {
				t.Fatalf("stream closed: text=%v done=%v session=%v", sawText, sawDone, sawSession)
			}
			switch m["type"] {
			case "text":
				sawText = m["text"] == "streamed"
			case "done":
				sawDone = true
			case "session":
				sawSession = true
			}
		case <-deadline:
			t.Fatalf("missing events: text=%v done=%v session=%v", sawText, sawDone, sawSession)
		}
	}
}

// TestServeListen binds a real port, answers /api/health, and shuts down on
// context cancel — `mtc serve`'s core loop.
func TestServeListen(t *testing.T) {
	srv, _ := newTestServer(t, fakeOpenAI(t, "x"))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- srv.Listen(ctx, "127.0.0.1", 18787) }()

	url := "http://127.0.0.1:18787"
	deadline := time.Now().Add(3 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := http.Get(url + "/api/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				lastErr = nil
				break
			}
		}
		lastErr = err
		time.Sleep(50 * time.Millisecond)
	}
	if lastErr != nil {
		t.Fatalf("server never came up: %v", lastErr)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not shut down on ctx cancel")
	}
}

// compile-time: config import stays used when helpers shift.
var _ = config.Load
var _ = store.List

// TestAgentsEndpointWithOverlay proves row 32 reaches the API: file-defined
// agents appear with their frontmatter model/description/steps, hidden and
// disabled agents stay out, and a session inherits the agent's model.
func TestAgentsEndpointWithOverlay(t *testing.T) {
	srv, cwd := newTestServer(t, fakeOpenAI(t, "hi"))
	t.Cleanup(func() { _ = agents.Load() })
	agentsDir := filepath.Join(cwd, ".mtc", "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentsDir, "reviewer.md"),
		[]byte("---\nmodel: mockt/reviewer\ndescription: reviews diffs\nsteps: 4\n---\nReview."), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentsDir, "ghost.md"),
		[]byte("---\nhidden: true\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentsDir, "dead.md"),
		[]byte("---\ndisabled: true\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Reload the registry against the new files.
	if err := agents.Load(srv.Cfg.AgentsDirs()...); err != nil {
		t.Fatal(err)
	}

	h := srv.Handler()
	code, m := get(t, h, "/api/agent")
	if code != 200 {
		t.Fatalf("agents: %d", code)
	}
	list, _ := m["agents"].([]any)
	byID := map[string]map[string]any{}
	for _, e := range list {
		e := e.(map[string]any)
		byID[e["id"].(string)] = e
	}
	r, ok := byID["reviewer"]
	if !ok {
		t.Fatalf("reviewer missing from %v", list)
	}
	if r["model"] != "mockt/reviewer" || r["description"] != "reviews diffs" {
		t.Errorf("reviewer = %v", r)
	}
	if r["steps"] != float64(4) {
		t.Errorf("steps = %v", r["steps"])
	}
	if _, ok := byID["ghost"]; ok {
		t.Error("hidden agent leaked into /api/agent")
	}
	if _, ok := byID["dead"]; ok {
		t.Error("disabled agent leaked into /api/agent")
	}
	if byID["build"]["default"] != true {
		t.Error("build should be the default agent")
	}

	// A session created for reviewer inherits the agent's model.
	code, m = post(t, h, "/api/session", `{"agent":"reviewer"}`)
	if code != 201 {
		t.Fatalf("session: %d %v", code, m)
	}
	meta, _ := m["model"].(string)
	if meta != "mockt/reviewer" {
		t.Errorf("session model = %q, want the agent's model", meta)
	}
}
