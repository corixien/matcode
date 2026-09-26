package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"matcode/internal/engine"
)

// mutExec feeds raw JSON into a tool's Execute — the input shape the
// engine hands every tool (row 51, mutating-tools coverage).
func mutExec(t *testing.T, tool engine.Tool, input string) (engine.Result, error) {
	t.Helper()
	return tool.Execute(context.Background(), json.RawMessage(input))
}

// mutSchema asserts the advertised input contract: type=object, the exact
// required list, and a property entry per required field.
func mutSchema(t *testing.T, tool engine.Tool, required ...string) {
	t.Helper()
	if tool.ID == "" || tool.Description == "" {
		t.Errorf("id/description = %q/%q, want both set", tool.ID, tool.Description)
	}
	if tool.Schema["type"] != "object" {
		t.Errorf("schema type = %v, want object", tool.Schema["type"])
	}
	got, ok := tool.Schema["required"].([]string)
	if !ok {
		t.Fatalf("required = %#v, want []string", tool.Schema["required"])
	}
	if len(got) != len(required) {
		t.Fatalf("required = %v, want %v", got, required)
	}
	for i := range required {
		if got[i] != required[i] {
			t.Errorf("required[%d] = %q, want %q", i, got[i], required[i])
		}
	}
	props, ok := tool.Schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties = %#v, want a map", tool.Schema["properties"])
	}
	for _, r := range required {
		if _, ok := props[r]; !ok {
			t.Errorf("required field %q has no property entry", r)
		}
	}
}

