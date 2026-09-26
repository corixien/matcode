package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"matcode/internal/agents"
)

// serveCanary is the dummy provider key the /api/config assertions watch
// for: its value must never leave the process.
const serveCanary = "sk-canary-not-a-real-key"

// serveClient bounds every request so a wedged listener fails fast instead
// of hanging the test.
var serveClient = &http.Client{Timeout: 2 * time.Second}

// stderrTap redirects os.Stderr into a pipe for a whole serve run. Unlike
// captureStdout the swap has to span a goroutine: the server logs from its
// own goroutine and keeps logging until Listen returns.
type stderrTap struct {
	r, w    *os.File
	old     *os.File
	buf     bytes.Buffer
	done    chan struct{}
	stopped bool
}

func startStderrTap(t *testing.T) *stderrTap {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	s := &stderrTap{r: r, w: w, old: os.Stderr, done: make(chan struct{})}
	os.Stderr = w
	go func() {
		defer close(s.done)
		_, _ = io.Copy(&s.buf, s.r)
	}()
	t.Cleanup(func() {
		s.Stop()
		// Never swallow a failure message that landed in the pipe.
		if t.Failed() {
			_, _ = fmt.Fprintf(os.Stderr, "captured serve stderr:\n%s\n", s.Text())
		}
	})
	return s
}

// Stop restores os.Stderr and waits for the drain goroutine; Text is safe
// to read only after it returns.
func (s *stderrTap) Stop() {
	if s.stopped {
		return
	}
	s.stopped = true
	os.Stderr = s.old
	_ = s.w.Close()
	<-s.done
	_ = s.r.Close()
}

func (s *stderrTap) Text() string { return s.buf.String() }

// freePort reserves an ephemeral loopback port and hands it back released,
// so tests never hardcode one (port 18787 in server_test is taken).
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// withServeProject writes config.toml into a temp project, points the
// global tree at an empty HOME, and chdirs into it: Serve assembles its
// server from the cwd (same shape as withAPIProject).
func withServeProject(t *testing.T, cfg string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".mtc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".mtc", "config.toml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	// Hermetic global tree: no real ~/.config/mtc may reach the test.
	t.Setenv("HOME", filepath.Join(dir, "home"))
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
	// server.New re-reads agent overlays from the temp tree; reset them.
	t.Cleanup(func() { _ = agents.Load() })
}

// serveRun is a running `mtc serve` on a background goroutine.
type serveRun struct {
	tap    *stderrTap
	errCh  chan error
	cancel context.CancelFunc
}

