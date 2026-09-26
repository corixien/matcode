package cli

import (
	"context"
	"os"
	"strings"
	"testing"
)

// captureStderr runs fn with os.Stderr piped (the flag package prints
// usage on parse errors) and returns what was written, plus fn's error.
func captureStderr(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	runErr := fn()
	os.Stderr = old
	w.Close()
	var b strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		b.Write(buf[:n])
		if err != nil {
			break
		}
	}
	r.Close()
	return b.String(), runErr
}

// TestServeDispatch pins the `serve` routing: args land in serve's own
// flagset (only -host/-port live there), so a missing value reads as "flag
// needs an argument" rather than an undefined flag. IsSubcommand feeds the
// bare-prompt heuristic in main, so "serve" must never fall through to it.
func TestServeDispatch(t *testing.T) {
	if !IsSubcommand("serve") {
		t.Error("serve must be a known subcommand")
	}
	_, err := captureStderr(t, func() error {
		return Dispatch(context.Background(), "serve", []string{"-host"})
	})
	if err == nil {
		t.Fatal("serve -host without a value must fail")
	}
	if !strings.Contains(err.Error(), "flag needs an argument: -host") {
		t.Errorf("error = %v, want serve's own flagset to parse the args", err)
	}
}

// TestDispatchUnknown: an unknown name reports instead of running anything.
func TestDispatchUnknown(t *testing.T) {
	err := Dispatch(context.Background(), "definitely-not-a-command", nil)
	if err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Errorf("Dispatch = %v, want unknown command error", err)
	}
	if IsSubcommand("definitely-not-a-command") {
		t.Error("unknown name must not be a subcommand")
	}
}
