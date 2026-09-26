// Package server is matcode's local HTTP + SSE API (spec §9): one engine
// behind URL shapes OpenCode scripts already know, so `mtc run`, the TUI,
// and any HTTP client see identical events.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"matcode/internal/agents"
	"matcode/internal/app"
	"matcode/internal/config"
	"matcode/internal/engine"
	"matcode/internal/mcp"
	"matcode/internal/plugin"
	"matcode/internal/references"
	"matcode/internal/skills"
	"matcode/internal/store"
	"matcode/internal/tools"
)

// hub fans one engine's events out to every SSE subscriber. Publishing
// never blocks: a slow subscriber drops events rather than the turn.
type hub struct {
	mu   sync.Mutex
	subs map[int]chan []byte
	next int
}

func newHub() *hub { return &hub{subs: map[int]chan []byte{}} }

func (h *hub) publish(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, ch := range h.subs {
		select {
		case ch <- b:
		default:
		}
	}
}

func (h *hub) subscribe() (int, <-chan []byte, func()) {
	ch := make(chan []byte, 256)
	h.mu.Lock()
	id := h.next
	h.next++
	h.subs[id] = ch
	h.mu.Unlock()
	return id, ch, func() {
		h.mu.Lock()
		delete(h.subs, id)
		h.mu.Unlock()
		close(ch)
	}
}

// Server serves the API for one data tree / working directory.
type Server struct {
	Cfg *config.Config
	Cwd string
	Hub *hub

	// Plugins is the hook host (§8): one per server, shared by every
	// request and closed when Listen returns.
	Plugins *plugin.Host

	// turn serializes prompt turns: one engine at a time, matching the
	// single-user local scope (§11).
	turn sync.Mutex
}

// New assembles a server for cwd's config.
func New(cwd string) (*Server, error) {
	cfg, err := config.Load(cwd)
	if err != nil {
		return nil, err
	}
	// Agent .md overlays are read once per server; `mtc serve` reloads by
	// restarting (the TUI hot-reloads them instead).
	if err := agents.Load(cfg.AgentsDirs()...); err != nil {
		return nil, err
	}
	return &Server{
		Cfg: cfg, Cwd: cwd, Hub: newHub(),
		Plugins: plugin.Open(cfg.PluginsDirs(), filepath.Join(cfg.DataDir(), "logs")),
	}, nil
}

// Handler builds the route table (Go method+wildcard patterns).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("GET /api/config", s.getConfig)
	mux.HandleFunc("GET /api/agent", s.getAgents)
	mux.HandleFunc("GET /api/skill", s.getSkills)
	mux.HandleFunc("GET /api/tool", s.getTools)
	mux.HandleFunc("GET /api/theme", s.getThemes)
	mux.HandleFunc("GET /api/mcp", s.getMCP)
	mux.HandleFunc("POST /api/session", s.postSession)
	mux.HandleFunc("GET /api/session", s.listSessions)
	mux.HandleFunc("GET /api/session/{id}", s.getSession)
	mux.HandleFunc("GET /api/session/{id}/message", s.getMessages)
	mux.HandleFunc("POST /api/session/{id}/prompt", s.postPrompt)
	mux.HandleFunc("POST /api/session/{id}/compact", s.postCompact)
	mux.HandleFunc("GET /api/session/{id}/context", s.getContext)
	mux.HandleFunc("GET /api/event", s.getEvent)
	return mux
}

// Listen opens the API on 127.0.0.1 (env-only auth scope: the local port IS
// the credential) and serves until ctx is cancelled.
func (s *Server) Listen(ctx context.Context, host string, port int) error {
	if host == "" {
		host = "127.0.0.1"
	}
	if port <= 0 {
		port = s.Cfg.APIPort
	}
	if port <= 0 {
		port = 8787
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return err
	}
	defer func() {
		if s.Plugins != nil {
			s.Plugins.Close()
		}
	}()
	srv := &http.Server{Handler: s.Handler()}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()
	fmt.Fprintf(os.Stderr, "mtc serve listening on http://%s\n", ln.Addr().String())
	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
		return nil
	case err := <-errCh:
		return err
	}
}

// --- helpers ---------------------------------------------------------------

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

