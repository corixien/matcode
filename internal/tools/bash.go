// Package tools holds matcode's builtin tools: one file per tool.
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"matcode/internal/engine"
)

// Inline output cap; anything larger is written to a managed file under
// <dataDir>/tmp and the model is pointed at it instead.
const bashInlineLimit = 64 * 1024

// Bash runs a shell command and returns its combined output. Large output is
// spilled to a file, and commands can run detached in the background.
func Bash(workdir, dataDir string) engine.Tool {
	return engine.Tool{
		ID:          "bash",
		Description: "Run a shell command and return stdout and stderr.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command":    map[string]any{"type": "string", "description": "The shell command to run."},
				"workdir":    map[string]any{"type": "string", "description": "Working directory for the command; relative paths resolve against the project root. Defaults to the project root."},
				"timeout":    map[string]any{"type": "number", "description": "Timeout in milliseconds (default 60000, max 600000). The command is killed when it expires."},
				"background": map[string]any{"type": "boolean", "description": "Run detached: returns immediately with a pid and an output file you can read later instead of blocking."},
			},
			"required": []string{"command"},
		},
		Execute: func(ctx context.Context, input json.RawMessage) (engine.Result, error) {
			var in struct {
				Command    string `json:"command"`
				Workdir    string `json:"workdir"`
				Timeout    int    `json:"timeout"`
				Background bool   `json:"background"`
			}
			if err := json.Unmarshal(input, &in); err != nil {
				return engine.Result{}, err
			}
			dir := workdir
			if in.Workdir != "" {
				dir = resolve(workdir, in.Workdir)
			}
			if in.Background {
				s, err := startBackground(in.Command, dir, dataDir)
				return engine.Result{Text: s}, err
			}
			s, err := runForeground(ctx, in.Command, dir, in.Timeout, dataDir)
			return engine.Result{Text: s}, err
		},
	}
}

// runForeground runs the command under a timeout and returns its output,
// spilling anything past the inline cap into a managed file.
func runForeground(ctx context.Context, command, dir string, timeoutMS int, dataDir string) (string, error) {
	if timeoutMS <= 0 {
		timeoutMS = 60000
	}
	if timeoutMS > 600000 {
		timeoutMS = 600000
	}
	cctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMS)*time.Millisecond)
	defer cancel()

	cmd := exec.CommandContext(cctx, "bash", "-c", command)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if len(out) > bashInlineLimit {
		path, werr := spillOutput(dataDir, out)
		if werr == nil {
			return fmt.Sprintf(
				"output too large for the transcript (%d bytes); full output saved to %s (read that file for the rest).\nFirst 4096 bytes:\n%s",
				len(out), path, truncate(out, 4096)), nil
		}
		out = out[:bashInlineLimit]
	}
	if errors.Is(cctx.Err(), context.DeadlineExceeded) {
		return fmt.Sprintf("timed out after %dms; partial output:\n%s", timeoutMS, out), nil
	}
	if err != nil && len(out) == 0 {
		return "", err
	}
	return string(out), nil
}

// startBackground detaches the command: output goes straight to a managed
// file, and the call returns without waiting. The wrapper shell appends the
// exit status itself, so the marker survives even after matcode exits.
func startBackground(command, dir, dataDir string) (string, error) {
	outFile, err := spillPath(dataDir)
	if err != nil {
		return "", err
	}
	q := shellQuote
	// The wrapper redirects its own stdout/stderr into the spill file, runs
	// the command, then appends the exit status — all inside one shell that
	// outlives matcode.
	script := fmt.Sprintf(
		"exec >>%s 2>&1\nbash -c %s\nstatus=$?\necho\necho \"[exit: $status]\"\n",
		q(outFile), q(command))
	cmd := exec.Command("bash", "-c", script)
	if dir != "" {
		cmd.Dir = dir
	}
	if err := cmd.Start(); err != nil {
		return "", err
	}
	pid := cmd.Process.Pid
	// Reap the child if it exits while matcode is still running; the exit
	// marker itself is written by the wrapper shell, not here.
	go cmd.Wait()
	return fmt.Sprintf(
		"started in background: pid=%d, output file %s; read the file to check progress (the exit status is appended when it finishes)",
		pid, outFile), nil
}

// shellQuote wraps s in single quotes for safe embedding in a shell script.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// spillOutput writes over-limit output under <dataDir>/tmp/shell/.
func spillOutput(dataDir string, out []byte) (string, error) {
	path, err := spillPath(dataDir)
	if err != nil {
		return "", err
	}
	return path, os.WriteFile(path, out, 0o644)
}

func spillPath(dataDir string) (string, error) {
	if dataDir == "" {
		return "", errors.New("no data directory for output spill")
	}
	dir := filepath.Join(dataDir, "tmp", "shell")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(dir, fmt.Sprintf("out-%d.log", time.Now().UnixNano())), nil
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n])
}