// mutRead slurps a file for assertions.
func mutRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestBashRunsAndCapturesOutput(t *testing.T) {
	tool := Bash(t.TempDir(), t.TempDir())

	// Both streams land in the combined output.
	res, err := mutExec(t, tool, `{"command":"echo out-here; echo err-here >&2"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "out-here") || !strings.Contains(res.Text, "err-here") {
		t.Errorf("combined output = %q, want both streams", res.Text)
	}
	if len(res.Media) != 0 {
		t.Errorf("media = %v, want none", res.Media)
	}

	// A silent non-zero exit reaches the caller as an error carrying the status.
	if _, err := mutExec(t, tool, `{"command":"exit 7"}`); err == nil ||
		!strings.Contains(err.Error(), "exit status 7") {
		t.Errorf("err = %v, want exit status 7", err)
	}
	// Once the command produced output the exit status is swallowed: the
	// text reaches the model, the error does not.
	res, err = mutExec(t, tool, `{"command":"echo partial; echo boom >&2; exit 3"}`)
	if err != nil {
		t.Fatalf("exit 3 with output must not error: %v", err)
	}
	if !strings.Contains(res.Text, "partial") || !strings.Contains(res.Text, "boom") {
		t.Errorf("output = %q, want the streams despite exit 3", res.Text)
	}
	if res, err = mutExec(t, tool, `{"command":"true"}`); err != nil || res.Text != "" {
		t.Errorf("true → (%q, %v), want empty success", res.Text, err)
	}
}

func TestBashSchemaAndJSONInput(t *testing.T) {
	tool := Bash(t.TempDir(), t.TempDir())
	mutSchema(t, tool, "command")
	props := tool.Schema["properties"].(map[string]any)
	for _, f := range []string{"workdir", "timeout", "background"} {
		if _, ok := props[f]; !ok {
			t.Errorf("property %q missing", f)
		}
	}

	if _, err := mutExec(t, tool, "not-json"); err == nil {
		t.Error("malformed JSON must error, not panic")
	}
	if _, err := mutExec(t, tool, `{"command":"echo hi","timeout":"soon"}`); err == nil {
		t.Error("non-numeric timeout must error")
	}
	// Current behavior: `required` is advisory — an empty command runs
	// `bash -c ""` and succeeds with empty output instead of erroring.
	res, err := mutExec(t, tool, `{}`)
	if err != nil || res.Text != "" {
		t.Errorf("empty input → (%q, %v), want empty success", res.Text, err)
	}
}

func TestBashTimeoutKillsCommand(t *testing.T) {
	tool := Bash(t.TempDir(), t.TempDir())
	start := time.Now()
	res, err := mutExec(t, tool, `{"command":"echo before; exec sleep 5","timeout":200}`)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Fatalf("took %v: the timeout must kill the command, not wait it out", elapsed)
	}
	if !strings.Contains(res.Text, "timed out after 200ms") {
		t.Errorf("timeout text = %q", res.Text)
	}
	if !strings.Contains(res.Text, "before") {
		t.Errorf("partial output missing: %q", res.Text)
	}
}

func TestBashWorkdirResolution(t *testing.T) {
	root := t.TempDir()
	wd := filepath.Join(root, "proj")
	if err := os.MkdirAll(filepath.Join(wd, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	tool := Bash(wd, t.TempDir())

	pwd := func(t *testing.T, input string) string {
		t.Helper()
		res, err := mutExec(t, tool, input)
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(res.Text)
	}
	sameDir := func(got, want string) bool {
		g, err1 := filepath.EvalSymlinks(got)
		w, err2 := filepath.EvalSymlinks(want)
		return err1 == nil && err2 == nil && g == w
	}

	if got := pwd(t, `{"command":"pwd"}`); !sameDir(got, wd) {
		t.Errorf("default cwd = %q, want the workdir %q", got, wd)
	}
	if got := pwd(t, `{"command":"pwd","workdir":"sub"}`); !sameDir(got, filepath.Join(wd, "sub")) {
		t.Errorf("relative workdir = %q, want %q", got, filepath.Join(wd, "sub"))
	}
	if got := pwd(t, `{"command":"pwd","workdir":"`+root+`"}`); !sameDir(got, root) {
		t.Errorf("absolute workdir = %q, want %q", got, root)
	}
	// A workdir that does not exist fails the call instead of running elsewhere.
	if _, err := mutExec(t, tool, `{"command":"echo hi","workdir":"missing"}`); err == nil {
		t.Error("missing workdir must error")
	}
}

func TestBashSpillsLargeOutput(t *testing.T) {
	dataDir := t.TempDir()
	tool := Bash(t.TempDir(), dataDir)

	res, err := mutExec(t, tool, `{"command":"yes a | head -c 70000"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "output too large for the transcript (70000 bytes)") ||
		!strings.Contains(res.Text, "First 4096 bytes:") {
		t.Errorf("spill notice = %q", res.Text)
	}
	spillDir := filepath.Join(dataDir, "tmp", "shell")
	if !strings.Contains(res.Text, spillDir) {
		t.Errorf("spill path %q missing from %q", spillDir, res.Text)
	}
	entries, err := os.ReadDir(spillDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("spilled files = %d, want 1", len(entries))
	}
	fi, err := entries[0].Info()
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() != 70000 {
		t.Errorf("spill file = %d bytes, want 70000", fi.Size())
	}

	// No data dir: the spill fails and the output is cut at the inline cap.
	tool = Bash(t.TempDir(), "")
	res, err = mutExec(t, tool, `{"command":"yes a | head -c 70000"}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Text) != bashInlineLimit {
		t.Errorf("truncated output = %d bytes, want %d", len(res.Text), bashInlineLimit)
	}
	if strings.Contains(res.Text, "output too large") {
		t.Error("the fallback must not point at a spill file that was never written")
	}
}

func TestBashBackground(t *testing.T) {
	dataDir := t.TempDir()
	tool := Bash(t.TempDir(), dataDir)

	res, err := mutExec(t, tool, `{"command":"echo bg-done","background":true}`)
	if err != nil {
		t.Fatal(err)
	}
	const marker = "started in background: pid="
	if !strings.HasPrefix(res.Text, marker) {
		t.Fatalf("background text = %q, want prefix %q", res.Text, marker)
	}
	rest := res.Text[len(marker):]
	pid, _, _ := strings.Cut(rest, ",")
	if n, err := strconv.Atoi(pid); err != nil || n <= 0 {
		t.Errorf("pid = %q, want a positive integer", pid)
	}
	_, file, ok := strings.Cut(rest, "output file ")
	outPath, _, ok2 := strings.Cut(file, "; read")
	if !ok || !ok2 {
		t.Fatalf("output file path missing: %q", res.Text)
	}
	if !strings.HasPrefix(outPath, filepath.Join(dataDir, "tmp", "shell")) {
		t.Errorf("output file = %q, want it under the data dir", outPath)
	}

	// The wrapper shell appends the exit marker once the command finishes.
	deadline := time.Now().Add(5 * time.Second)
	var body []byte
	for time.Now().Before(deadline) {
		if body, err = os.ReadFile(outPath); err == nil &&
			strings.Contains(string(body), "[exit: 0]") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := string(body); !strings.Contains(got, "bg-done") ||
		!strings.Contains(got, "[exit: 0]") {
		t.Errorf("background output = %q, want command output plus exit marker", got)
	}

	// No data dir: there is nowhere to spill, so the spawn fails cleanly.
	_, err = mutExec(t, Bash(t.TempDir(), ""), `{"command":"echo hi","background":true}`)
	if err == nil || !strings.Contains(err.Error(), "no data directory") {
		t.Errorf("err = %v, want the missing data directory error", err)
	}
}