// readBody decodes a small JSON object; empty body yields an empty map.
func readBody(r *http.Request) (map[string]any, error) {
	m := map[string]any{}
	if r.Body == nil {
		return m, nil
	}
	b := make([]byte, 1<<20)
	n, _ := r.Body.Read(b)
	if n == 0 {
		return m, nil
	}
	if err := json.Unmarshal(b[:n], &m); err != nil {
		return nil, fmt.Errorf("invalid JSON body: %w", err)
	}
	return m, nil
}

// openSession resolves an id to its folder with the traversal guard.
func (s *Server) openSession(id string) (*store.Session, error) {
	dir, err := sessionDir(s.Cfg, id)
	if err != nil {
		return nil, err
	}
	return store.Open(dir)
}

func sessionDir(cfg *config.Config, id string) (string, error) {
	if id == "" || strings.ContainsAny(id, "/\\") || strings.Contains(id, "..") {
		return "", fmt.Errorf("invalid session id %q", id)
	}
	dir := filepath.Join(cfg.SessionsDir(), id)
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return "", fmt.Errorf("session %q not found", id)
	}
	return dir, nil
}

// systemFor rebuilds the system prompt for an agent without touching a
// provider: agent body + env line + AGENTS.md + references block +
// skill guidance (§11 pipeline order).
func (s *Server) systemFor(agentID string) (string, *skills.Set, error) {
	ag, err := agents.Get(agentID)
	if err != nil {
		return "", nil, err
	}
	system := ag.System + "\n\n" + engine.EnvLine(s.Cwd)
	if ag.UseInstructions {
		instructions, _ := s.Cfg.Find("AGENTS.md")
		system += "\n\n" + engine.LoadInstructions(instructions)
	}
	if !ag.Stateless {
		references.Sync(s.Cfg.DataDir(), s.Cfg.References)
		system += references.Block(s.Cfg.References)
	}
	set, err := app.SkillsFor(s.Cfg, nil)
	if err != nil {
		return "", nil, err
	}
	if g := set.Guidance(); g != "" {
		system += "\n\n" + g
	}
	return system, set, nil
}

// --- read endpoints --------------------------------------------------------

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) getConfig(w http.ResponseWriter, _ *http.Request) {
	cfg := s.Cfg
	providers := map[string]any{}
	for name, p := range cfg.Providers {
		_, keySet := "", false
		if p.APIKey.Env != "" {
			_, keySet = os.LookupEnv(p.APIKey.Env)
		}
		providers[name] = map[string]any{
			"dialect": p.Dialect, "base_url": p.BaseURL,
			"api_key_env": p.APIKey.Env, "api_key_set": keySet,
			"default_model": p.DefaultModel,
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"model": cfg.Model, "agent": cfg.Agent,
		"formatter": cfg.Formatter, "snapshots": cfg.Snapshots,
		"auto_compact": cfg.AutoCompact, "context_limit": cfg.ContextLimit,
		"api_port":   cfg.APIPort,
		"media":      cfg.Media,
		"providers":  providers,
		"global_dir": cfg.GlobalDir, "project_dir": cfg.ProjectDir,
		"data_dir": cfg.DataDir(),
	})
}

