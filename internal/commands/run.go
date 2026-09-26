// Package commands holds one file per mtc subcommand.
package commands

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"matcode/internal/app"
	"matcode/internal/attach"
	"matcode/internal/config"
	"matcode/internal/engine"
	"matcode/internal/permissions"
	"matcode/internal/references"
	"matcode/internal/skills"
	"matcode/internal/store"
)

// stringList is a repeatable flag.Value.
type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// skillsFor delegates to the shared assembly in app: run and the HTTP API
// must apply the same deny-hiding rules (§11).
func skillsFor(cfg *config.Config, perms *permissions.Set) (*skills.Set, error) {
	return app.SkillsFor(cfg, perms)
}

// jsonOut writes one JSON object per line: the event stream behind
// `run --format json`. The TUI renders from the same engine events.
type jsonOut struct{ enc *json.Encoder }

func newJSONOut(w io.Writer) *jsonOut { return &jsonOut{enc: json.NewEncoder(w)} }

func (j *jsonOut) event(v any)          { _ = j.enc.Encode(v) }
func (j *jsonOut) emit(ev engine.Event) { j.event(ev) }
func (j *jsonOut) text(s string)        { j.event(map[string]string{"type": "text", "text": s}) }

// Run executes `mtc run [-agent a] [-model p/m] [-session id] [-continue]
// [-format json] [-file ref] "prompt"`: load config, assemble the shared
// engine (app.New), open or resume a session, stream a reply.
func Run(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	agentFlag := fs.String("agent", "", "agent: build (default), plan, summary, title")
	modelFlag := fs.String("model", "", "provider/model for this run (overrides config)")
	sessionFlag := fs.String("session", "", "resume an existing session id")
	formatFlag := fs.String("format", "", "output format: default, json (NDJSON events)")
	var contFlag bool
	fs.BoolVar(&contFlag, "continue", false, "resume the most recently updated session")
	fs.BoolVar(&contFlag, "c", false, "alias for -continue")
	var fileFlags stringList
	fs.Var(&fileFlags, "file", "prompt attachment: file:// URI (?start=&end= lines), path, dir, or data: URI (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	prompt := strings.Join(fs.Args(), " ")
	if prompt == "" {
		return fmt.Errorf(`usage: mtc run [-agent build|plan|summary|title] [-model provider/model] [-session ses_...|-continue] [-format json] "prompt"`)
	}
	format := *formatFlag
	if format == "" {
		format = "default"
	}
	if format != "default" && format != "json" {
		return fmt.Errorf("unknown -format %q (want default or json)", format)
	}
	if contFlag && *sessionFlag != "" {
		return fmt.Errorf("-continue and -session are mutually exclusive")
	}
	var out *jsonOut
	echoed := false // set when json-mode text streamed through Echo
	if format == "json" {
		out = newJSONOut(os.Stdout)
	}

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	cfg, err := config.Load(cwd)
	if err != nil {
		return err
	}

	// Prompt attachments: text inputs (files, listings) append to the
	// prompt; media inputs (images, PDF) ride on the user message.
	var media []engine.Media
	for _, ref := range fileFlags {
		if alias, ok := strings.CutPrefix(ref, "ref:"); ok {
			r, known := cfg.References[alias]
			if !known {
				names := references.Names(cfg.References, false)
				if len(names) == 0 {
					return fmt.Errorf("unknown reference %q: none configured in config.toml", alias)
				}
				return fmt.Errorf("unknown reference %q (configured: %s)", alias, strings.Join(names, ", "))
			}
			if st, err := os.Stat(r.Dir); err != nil || !st.IsDir() {
				return fmt.Errorf("reference %q is not ready at %s: clone may still be running (see %s)",
					alias, r.Dir, filepath.Join(cfg.DataDir(), "repos", "refresh.log"))
			}
			att, err := attach.Resolve(cwd, r.Dir, attach.MediaConfig{
				AutoResize:     cfg.Media.AutoResize,
				MaxBase64Bytes: cfg.Media.MaxBase64Bytes,
				MaxFileBytes:   cfg.Media.MaxFileBytes,
			})
			if err != nil {
				return err
			}
			if att.Text != "" {
				prompt += "\n\n" + att.Text
			}
			media = append(media, att.Media...)
			continue
		}
		att, err := attach.Resolve(cwd, ref, attach.MediaConfig{
			AutoResize:     cfg.Media.AutoResize,
			MaxBase64Bytes: cfg.Media.MaxBase64Bytes,
			MaxFileBytes:   cfg.Media.MaxFileBytes,
		})
		if err != nil {
			return err
		}
		if att.Text != "" {
			prompt += "\n\n" + att.Text
		}
		media = append(media, att.Media...)
	}

	// One engine builder for run, API, and TUI (§9): agent, provider,
	// permissions, skills, MCP, snapshots all assemble in app.New.
	opts := app.Options{Cwd: cwd, Config: cfg, Agent: *agentFlag, Model: *modelFlag}
	if out != nil {
		opts.Echo = func(s string) { echoed = true; out.text(s) }
		opts.Emit = out.emit
	} else {
		opts.Echo = func(s string) { fmt.Print(s) }
	}
	built, err := app.New(ctx, opts)
	if err != nil {
		return err
	}
	defer built.Close()
	ag, eng, model := built.Agent, built.Engine, built.Model
	agentID := ag.ID

	// Stateless agents (summary, title): one completion, no session, no
	// permissions — they have no tools to gate.
	if ag.Stateless {
		if *sessionFlag != "" || contFlag {
			return fmt.Errorf("-session/-continue cannot be used with the %s agent (stateless)", ag.ID)
		}
		reply, err := eng.Oneshot(ctx, prompt, media...)
		if err != nil {
			if out != nil {
				out.event(map[string]string{"type": "error", "error": err.Error()})
			}
			return err
		}
		if out != nil {
			// Text already streamed through Echo; only a non-streaming
			// provider leaves reply unsent.
			if !echoed && reply != "" {
				out.text(reply)
			}
			out.event(map[string]string{"type": "done"})
			return nil
		}
		if strings.TrimSpace(reply) != "" {
			fmt.Println()
		}
		return nil
	}

	var session *store.Session
	switch {
	case *sessionFlag != "":
		session, err = store.Open(filepath.Join(cfg.SessionsDir(), *sessionFlag))
	case contFlag:
		session, err = store.Latest(cfg.SessionsDir())
	default:
		session, err = store.Create(cfg.SessionsDir(), model)
	}
	if err != nil {
		return err
	}
	// Record which agent/model this run is on; -model and -agent override a
	// resumed session's previous values, and the new pair is persisted.
	if session.Meta.Agent != agentID || session.Meta.Model != model {
		session.Meta.Agent = agentID
		session.Meta.Model = model
		if err := session.Save(); err != nil {
			return err
		}
	}
	if out != nil {
		out.event(map[string]any{
			"type": "session", "id": session.Meta.ID, "agent": agentID, "model": model,
		})
	}
	built.UseSession(session)

	if err := eng.Turn(ctx, prompt, media...); err != nil {
		if out != nil {
			out.event(map[string]string{"type": "error", "error": err.Error()})
		}
		return err
	}
	if out != nil {
		out.event(map[string]any{
			"type": "done", "session": session.Meta.ID,
			"usage": engine.UsageEvent{
				Input: eng.Usage.Input, Output: eng.Usage.Output, Cost: eng.Usage.CostUSD,
			},
		})
		return nil
	}
	fmt.Printf("\n\n[session %s", session.Meta.ID)
	if u := eng.Usage; u.Input > 0 || u.Output > 0 {
		fmt.Printf(" | %d in / %d out tokens", u.Input, u.Output)
		if u.CostUSD > 0 {
			fmt.Printf(" | $%.4f", u.CostUSD)
		}
	}
	fmt.Println("]")
	return nil
}