// startServe runs Serve(args) until /api/health answers on
// http://127.0.0.1:port, proving the listener is up on that exact
// address.
func startServe(t *testing.T, args []string, port int) *serveRun {
	t.Helper()
	run := &serveRun{tap: startStderrTap(t), errCh: make(chan error, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	run.cancel = cancel
	t.Cleanup(cancel)
	go func() { run.errCh <- Serve(ctx, args) }()

	base := "http://127.0.0.1:" + strconv.Itoa(port)
	deadline := time.Now().Add(5 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		select {
		case err := <-run.errCh:
			cancel()
			t.Fatalf("serve exited before answering: %v", err)
		default:
		}
		resp, err := serveClient.Get(base + "/api/health")
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return run
			}
			lastErr = fmt.Errorf("status %d", resp.StatusCode)
		} else {
			lastErr = err
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	t.Fatalf("serve never healthy on %s: %v", base, lastErr)
	return nil
}

// stop cancels the run, waits for Serve to return nil, and yields the
// captured stderr.
func (r *serveRun) stop(t *testing.T) string {
	t.Helper()
	r.cancel()
	select {
	case err := <-r.errCh:
		if err != nil {
			t.Fatalf("serve returned %v, want nil after cancel", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not stop on ctx cancel")
	}
	r.tap.Stop()
	return r.tap.Text()
}

// getURL fetches a path and returns status plus the raw body.
func getURL(t *testing.T, base, path string) (int, string) {
	t.Helper()
	resp, err := serveClient.Get(base + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// TestServeDefaultsBindsConfigPort is `mtc serve` end-to-end: with no
// flags it binds loopback on the config's api_port, /api/health answers,
// and /api/config exposes the key's env name and set-flag but never the
// value.
func TestServeDefaultsBindsConfigPort(t *testing.T) {
	port := freePort(t)
	withServeProject(t, fmt.Sprintf(`api_port = %d

[providers.mockt]
base_url = "http://127.0.0.1:1/v1"
api_key = { env = "MTC_SERVE_TEST_KEY" }
`, port))
	t.Setenv("MTC_SERVE_TEST_KEY", serveCanary)

	run := startServe(t, nil, port)
	base := "http://127.0.0.1:" + strconv.Itoa(port)

	code, body := getURL(t, base, "/api/health")
	if code != http.StatusOK || !strings.Contains(body, `"ok":true`) {
		t.Fatalf("health: %d %s", code, body)
	}

	code, raw := getURL(t, base, "/api/config")
	if code != http.StatusOK {
		t.Fatalf("config: %d %s", code, raw)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("config json: %v", err)
	}
	prov, _ := m["providers"].(map[string]any)["mockt"].(map[string]any)
	if prov == nil {
		t.Fatalf("mockt provider missing: %s", raw)
	}
	if prov["api_key_env"] != "MTC_SERVE_TEST_KEY" || prov["api_key_set"] != true {
		t.Errorf("key flags: %v", prov)
	}
	if _, ok := prov["api_key"]; ok {
		t.Errorf("api_key leaked as a field: %v", prov)
	}
	if strings.Contains(raw, serveCanary) {
		t.Errorf("api key value echoed by /api/config: %s", raw)
	}
	if m["api_port"] != float64(port) {
		t.Errorf("api_port = %v, want %d", m["api_port"], port)
	}

	out := run.stop(t)
	if !strings.Contains(out, "listening on http://127.0.0.1:"+strconv.Itoa(port)) {
		t.Errorf("default host/flag-port not logged: %q", out)
	}
	if !strings.Contains(out, "mtc serve: stopped") {
		t.Errorf("clean-stop line missing: %q", out)
	}
}

// TestServePortFlagOverridesConfig: -port wins over config api_port while
// the default -host still pins the bind to loopback.
func TestServePortFlagOverridesConfig(t *testing.T) {
	cfgPort := freePort(t)
	flagPort := freePort(t)
	withServeProject(t, fmt.Sprintf("api_port = %d\n", cfgPort))

	run := startServe(t, []string{"-port", strconv.Itoa(flagPort)}, flagPort)
	code, body := getURL(t, "http://127.0.0.1:"+strconv.Itoa(flagPort), "/api/health")
	if code != http.StatusOK || !strings.Contains(body, `"ok":true`) {
		t.Fatalf("health on flag port: %d %s", code, body)
	}

	out := run.stop(t)
	want := "listening on http://127.0.0.1:" + strconv.Itoa(flagPort)
	if !strings.Contains(out, want) {
		t.Errorf("stderr = %q, want %q", out, want)
	}
	if strings.Contains(out, ":"+strconv.Itoa(cfgPort)) {
		t.Errorf("config port %d bound instead of the flag: %q", cfgPort, out)
	}
}

// TestServeFlagErrors: flag problems surface as errors before any listener
// or config assembly happens.
func TestServeFlagErrors(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"-definitely-not-a-flag"}, "flag provided but not defined"},
		{[]string{"-host"}, "flag needs an argument: -host"},
		{[]string{"-port", "not-a-number"}, "invalid value"},
	}
	tap := startStderrTap(t)
	for _, tc := range cases {
		err := Serve(context.Background(), tc.args)
		if err == nil {
			t.Errorf("Serve(%q) = nil, want error", tc.args)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("Serve(%q) = %v, want %q", tc.args, err, tc.want)
		}
	}
	tap.Stop()
}
