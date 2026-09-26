// Package mcp implements the Model Context Protocol client: mcp.json
// config, stdio and streamable-HTTP transports, and the `server__tool`
// namespacing that joins remote tools into the registry (spec §7).
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Server is one configured MCP server in mcp.json. Secrets appear only as
// {env:VAR} references and are expanded at dial time.
type Server struct {
	Type        string            `json:"type,omitempty"` // "local" (stdio), "remote" (HTTP), "sse" (legacy)
	Command     []string          `json:"command,omitempty"`
	Environment map[string]string `json:"environment,omitempty"`
	URL         string            `json:"url,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	Enabled     *bool             `json:"enabled,omitempty"` // nil = disabled (rice posture)
	// LogDir (never serialized — set by the loader) redirects stdio
	// stderr to <LogDir>/mcp-<name>.log (§7 per-server logs).
	LogDir string `json:"-"`
}

// IsEnabled reports the effective posture: nothing extra runs until asked.
func (s Server) IsEnabled() bool { return s.Enabled != nil && *s.Enabled }

// Transport is "stdio", "http" (streamable), or "sse" (legacy).
func (s Server) Transport() string {
	switch s.Type {
	case "sse":
		return "sse"
	case "remote":
		return "http"
	}
	if s.URL != "" {
		return "http"
	}
	return "stdio"
}

// file is the on-disk shape of mcp.json: {"mcp": {name: server}}.
type file struct {
	Servers map[string]Server `json:"mcp"`
}

// Load reads mcp.json. A missing file yields an empty registry, not an error.
func Load(path string) (map[string]Server, error) {
	out := map[string]Server{}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, err
	}
	var f file
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for name, s := range f.Servers {
		out[name] = s
	}
	return out, nil
}

// Save writes the whole registry back to mcp.json (pretty, stable order).
func Save(path string, servers map[string]Server) error {
	names := make([]string, 0, len(servers))
	for n := range servers {
		names = append(names, n)
	}
	for i := 1; i < len(names); i++ {
		for j := i; j > 0 && names[j] < names[j-1]; j-- {
			names[j], names[j-1] = names[j-1], names[j]
		}
	}
	ordered := make(map[string]Server, len(servers))
	for _, n := range names {
		ordered[n] = servers[n]
	}
	b, err := json.MarshalIndent(file{Servers: ordered}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// Tool is one tool advertised by a server.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"inputSchema,omitempty"`
}

// Client is a live connection to one server.
type Client struct {
	Name string
	srv  Server

	cmd     *exec.Cmd
	in      io.WriteCloser
	lines   chan string // incoming JSON-RPC responses (stdio lines or SSE messages)
	logFile *os.File    // stdio stderr destination (row 46), nil = discarded

	http    *http.Client
	hdr     http.Header // request headers for HTTP mode
	session string      // Mcp-Session-Id for streamable HTTP

	// Legacy SSE transport (§7): where client messages are POSTed —
	// announced by the stream's endpoint event, else the server URL.
	purlMu sync.Mutex
	purl   string

	seq int
	mu  sync.Mutex // one in-flight request at a time

	// dead marks a lost transport; the next request redials with
	// backoff (§7 lifecycle). closed forbids any reconnect.
	dead   atomic.Bool
	closed atomic.Bool
	// baseCtx outlives per-call contexts so the legacy SSE stream stays
	// open; cancelled by Close.
	baseCtx    context.Context
	baseCancel context.CancelFunc
	stop       chan struct{}
	// streamCtx/streamCancel bound the current legacy SSE GET; cancelled
	// on resetTransport so a redial replaces the stream.
	streamCtx    context.Context
	streamCancel context.CancelFunc

	// OnToolsChanged fires (asynchronously) when the server sends
	// notifications/tools_list_changed; the app layer re-lists and swaps
	// the registry (§7 live registry swap). nil disables it.
	OnToolsChanged func()
}

