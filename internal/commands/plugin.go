package commands

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"matcode/internal/plugin"
)

// Plugin implements `mtc plugin new|list|check` over the plugins data tree
// (§8): one folder per plugin, a JSON-RPC subprocess on the other end of it.
//
//	mtc plugin new <name>          scaffold plugins/<name>/{plugin.toml,main.py,README.md}
//	mtc plugin list                every loaded plugin: state, grants, hooks
//	mtc plugin check               validate the tree; non-zero exit on problems
func Plugin(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: mtc plugin new|list|check … (try: mtc plugin list)")
	}
	switch args[0] {
	case "new", "create":
		return pluginNew(args[1:])
	case "list", "ls":
		return pluginList(args[1:])
	case "check":
		return pluginCheck(args[1:])
	default:
		return fmt.Errorf("unknown plugin command %q (want new, list, check)", args[0])
	}
}

// pluginNew scaffolds a working plugin: the manifest, an answering main.py
// that speaks the 6 hooks, and a README explaining the protocol.
func pluginNew(args []string) error {
	fs := flag.NewFlagSet("plugin new", flag.ContinueOnError)
	var global, force bool
	fs.BoolVar(&global, "global", false, "scaffold into the global data tree")
	fs.BoolVar(&force, "force", false, "overwrite existing files")
	if err := parse(fs, args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) != 1 || strings.TrimSpace(rest[0]) == "" {
		return fmt.Errorf("usage: mtc plugin new <name> [--global]")
	}
	name := strings.TrimSpace(rest[0])
	if strings.ContainsAny(name, "/\\.") || name != filepath.Base(name) {
		return fmt.Errorf("plugin new: %q is not a folder name", name)
	}

	cfg, _, err := sessionConfig()
	if err != nil {
		return err
	}
	root := cfg.PluginsDir()
	if global {
		root = filepath.Join(cfg.GlobalDir, "plugins")
	}
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	files := map[string]string{
		"plugin.toml": fmt.Sprintf(scaffoldTOML, name),
		"main.py":     scaffoldScript,
		"README.md":   scaffoldREADME,
	}
	for fname, body := range files {
		path := filepath.Join(dir, fname)
		if _, err := os.Stat(path); err == nil && !force {
			return fmt.Errorf("plugin new: %s exists (use --force to overwrite)", path)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			return err
		}
	}
	fmt.Printf("created plugin %q in %s\n", name, dir)
	fmt.Printf("next: mtc plugin check && mtc doctor\n")
	return nil
}

