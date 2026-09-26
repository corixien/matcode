package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"matcode/internal/engine"
)

// userTimeout bounds one exec.sh run (same envelope as the bash tool).
const (
	userTimeoutDefault = 60 * time.Second
	userTimeoutMax     = 600 * time.Second
)

// userSpec is one parsed `<dir>/<name>/tool.toml`. The folder name is the
// tool id when the file does not state one; `script` defaults to exec.sh.
type userSpec struct {
	dir     string
	id      string
	desc    string
	script  string
	timeout time.Duration
	input   map[string]any
}

// toolFile mirrors tool.toml (spec §registry): id, description, an
// optional script/timeout, and the JSON-Schema of the input under
// `[input]`.
type toolFile struct {
	ID          string         `toml:"id"`
	Description string         `toml:"description"`
	Script      string         `toml:"script"`
	Timeout     float64        `toml:"timeout"` // seconds
	Input       map[string]any `toml:"input"`
}

// User loads the data-tree tools (spec §registry: builtins →
// data/tools/<name>/ → MCP). Problems never abort the load — a broken
// folder is skipped with a warning so one typo cannot take the registry
// down; Validate reports the same problems for `mtc doctor`.
//
// Tools are not compiled: tool.toml declares the schema and exec.sh gets
// the JSON input on stdin, printing JSON `{"output": …}` on stdout.
func User(workdir string, dirs ...string) []engine.Tool {
	specs, problems := loadUserSpecs(dirs)
	for _, p := range problems {
		fmt.Fprintf(os.Stderr, "mtc: user tool %s\n", p)
	}
	out := make([]engine.Tool, 0, len(specs))
	for _, s := range specs {
		out = append(out, s.engine(workdir))
	}
	return out
}

// Validate loads the same tree for its diagnostics: it returns how many
// tools resolve and one message per folder that does not.
func Validate(dirs ...string) (int, []string) {
	specs, problems := loadUserSpecs(dirs)
	return len(specs), problems
}

// loadUserSpecs reads every `<dir>/<name>/tool.toml`, later dirs winning
// per tool so a project tool replaces its global twin (§3 resolution).
func loadUserSpecs(dirs []string) ([]userSpec, []string) {
	var specs []userSpec
	var problems []string
	index := map[string]int{} // tool id → position in specs
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			if !os.IsNotExist(err) {
				problems = append(problems, fmt.Sprintf("%s: %v", dir, err))
			}
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			root := filepath.Join(dir, e.Name())
			path := filepath.Join(root, "tool.toml")
			var f toolFile
			if _, err := toml.DecodeFile(path, &f); err != nil {
				if os.IsNotExist(err) {
					problems = append(problems, fmt.Sprintf("%s: no tool.toml", root))
					continue
				}
				problems = append(problems, fmt.Sprintf("%s: %v", path, err))
				continue
			}
			s := userSpec{
				dir:     root,
				id:      f.ID,
				desc:    f.Description,
				script:  f.Script,
				timeout: time.Duration(f.Timeout * float64(time.Second)),
				input:   f.Input,
			}
			if s.id == "" {
				s.id = e.Name()
			}
			if s.desc == "" {
				s.desc = fmt.Sprintf("User tool %q from the data tree (tools/%s).", s.id, e.Name())
			}
			if s.script == "" {
				s.script = "exec.sh"
			}
			if s.timeout <= 0 {
				s.timeout = userTimeoutDefault
			} else if s.timeout > userTimeoutMax {
				s.timeout = userTimeoutMax
			}
			if i, seen := index[s.id]; seen {
				specs[i] = s // last registration wins
				continue
			}
			index[s.id] = len(specs)
			specs = append(specs, s)
		}
	}
	return specs, problems
}

// engine turns the spec into a callable: exec.sh under the workdir, input
// on stdin, its JSON on stdout.
func (s userSpec) engine(workdir string) engine.Tool {
	script := filepath.Join(s.dir, s.script)
	return engine.Tool{
		ID:          s.id,
		Description: s.desc,
		Schema:      s.schema(),
		Execute: func(ctx context.Context, input json.RawMessage) (engine.Result, error) {
			cctx, cancel := context.WithTimeout(ctx, s.timeout)
			defer cancel()
			cmd := exec.CommandContext(cctx, "bash", script)
			if workdir != "" {
				cmd.Dir = workdir
			}
			cmd.Stdin = bytes.NewReader(input)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr

			err := cmd.Run()
			if cctx.Err() == context.DeadlineExceeded {
				return engine.Result{}, fmt.Errorf("user tool %s: timed out after %s", s.id, s.timeout)
			}
			if err != nil {
				return engine.Result{}, fmt.Errorf("user tool %s: %w: %s",
					s.id, err, trimDiag(stderr.String(), stdout.String()))
			}
			return userResult(stdout.Bytes()), nil
		},
	}
}

// schema is the input JSON-Schema; a tool that declares none still gets a
// valid empty object schema so the provider accepts the call.
func (s userSpec) schema() map[string]any {
	in := s.input
	if in == nil {
		in = map[string]any{}
	}
	if _, ok := in["type"]; !ok {
		in["type"] = "object"
	}
	return in
}

// userResult reads exec.sh's stdout. The contract is `{"output": …}`;
// anything else that still exited 0 is passed through as raw text, which
// keeps one-line scripts (`echo hi`) usable.
func userResult(stdout []byte) engine.Result {
	trimmed := bytes.TrimSpace(stdout)
	if len(trimmed) == 0 {
		return engine.Result{}
	}
	var payload struct {
		Output json.RawMessage `json:"output"`
	}
	if err := json.Unmarshal(trimmed, &payload); err == nil && len(payload.Output) > 0 {
		var s string
		if json.Unmarshal(payload.Output, &s) == nil {
			return engine.Result{Text: s}
		}
		return engine.Result{Text: string(payload.Output)}
	}
	return engine.Result{Text: string(trimmed)}
}

// trimDiag keeps a failing tool's stderr (then stdout) short enough for
// the transcript.
func trimDiag(stderr, stdout string) string {
	for _, s := range []string{stderr, stdout} {
		if t := strings.TrimSpace(s); t != "" {
			if len(t) > 2000 {
				t = t[:2000] + "…"
			}
			return t
		}
	}
	return "no output"
}