// Dial connects to a server, performing the MCP initialize handshake.
// Stdio servers spawn their command; remote servers POST to their URL.
func Dial(ctx context.Context, name string, srv Server) (*Client, error) {
	c := &Client{Name: name, srv: srv, stop: make(chan struct{})}
	c.baseCtx, c.baseCancel = context.WithCancel(context.WithoutCancel(ctx))
	var err error
	if err = c.transportDial(ctx); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("mcp %s: %w", name, err)
	}
	if err = c.handshake(ctx); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("mcp %s: %w", name, err)
	}
	return c, nil
}

// transportDial opens the transport for the configured kind (stdio,
// streamable HTTP, or legacy SSE). Safe to call again on redial.
func (c *Client) transportDial(ctx context.Context) error {
	switch c.srv.Transport() {
	case "stdio":
		return c.dialStdio(ctx)
	case "sse":
		return c.dialSSE(ctx)
	default:
		return c.dialHTTP(ctx)
	}
}

// handshake runs the MCP initialize exchange on a fresh transport.
func (c *Client) handshake(ctx context.Context) error {
	params := map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "mtc", "version": "0.0.1"},
	}
	if _, err := c.requestLocked(ctx, "initialize", params); err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	// Best-effort initialized notification (no response expected).
	_ = c.notify(ctx, "notifications/initialized", map[string]any{})
	return nil
}

func (c *Client) dialStdio(ctx context.Context) error {
	if len(c.srv.Command) == 0 {
		return fmt.Errorf("no command configured")
	}
	cmd := exec.Command(c.srv.Command[0], c.srv.Command[1:]...)
	cmd.Env = os.Environ()
	for k, v := range c.srv.Environment {
		val, err := expandEnv(v)
		if err != nil {
			return err
		}
		cmd.Env = append(cmd.Env, k+"="+val)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	// stderr goes to the server's log file (§7 per-server logs, row 46)
	// when a log dir is configured; otherwise it is discarded.
	var logf *os.File
	if c.srv.LogDir != "" {
		if err := os.MkdirAll(c.srv.LogDir, 0o755); err == nil {
			path := filepath.Join(c.srv.LogDir, "mcp-"+sanitizeName(c.Name)+".log")
			if f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
				fmt.Fprintf(f, "=== %s dial %s ===\n", time.Now().Format(time.RFC3339), c.Name)
				logf = f
				cmd.Stderr = f
			}
		}
	}
	if cmd.Stderr == nil {
		cmd.Stderr = io.Discard
	}
	if err := cmd.Start(); err != nil {
		if logf != nil {
			_ = logf.Close()
		}
		return err
	}
	c.cmd, c.in, c.logFile = cmd, stdin, logf
	c.lines = make(chan string, 8)
	go c.readLoop(stdout, c.lines)
	return nil
}

func (c *Client) dialHTTP(ctx context.Context) error {
	if c.srv.URL == "" {
		return fmt.Errorf("no url configured")
	}
	hdr := http.Header{}
	hdr.Set("Content-Type", "application/json")
	hdr.Set("Accept", "application/json, text/event-stream")
	for k, v := range c.srv.Headers {
		val, err := expandEnv(v)
		if err != nil {
			return err
		}
		hdr.Set(k, val)
	}
	c.http = &http.Client{Timeout: 60 * time.Second}
	c.hdr = hdr
	c.session = ""
	return nil
}

// dialSSE opens the legacy HTTP+SSE transport (§7): a long-lived GET
// stream carries server messages; client messages POST to the endpoint
// the stream announces.
func (c *Client) dialSSE(ctx context.Context) error {
	if c.srv.URL == "" {
		return fmt.Errorf("no url configured")
	}
	hdr := http.Header{}
	hdr.Set("Content-Type", "application/json")
	hdr.Set("Accept", "text/event-stream")
	for k, v := range c.srv.Headers {
		val, err := expandEnv(v)
		if err != nil {
			return err
		}
		hdr.Set(k, val)
	}
	// No client timeout: the GET stream is long-lived; per-call
	// contexts bound the POSTs.
	c.http = &http.Client{}
	c.hdr = hdr
	c.session = ""
	c.setPostURL(c.srv.URL)
	c.lines = make(chan string, 8)
	c.streamCtx, c.streamCancel = context.WithCancel(c.baseCtx)
	go c.sseLoop(c.streamCtx)
	return nil
}

