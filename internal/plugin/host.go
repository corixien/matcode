package plugin

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Host spawns enabled plugins and keeps them alive across hook calls.
// Open and Reload never fail: a plugin that will not start is recorded and
// skipped, because a plugin must never break a session (§8).
type Host struct {
	mu      sync.Mutex
	dirs    []string
	logDir  string
	plugins []Plugin
	procs   map[string]*proc
	errs    map[string]error // last spawn failure per id
	closed  bool
}

// Status is one plugin's runtime picture for `mtc plugin list`.
type Status struct {
	Plugin Plugin
	State  string // running | disabled | stopped | failed
	PID    int
	Err    string
}

// Open loads the plugin tree under dirs and starts every enabled plugin.
// logDir receives one stderr log per plugin; "" discards it.
func Open(dirs []string, logDir string) *Host {
	h := &Host{
		dirs:   append([]string{}, dirs...),
		logDir: logDir,
		procs:  map[string]*proc{},
		errs:   map[string]error{},
	}
	h.mu.Lock()
	h.plugins = Load(h.dirs...)
	h.mu.Unlock()
	h.spawn()
	return h
}

// Reload re-reads the data tree: removed, disabled or edited plugins are
// killed and the rest restarted. Safe to call from the config watcher.
func (h *Host) Reload(dirs ...string) {
	h.mu.Lock()
	if len(dirs) > 0 {
		h.dirs = append([]string{}, dirs...)
	}
	old := h.procs
	h.procs = map[string]*proc{}
	h.errs = map[string]error{}
	h.plugins = Load(h.dirs...)
	closed := h.closed
	h.mu.Unlock()

	for _, pr := range old {
		pr.stop()
	}
	if closed {
		return
	}
	h.spawn()
}

// Close stops every plugin process.
func (h *Host) Close() {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	old := h.procs
	h.procs = map[string]*proc{}
	h.mu.Unlock()
	for _, pr := range old {
		pr.stop()
	}
}

// Plugins returns the loaded manifests (enabled and disabled alike).
func (h *Host) Plugins() []Plugin {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]Plugin{}, h.plugins...)
}

// Dirs returns the plugin roots the host was opened with.
func (h *Host) Dirs() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string{}, h.dirs...)
}

// Status reports every plugin's runtime state.
func (h *Host) Status() []Status {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]Status, 0, len(h.plugins))
	for _, p := range h.plugins {
		s := Status{Plugin: p}
		switch {
		case !p.Enabled:
			s.State = "disabled"
		case h.errs[p.ID] != nil:
			s.State, s.Err = "failed", h.errs[p.ID].Error()
		default:
			if pr, ok := h.procs[p.ID]; ok {
				select {
				case <-pr.done:
					s.State = "stopped"
					s.Err = exitText(pr.exitErr)
				default:
					s.State, s.PID = "running", pr.cmd.Process.Pid
				}
			} else {
				s.State = "stopped"
			}
		}
		out = append(out, s)
	}
	return out
}

// spawn starts every enabled plugin that is not running yet.
func (h *Host) spawn() {
	h.mu.Lock()
	plugins := append([]Plugin{}, h.plugins...)
	closed := h.closed
	h.mu.Unlock()
	if closed {
		return
	}
	for _, p := range plugins {
		if !p.Enabled {
			continue
		}
		if _, err := h.procFor(p); err != nil {
			h.note(p.ID, err)
		}
	}
}

func (h *Host) note(id string, err error) {
	h.mu.Lock()
	h.errs[id] = err
	h.mu.Unlock()
}

// procFor returns the running process for p, starting one if needed. A
// crashed plugin is restarted on the next hook call (crash isolation §8).
func (h *Host) procFor(p Plugin) (*proc, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, errors.New("plugin host is closed")
	}
	if pr, ok := h.procs[p.ID]; ok {
		select {
		case <-pr.done:
			delete(h.procs, p.ID) // dead: fall through to a fresh spawn
		default:
			return pr, nil
		}
	}
	pr, err := startProc(p, h.logDir)
	if err != nil {
		return nil, fmt.Errorf("plugin %s: %w", p.ID, err)
	}
	delete(h.errs, p.ID)
	h.procs[p.ID] = pr
	return pr, nil
}

// callFirst asks plugins in order and returns the first real result: the
// hook surface is "first plugin wins". Errors (crash, timeout, bad JSON)
// are skipped so one broken plugin cannot block the rest.
func (h *Host) callFirst(ctx context.Context, method, perm string, params any) json.RawMessage {
	for _, p := range h.granted(perm) {
		pr, err := h.procFor(p)
		if err != nil {
			h.note(p.ID, err)
			continue
		}
		res, err := pr.call(ctx, method, params, p.Timeout)
		if err != nil || len(res) == 0 {
			continue
		}
		return res
	}
	return nil
}

// callAll asks every granted plugin and collects their results in load
// order (session.context stacks lines from all of them).
func (h *Host) callAll(ctx context.Context, method, perm string, params any) []json.RawMessage {
	var out []json.RawMessage
	for _, p := range h.granted(perm) {
		pr, err := h.procFor(p)
		if err != nil {
			h.note(p.ID, err)
			continue
		}
		res, err := pr.call(ctx, method, params, p.Timeout)
		if err != nil || len(res) == 0 {
			continue
		}
		out = append(out, res)
	}
	return out
}