func (s *Server) getAgents(w http.ResponseWriter, _ *http.Request) {
	out := []map[string]any{}
	for _, id := range agents.IDs() {
		ag, err := agents.Get(id)
		if err != nil {
			continue
		}
		out = append(out, map[string]any{
			"id": id, "tools": ag.ToolIDs,
			"stateless": ag.Stateless, "bypass_permissions": ag.BypassPermissions,
			"instructions": ag.UseInstructions,
			"default":      id == defaultAgent(s.Cfg),
			"model":        ag.Model, "description": ag.Description,
			"steps": ag.Steps, "color": ag.Color,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": out})
}

func defaultAgent(cfg *config.Config) string {
	if cfg.Agent != "" {
		return cfg.Agent
	}
	return "build"
}

func (s *Server) getSkills(w http.ResponseWriter, _ *http.Request) {
	set, err := app.SkillsFor(s.Cfg, nil)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	out := []map[string]any{}
	for _, sk := range set.List() {
		out = append(out, map[string]any{
			"id": sk.ID, "name": sk.Name, "description": sk.Description,
			"dir": sk.Dir, "advertised": sk.Advertised,
			"loaded": contains(set.LoadedIDs(), sk.ID),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"skills": out})
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func (s *Server) getTools(w http.ResponseWriter, r *http.Request) {
	// Tools are agent-scoped: ?agent= narrows the list.
	agentID := defaultAgent(s.Cfg)
	if q := r.URL.Query().Get("agent"); q != "" {
		agentID = q
	}
	writeJSON(w, http.StatusOK, s.toolList(agentID))
}

func (s *Server) toolList(agentID string) map[string]any {
	ag, err := agents.Get(agentID)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	set, err := app.SkillsFor(s.Cfg, nil)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	out := []map[string]any{}
	for _, t := range ag.Tools(s.Cwd, s.Cfg.DataDir(), s.Cfg.Formatter, set,
		tools.User(s.Cwd, s.Cfg.ToolsDirs()...)) {
		out = append(out, map[string]any{
			"id": t.ID, "description": t.Description, "schema": t.Schema,
		})
	}
	return map[string]any{"agent": agentID, "tools": out}
}

func (s *Server) getThemes(w http.ResponseWriter, _ *http.Request) {
	// One JSON per theme (data tree); the loader itself lands with the TUI.
	names := []string{"default"}
	root := filepath.Join(s.Cfg.DataDir(), "themes")
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		names = append(names, strings.TrimSuffix(e.Name(), ".json"))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"themes": names, "current": s.theme(), "dir": root,
	})
}

func (s *Server) getMCP(w http.ResponseWriter, _ *http.Request) {
	servers, err := mcp.Load(mcp.Path(s.Cfg.DataDir()))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	out := []map[string]any{}
	for name, srv := range servers {
		// Environment *names* only — keys, never values: a hand-written
		// literal in mcp.json must not leak through this endpoint.
		env := []string{}
		for k := range srv.Environment {
			env = append(env, k)
		}
		out = append(out, map[string]any{
			"name": name, "transport": srv.Transport(),
			"command": srv.Command, "url": srv.URL,
			"enabled": srv.IsEnabled(), "env": env,
			"headers": len(srv.Headers),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"mcp": out})
}

// --- session endpoints -----------------------------------------------------

func (s *Server) postSession(w http.ResponseWriter, r *http.Request) {
	body, err := readBody(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	agentID := str(body, "agent", defaultAgent(s.Cfg))
	if _, err := agents.Get(agentID); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	// An agent's frontmatter model wins over the config default; an
	// explicit body model wins over both.
	model := agents.ModelFor(agentID, s.Cfg.Model)
	if v := str(body, "model", ""); v != "" {
		model = v
	}
	if model == "" {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("no model configured"))
		return
	}
	sess, err := store.Create(s.Cfg.SessionsDir(), model)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	sess.Meta.Agent = agentID
	if err := sess.Save(); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	s.Hub.publish(map[string]any{
		"type": "session", "session": sess.Meta.ID, "agent": agentID, "model": model,
	})
	writeJSON(w, http.StatusCreated, sess.Meta)
}

func (s *Server) listSessions(w http.ResponseWriter, _ *http.Request) {
	metas, err := store.List(s.Cfg.SessionsDir())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": metas})
}

func (s *Server) getSession(w http.ResponseWriter, r *http.Request) {
	sess, err := s.openSession(r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, sess.Meta)
}

func (s *Server) getMessages(w http.ResponseWriter, r *http.Request) {
	sess, err := s.openSession(r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	msgs, err := sess.Messages()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": msgs})
}

func (s *Server) getContext(w http.ResponseWriter, r *http.Request) {
	sess, err := s.openSession(r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	agentID := sess.Meta.Agent
	if agentID == "" {
		agentID = defaultAgent(s.Cfg)
	}
	sys, _, err := s.systemFor(agentID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	msgs, err := sess.Messages()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	system, live, tokens := engine.ContextPreview(sys, msgs)
	// session.context (§8): preview exactly what the next request would
	// send, plugin lines included.
	if s.Plugins != nil {
		if lines, ok := s.Plugins.SessionContext(r.Context(), sess.Meta.ID); ok {
			system += "\n\n" + lines
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"session": sess.Meta.ID, "agent": agentID,
		"system": system, "messages": live, "tokens": tokens,
	})
}

// --- prompt + compaction ---------------------------------------------------

func (s *Server) postPrompt(w http.ResponseWriter, r *http.Request) {
	body, err := readBody(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	text := str(body, "text", "")
	if text == "" {
		text = str(body, "message", "")
	}
	if strings.TrimSpace(text) == "" {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("missing text"))
		return
	}
	sess, err := s.openSession(r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	agentID := sess.Meta.Agent
	if v := str(body, "agent", ""); v != "" {
		agentID = v
	}
	if agentID == "" {
		agentID = defaultAgent(s.Cfg)
	}
	ag, err := agents.Get(agentID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if ag.Stateless {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("agent %q is stateless: sessions need a stateful agent", agentID))
		return
	}
	model := agents.ModelFor(agentID, s.Cfg.Model)
	if v := str(body, "model", ""); v != "" {
		model = v
	}

	// Serialize turns: the engine is stateful across a prompt.
	s.turn.Lock()
	defer s.turn.Unlock()

	sid := sess.Meta.ID
	var reply strings.Builder
	built, err := app.New(r.Context(), app.Options{
		Cwd: s.Cwd, Config: s.Cfg, Agent: agentID, Model: model,
		Plugins: s.Plugins,
		Echo: func(t string) {
			reply.WriteString(t)
			s.Hub.publish(map[string]any{"type": "text", "session": sid, "text": t})
		},
		Emit: func(ev engine.Event) {
			m := map[string]any{"session": sid}
			b, _ := json.Marshal(ev)
			var em map[string]any
			_ = json.Unmarshal(b, &em)
			for k, v := range em {
				m[k] = v
			}
			if m["type"] == nil {
				m["type"] = ev.Type
			}
			s.Hub.publish(m)
		},
	})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	defer built.Close()
	built.UseSession(sess)

	if sess.Meta.Agent != agentID || sess.Meta.Model != built.Model {
		sess.Meta.Agent = agentID
		sess.Meta.Model = built.Model
		if err := sess.Save(); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
	}

	if err := built.Engine.Turn(r.Context(), text); err != nil {
		s.Hub.publish(map[string]any{"type": "error", "session": sid, "error": err.Error()})
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	usage := engine.UsageEvent{
		Input:  built.Engine.Usage.Input,
		Output: built.Engine.Usage.Output,
		Cost:   built.Engine.Usage.CostUSD,
	}
	s.Hub.publish(map[string]any{
		"type": "done", "session": sid, "usage": usage,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"session": sid, "reply": reply.String(), "usage": usage,
	})
}

func (s *Server) postCompact(w http.ResponseWriter, r *http.Request) {
	sess, err := s.openSession(r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	agentID := sess.Meta.Agent
	if agentID == "" {
		agentID = defaultAgent(s.Cfg)
	}
	s.turn.Lock()
	defer s.turn.Unlock()
	built, err := app.New(r.Context(), app.Options{
		Cwd: s.Cwd, Config: s.Cfg, Agent: agentID, Model: s.Cfg.Model,
		Plugins: s.Plugins,
		Emit: func(ev engine.Event) {
			s.Hub.publish(map[string]any{"type": ev.Type, "session": sess.Meta.ID, "text": ev.Text})
		},
	})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	defer built.Close()
	built.UseSession(sess)
	if err := built.Engine.Compact(); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	msgs, _ := sess.Messages()
	live := 0
	for _, m := range msgs {
		if m.Role == "compaction" {
			continue
		}
		live++
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"session": sess.Meta.ID, "messages": len(msgs), "live": live,
	})
}

// --- SSE -------------------------------------------------------------------

func (s *Server) getEvent(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("streaming unsupported"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	_, ch, cancel := s.Hub.subscribe()
	defer cancel()
	// Announce the stream so clients know they are connected.
	fmt.Fprintf(w, "event: hello\ndata: {\"ok\":true}\n\n")
	fl.Flush()

	keep := time.NewTicker(15 * time.Second)
	defer keep.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-keep.C:
			fmt.Fprint(w, ": keepalive\n\n")
			fl.Flush()
		case b, open := <-ch:
			if !open {
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", b)
			fl.Flush()
		}
	}
}

func isFile(name string) bool {
	st, err := os.Stat(name)
	return err == nil && st.Mode().IsRegular()
}

func (s *Server) theme() string {
	if t, ok := s.Cfg.Find("theme"); ok {
		if b, err := os.ReadFile(t); err == nil {
			var v struct {
				Theme string `json:"theme"`
			}
			if json.Unmarshal(b, &v) == nil && v.Theme != "" {
				return v.Theme
			}
		}
	}
	if isFile(filepath.Join(s.Cfg.DataDir(), "themes", "default.json")) {
		return "default"
	}
	return "default"
}

// str reads a string field with a default.
func str(m map[string]any, key, def string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return def
}