// sseLoop pumps the legacy event stream: endpoint events retarget the
// POST URL, message events deliver JSON-RPC payloads. EOF kills the
// connection (marked dead unless a redial or Close replaced it).
func (c *Client) sseLoop(streamCtx context.Context) {
	ch := c.lines
	defer func() {
		if streamCtx.Err() == nil && !c.closed.Load() {
			c.dead.Store(true)
		}
		close(ch)
	}()
	req, err := http.NewRequestWithContext(streamCtx, http.MethodGet, c.srv.URL, nil)
	if err != nil {
		return
	}
	for k, vals := range c.hdr {
		for _, v := range vals {
			req.Header.Add(k, v)
		}
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	var event, data string
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			data = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		case line == "":
			if data != "" {
				c.sseDispatch(event, data, ch)
			}
			event, data = "", ""
		}
	}
}

// sseDispatch routes one SSE frame: endpoint → POST target, message →
// notification hook + response channel.
func (c *Client) sseDispatch(event, data string, ch chan string) {
	switch event {
	case "endpoint":
		if base, err := url.Parse(c.srv.URL); err == nil {
			if ref, err := url.Parse(data); err == nil {
				c.setPostURL(base.ResolveReference(ref).String())
			}
		}
		return
	case "", "message":
	default:
		return
	}
	c.noteToolsChanged(data)
	var probe struct {
		ID *int `json:"id"`
	}
	if json.Unmarshal([]byte(data), &probe) == nil && probe.ID != nil {
		select {
		case ch <- data:
		case <-c.stop:
		}
	}
}

// readLoop delivers stdio responses and fires the tools-changed hook on
// notifications (§7 live registry swap); response-less lines are never
// queued.
func (c *Client) readLoop(r io.Reader, ch chan string) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		c.noteToolsChanged(line)
		var probe struct {
			ID *int `json:"id"`
		}
		if json.Unmarshal([]byte(line), &probe) == nil && probe.ID == nil {
			continue // notifications never answer a request
		}
		select {
		case ch <- line:
		case <-c.stop:
			close(ch)
			return
		}
	}
	close(ch)
}

// noteToolsChanged fires OnToolsChanged for a tools_list_changed
// notification. The hook runs in its own goroutine because it issues a
// tools/list, which takes the request mutex.
func (c *Client) noteToolsChanged(line string) {
	h := c.OnToolsChanged
	if h == nil {
		return
	}
	var note struct {
		ID     *int   `json:"id"`
		Method string `json:"method"`
	}
	if json.Unmarshal([]byte(line), &note) != nil {
		return
	}
	if note.ID != nil || note.Method != "notifications/tools_list_changed" {
		return
	}
	go h()
}

// postURL is the legacy SSE POST target (endpoint event, else the URL).
func (c *Client) postURL() string {
	c.purlMu.Lock()
	defer c.purlMu.Unlock()
	return c.purl
}

func (c *Client) setPostURL(u string) {
	c.purlMu.Lock()
	c.purl = u
	c.purlMu.Unlock()
}

// sanitizeName makes a server name file-safe for its log file.
func sanitizeName(name string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			return r
		default:
			return '_'
		}
	}, name)
}

// LogPath is the per-server stderr log (§7): <dataDir>/logs/mcp-<name>.log.
// The dial writes it; the TUI /mcp dialog reads it (row 46).
func LogPath(dataDir, name string) string {
	return filepath.Join(dataDir, "logs", "mcp-"+sanitizeName(name)+".log")
}