// pluginList prints the tree as it loads: state first, because a plugin
// that never answers is the common failure.
func pluginList(args []string) error {
	fs := flag.NewFlagSet("plugin list", flag.ContinueOnError)
	if err := parse(fs, args); err != nil {
		return err
	}
	cfg, _, err := sessionConfig()
	if err != nil {
		return err
	}
	list := plugin.Load(cfg.PluginsDirs()...)
	if len(list) == 0 {
		fmt.Printf("no plugins (looked in %s)\n", strings.Join(cfg.PluginsDirs(), ", "))
		return nil
	}
	// A host gives the runtime picture (state/pid) without keeping it: it
	// starts every plugin and we close it on the way out.
	host := plugin.Open(cfg.PluginsDirs(), filepath.Join(cfg.DataDir(), "logs"))
	defer host.Close()
	byID := map[string]plugin.Status{}
	for _, st := range host.Status() {
		byID[st.Plugin.ID] = st
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tVERSION\tSTATE\tPID\tGRANTS\tHOOKS")
	for _, p := range list {
		state, pid := "stopped", "-"
		if st, ok := byID[p.ID]; ok {
			state, pid = st.State, fmt.Sprintf("%d", st.PID)
			if st.PID == 0 {
				pid = "-"
			}
		}
		if !p.Enabled {
			state, pid = "disabled", "-"
		}
		perms := p.Permissions
		if len(perms) == 0 {
			perms = []string{"(none)"}
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			p.ID, orDash(p.Version), state, pid,
			strings.Join(perms, ","), strings.Join(p.Hooks(), ","))
	}
	w.Flush()
	return nil
}

// pluginCheck validates the manifest tree and fails the command on any
// problem: the scriptable form of doctor's "plugins" check.
func pluginCheck(args []string) error {
	fs := flag.NewFlagSet("plugin check", flag.ContinueOnError)
	if err := parse(fs, args); err != nil {
		return err
	}
	cfg, _, err := sessionConfig()
	if err != nil {
		return err
	}
	count, problems := plugin.Validate(cfg.PluginsDirs()...)
	if len(problems) == 0 {
		fmt.Printf("plugins: %d checked, no problems\n", count)
		return nil
	}
	for _, p := range problems {
		fmt.Printf("error: %s\n", p)
	}
	return fmt.Errorf("plugins: %d problem(s)", len(problems))
}

// scaffoldTOML is the manifest `mtc plugin new` writes: every grant is
// explicit — the host refuses a hook the plugin did not ask for.
const scaffoldTOML = `# Plugin manifest (§8). One folder, one subprocess.
id = "%s"
version = "0.1.0"
enabled = true

# Delivered hooks are gated by these grants; "*" grants everything.
# hooks.session  -> hook.session.context + hook.session.compaction
# hooks.tool     -> hook.tool.execute.before + hook.tool.execute.after
# hooks.permission -> hook.permission.evaluate
# hooks.shell    -> hook.shell.create.before
permissions = ["hooks.*"]

# How to start the subprocess (JSON-RPC over stdin/stdout, one JSON per line).
# cmd = ["bash", "main.sh"]  or  ["go", "run", "."]
cmd = ["python3", "main.py"]

# Seconds one hook call may take (1-60; 0 = 5).
timeout = 5
`

// scaffoldScript is a complete, working plugin: it answers all six hooks,
// so `mtc plugin new demo && mtc doctor` exercises the whole surface.
const scaffoldScript = `import json, sys

# Newline-delimited JSON-RPC 2.0: one request in, one response out.
# Returning no result (or an error) means "not handled" — the host falls
# through to the next plugin, and a crash never breaks the session.
for line in sys.stdin:
    line = line.strip()
    if not line:
        continue
    try:
        req = json.loads(line)
    except Exception:
        continue
    if "method" not in req:
        continue
    mid, method, params = req.get("id"), req["method"], req.get("params") or {}
    result = None

    if method == "hook.session.context":
        # Extra system lines for every request of this session.
        result = {"lines": "[plugin] remember to run go test before committing"}
    elif method == "hook.tool.execute.before":
        # Rewrite the tool input, e.g. force an answer format.
        result = {"input": params.get("input")}
    elif method == "hook.tool.execute.after":
        # Append to the tool output the model reads.
        result = {"output": params.get("output", "")}
    elif method == "hook.permission.evaluate":
        # Only a tightening wins: deny > ask > allow. Answer only on a
        # rule miss, and only when you mean to change the verdict.
        result = {"decision": "allow"}
    elif method == "hook.shell.create.before":
        # Rewrite a command, or veto it: {"deny": true, "reason": "..."}
        result = {"command": params.get("command")}
    elif method == "hook.session.compaction":
        # Replace the built-in fold with your own checkpoint markdown.
        result = None
    else:
        result = None

    if result is None:
        resp = {"jsonrpc": "2.0", "id": mid,
                "error": {"code": -32601, "message": "unhandled"}}
    else:
        resp = {"jsonrpc": "2.0", "id": mid, "result": result}
    sys.stdout.write(json.dumps(resp) + "\n")
    sys.stdout.flush()
`

const scaffoldREADME = `# %s

A matcode plugin: a subprocess speaking newline-delimited JSON-RPC 2.0.

## Protocol

The host writes one request per line:

    {"jsonrpc":"2.0","id":1,"method":"hook.session.context","params":{...}}

You answer on stdout:

    {"jsonrpc":"2.0","id":1,"result":{...}}

No answer, an error, or a crash means "not handled": the host moves on and
the session keeps running. Anything you print that is not valid JSON is
treated as a log line.

## Hooks

| method | params | result |
| --- | --- | --- |
| hook.session.context | sessionID | {"lines": str \| [str]} |
| hook.session.compaction | sessionID, messages | {"checkpoint": str} |
| hook.tool.execute.before | tool, input | {"input": ...} |
| hook.tool.execute.after | tool, input, output | {"output": str} |
| hook.permission.evaluate | tool, input | {"decision": "allow\|ask\|deny"} |
| hook.shell.create.before | command | {"command": str} or {"deny": true, "reason": str} |

Delivery is gated by plugin.toml permissions: a hook you did not grant is
never sent. For permission.evaluate only a tightening is honored — plugins
can never loosen an existing verdict.

## Try it

    mtc plugin check
    mtc doctor
    mtc tui
`