// granted lists enabled plugins that declared perm, in load order.
func (h *Host) granted(perm string) []Plugin {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []Plugin
	for _, p := range h.plugins {
		if p.Enabled && p.Allows(perm) {
			out = append(out, p)
		}
	}
	return out
}

// --- one subprocess ---------------------------------------------------------

// rpcRequest is a JSON-RPC 2.0 request (newline-delimited, §8).
type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// rpcResponse accepts a result, an error, or noise from stdout.
type rpcResponse struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
	Method string          `json:"method"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

const maxLine = 4 << 20 // 4 MB per response line

type proc struct {
	p     Plugin
	cmd   *exec.Cmd
	log   *os.File
	stdin io.WriteCloser

	writeMu sync.Mutex // serializes request writes
	busyMu  sync.Mutex // one in-flight call: plugin loops read line → answer

	mu     sync.Mutex
	wait   map[int64]chan rpcResponse
	nextID atomic.Int64

	done    chan struct{}
	exitErr error
}

func startProc(p Plugin, logDir string) (*proc, error) {
	cmd := exec.Command(p.Cmd[0], p.Cmd[1:]...)
	cmd.Dir = p.Dir
	cmd.Env = os.Environ()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var logFile *os.File
	if logDir != "" {
		if mkErr := os.MkdirAll(logDir, 0o755); mkErr == nil {
			name := "plugin-" + strings.Map(func(r rune) rune {
				if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-' || r == '_' {
					return r
				}
				return '_'
			}, p.ID) + ".log"
			logFile, _ = os.OpenFile(filepath.Join(logDir, name), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		}
	}
	if logFile != nil {
		cmd.Stderr = logFile
	} else {
		cmd.Stderr = io.Discard
	}
	if err := cmd.Start(); err != nil {
		if logFile != nil {
			logFile.Close()
		}
		return nil, err
	}
	pr := &proc{p: p, cmd: cmd, log: logFile, stdin: stdin,
		wait: map[int64]chan rpcResponse{}, done: make(chan struct{})}
	go pr.read(stdout)
	go pr.reap()
	return pr, nil
}

// reap records why the process ended and wakes every waiting call.
func (pr *proc) reap() {
	err := pr.cmd.Wait()
	pr.exitErr = err
	if pr.log != nil {
		pr.log.Close()
	}
	close(pr.done)
}

// stop closes stdin (the plugin's normal shutdown signal) and force-kills
// it if it lingers.
func (pr *proc) stop() {
	select {
	case <-pr.done:
		return
	default:
	}
	pr.stdinClose()
	select {
	case <-pr.done:
		return
	case <-time.After(300 * time.Millisecond):
	}
	if pr.cmd.Process != nil {
		_ = pr.cmd.Process.Kill()
	}
}

func (pr *proc) stdinClose() {
	if pr.stdin != nil {
		_ = pr.stdin.Close()
	}
}

// read dispatches responses to waiting calls until the pipe closes.
func (pr *proc) read(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), maxLine)
	for sc.Scan() {
		var resp rpcResponse
		if json.Unmarshal(sc.Bytes(), &resp) != nil {
			continue // stdout noise: not a JSON-RPC message (§8 keeps stdout pure)
		}
		if len(resp.ID) == 0 || string(resp.ID) == "null" {
			continue // notification or outbound request: no inbound surface in v1
		}
		var id int64
		if json.Unmarshal(resp.ID, &id) != nil {
			continue
		}
		pr.mu.Lock()
		ch := pr.wait[id]
		pr.mu.Unlock()
		if ch == nil {
			continue
		}
		select {
		case ch <- resp:
		default:
		}
	}
}

// call sends one request and blocks for its response, a timeout, a plugin
// crash, or ctx cancellation — never longer than timeout.
func (pr *proc) call(ctx context.Context, method string, params any, timeout time.Duration) (json.RawMessage, error) {
	pr.busyMu.Lock()
	defer pr.busyMu.Unlock()
	select {
	case <-pr.done:
		return nil, fmt.Errorf("plugin %s: exited: %s", pr.p.ID, exitText(pr.exitErr))
	default:
	}

	id := pr.nextID.Add(1)
	ch := make(chan rpcResponse, 1)
	pr.mu.Lock()
	pr.wait[id] = ch
	pr.mu.Unlock()
	defer func() {
		pr.mu.Lock()
		delete(pr.wait, id)
		pr.mu.Unlock()
	}()

	req := rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params}
	pr.writeMu.Lock()
	err := json.NewEncoder(pr.stdin).Encode(req)
	pr.writeMu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("plugin %s: write: %w", pr.p.ID, err)
	}

	if timeout <= 0 {
		timeout = defaultTimeout
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case resp := <-ch:
		if resp.Error != nil {
			return nil, fmt.Errorf("plugin %s: %s: %s", pr.p.ID, method, resp.Error.Message)
		}
		if len(resp.Result) == 0 || string(resp.Result) == "null" {
			return nil, nil // {"result":null} = "I do not handle this"
		}
		return resp.Result, nil
	case <-timer.C:
		return nil, fmt.Errorf("plugin %s: %s timed out after %s", pr.p.ID, method, timeout)
	case <-pr.done:
		return nil, fmt.Errorf("plugin %s: exited: %s", pr.p.ID, exitText(pr.exitErr))
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func exitText(err error) string {
	if err == nil {
		return "process exited"
	}
	return err.Error()
}