func (c *Client) notify(ctx context.Context, method string, params any) error {
	msg := map[string]any{"jsonrpc": "2.0", "method": method, "params": params}
	if c.http != nil {
		_, err := c.post(ctx, msg)
		return err
	}
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	_, err = c.in.Write(append(b, '\n'))
	return err
}

// request runs one JSON-RPC exchange. A dead transport is redialled with
// backoff before the attempt, and once more after a mid-flight loss
// (§7 reconnect).
func (c *Client) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dead.Load() && !c.closed.Load() {
		if err := c.redialLocked(ctx); err != nil {
			return nil, err
		}
	}
	raw, err := c.requestLocked(ctx, method, params)
	if err != nil && c.dead.Load() && !c.closed.Load() {
		if rerr := c.redialLocked(ctx); rerr == nil {
			return c.requestLocked(ctx, method, params)
		}
	}
	return raw, err
}

// redialLocked rebuilds a dead transport: up to 5 attempts at 250ms,
// 500ms, 1s, 2s, 2s (capped exponential backoff). Caller holds c.mu.
func (c *Client) redialLocked(ctx context.Context) error {
	delay := 250 * time.Millisecond
	var err error
	for attempt := 0; attempt < 5; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
			if delay < 2*time.Second {
				delay *= 2
			}
		}
		c.resetTransport()
		if err = c.transportDial(ctx); err != nil {
			continue
		}
		if err = c.handshake(ctx); err != nil {
			continue
		}
		c.dead.Store(false)
		return nil
	}
	return fmt.Errorf("mcp %s: reconnect: %w", c.Name, err)
}

// resetTransport tears the current transport down fast (no grace wait:
// the transport is already declared dead) so redial starts clean.
func (c *Client) resetTransport() {
	if c.in != nil {
		_ = c.in.Close()
		c.in = nil
	}
	if c.cmd != nil && c.cmd.Process != nil {
		cmd := c.cmd
		_ = cmd.Process.Kill()
		go func() { _ = cmd.Wait() }() // reap the killed child
		c.cmd = nil
	}
	if c.logFile != nil {
		_ = c.logFile.Close()
		c.logFile = nil
	}
	if c.streamCancel != nil {
		c.streamCancel()
		c.streamCancel = nil
	}
	// The HTTP session belongs to the old connection.
	c.session = ""
}

// requestLocked is request's body: the caller serializes on c.mu.
func (c *Client) requestLocked(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.seq++
	id := c.seq
	msg := map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}
	if c.srv.Transport() == "http" {
		raw, err := c.post(ctx, msg)
		if err != nil {
			return nil, err
		}
		return parseRPC(raw, id)
	}
	// stdio and legacy SSE both deliver responses on c.lines.
	if err := c.write(ctx, msg); err != nil {
		return nil, err
	}
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case line, ok := <-c.lines:
			if !ok {
				c.dead.Store(true)
				return nil, fmt.Errorf("server exited")
			}
			raw := json.RawMessage(line)
			var probe struct {
				ID *int `json:"id"`
			}
			if err := json.Unmarshal(raw, &probe); err != nil || probe.ID == nil {
				continue // notification or non-response line
			}
			if *probe.ID != id {
				continue
			}
			return parseRPC(raw, id)
		}
	}
}

// write delivers a client message on a streaming transport: stdio stdin,
// or the legacy SSE message endpoint (the response rides the stream).
func (c *Client) write(ctx context.Context, msg any) error {
	if c.srv.Transport() == "sse" {
		_, err := c.post(ctx, msg)
		return err
	}
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	if _, err := c.in.Write(append(b, '\n')); err != nil {
		c.dead.Store(true)
		return err
	}
	return nil
}

// post sends one JSON-RPC message over streamable or legacy HTTP and
// returns the body (JSON object or the last SSE data line). For legacy
// SSE the response arrives on the stream, so the body is empty.
func (c *Client) post(ctx context.Context, msg any) (json.RawMessage, error) {
	b, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	target := c.srv.URL
	if c.srv.Transport() == "sse" {
		target = c.postURL()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	for k, vals := range c.hdr {
		for _, v := range vals {
			req.Header.Add(k, v)
		}
	}
	if c.session != "" {
		req.Header.Set("Mcp-Session-Id", c.session)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.dead.Store(true) // network loss: redial on the next attempt
		return nil, err
	}
	defer resp.Body.Close()
	if s := resp.Header.Get("Mcp-Session-Id"); s != "" {
		c.session = s
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == 404 && c.session != "" {
		// The server restarted and dropped our session: redial to
		// re-run the handshake and mint a new one.
		c.session = ""
		c.dead.Store(true)
		return nil, fmt.Errorf("session expired (HTTP 404)")
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if c.srv.Transport() == "sse" {
		return json.RawMessage("null"), nil // legacy endpoint: 202, response on the stream
	}
	ct := resp.Header.Get("Content-Type")
	if strings.Contains(ct, "event-stream") {
		// Take the last data: line as the response.
		var last string
		for _, line := range strings.Split(string(body), "\n") {
			if d, ok := strings.CutPrefix(line, "data:"); ok {
				last = strings.TrimSpace(d)
			}
		}
		if last == "" {
			return nil, fmt.Errorf("SSE response carried no data")
		}
		return json.RawMessage(last), nil
	}
	return json.RawMessage(body), nil
}

// parseRPC unwraps a JSON-RPC response for id, mapping the error member.
func parseRPC(raw json.RawMessage, id int) (json.RawMessage, error) {
	var env struct {
		ID     *int            `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("bad response: %w", err)
	}
	if env.Error != nil {
		return nil, fmt.Errorf("rpc error %d: %s", env.Error.Code, env.Error.Message)
	}
	if env.ID == nil || *env.ID != id {
		return nil, fmt.Errorf("response id mismatch")
	}
	return env.Result, nil
}

// ListTools runs tools/list.
func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	result, err := c.request(ctx, "tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var out struct {
		Tools []Tool `json:"tools"`
	}
	if err := json.Unmarshal(result, &out); err != nil {
		return nil, err
	}
	return out.Tools, nil
}

// CallTool runs tools/call and flattens the content array to text.
func (c *Client) CallTool(ctx context.Context, name string, args json.RawMessage) (string, error) {
	params := map[string]any{"name": name}
	if len(args) > 0 {
		var a any
		if err := json.Unmarshal(args, &a); err != nil {
			return "", fmt.Errorf("arguments must be a JSON object: %w", err)
		}
		params["arguments"] = a
	}
	result, err := c.request(ctx, "tools/call", params)
	if err != nil {
		return "", err
	}
	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(result, &out); err != nil {
		return "", err
	}
	var sb strings.Builder
	for _, part := range out.Content {
		if part.Type == "text" || part.Type == "" {
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(part.Text)
		}
	}
	if out.IsError {
		return sb.String(), fmt.Errorf("tool returned an error")
	}
	return sb.String(), nil
}

// Close tears down the connection (kills a spawned stdio process) and
// forbids any reconnect. Safe to call twice.
func (c *Client) Close() error {
	if c.closed.Swap(true) {
		return nil
	}
	if c.stop != nil {
		close(c.stop)
	}
	c.resetTransport()
	if c.baseCancel != nil {
		c.baseCancel()
	}
	return nil
}

// expandEnv resolves a {env:VAR} reference; anything else passes through.
func expandEnv(v string) (string, error) {
	if inner, ok := strings.CutPrefix(v, "{env:"); ok {
		name, ok := strings.CutSuffix(inner, "}")
		if !ok {
			return "", fmt.Errorf("malformed env reference %q", v)
		}
		val, set := os.LookupEnv(name)
		if !set {
			return "", fmt.Errorf("environment variable %s is not set", name)
		}
		return val, nil
	}
	return v, nil
}
