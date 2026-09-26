# matcode — project document

**matcode** (terminal short **`mtc`**) is a coding agent harness: a TUI application, an engine,
and an HTTP API, written in Go. It is an intentional clone of OpenCode's TUI and feature set,
rebuilt with one overriding principle:

> Everything is a file you can open, read, and edit while it runs. No hidden state.

This document explains every part of matcode — what exists, why it exists, and where it lives.

**Status (2026-09-23)**: phases 0 and 1 are done — headless `mtc run` verified end-to-end
against OpenAI- and Anthropic-dialect providers, all builtin tools, JSONL store, permissions,
revert/restore, and the builtin agent roster (§11). Everything else here is the build plan;
§12 tracks phase progress.

---

## 1. Design principles

| # | Principle | Meaning |
|---|-----------|---------|
| 1 | **One thing, one file** | Every tool, skill, agent, theme, command, plugin is a single file (or a single folder for things that need assets). You can `ls` the feature you want to change. |
| 2 | **Data vs. code** | `internal/` is behavior — compiled Go. `data/` (and its runtime counterparts `~/.config/mtc/` and `<project>/.mtc/`) is content — files you edit live, watched and hot-reloaded. |
| 3 | **Folders are truth** | A session is a folder. Its transcript is a JSONL file you can `cat`, `grep`, `diff`, and delete. No database is authoritative; indexes are always rebuildable. |
| 4 | **No junk** | The tree stays minimal: no dead code, no unused abstractions, no speculative config. Every file in `internal/` must be imported; every config key must be read. |
| 5 | **Compatible where free** | File formats that OpenCode already defined well (SKILL.md, theme JSON, agent MD with frontmatter, `/api` URL shapes) are kept byte-compatible so knowledge and tooling transfer. |
| 6 | **Rice by default** | The token-minimal configuration is the *default* configuration, not an opt-in. |

---

## 2. Source tree (the `matcode/` repository)

```
matcode/
├── go.mod
├── matcodeproject.md          # this document
├── cmd/
│   └── mtc/
│       └── main.go            # entrypoint; wires subcommands, nothing else
└── internal/                  # all behavior, one package per concern
    ├── agents/                # builtin agent roster — one .go file each (see §11)
    ├── attach/                # prompt attachments: file:// / path / dir / data: URIs (see §6)
    ├── cli/                   # subcommand dispatch: name → implementation
    ├── commands/              # one .go file per mtc subcommand (run, session, undo, …)
    ├── config/                # loads + merges the data trees, fsnotify hot-reload
    ├── engine/                # session loop: payload, tool dispatch, compaction, events (see §11)
    ├── providers/             # LLM provider layer (see §4)
    ├── references/            # alias → local path or cloned repo (see §3 References)
    ├── skills/                # skills data tree: parse, advertise, load (see §3 Skills)
    ├── mcp/                   # MCP client (see §7)
    ├── tools/                 # builtin tools — one .go file each (see §6)
    ├── permissions/           # allow/ask/deny evaluation
    ├── snapshots/             # per-step file snapshots backing undo/redo
    ├── store/                 # session folders: JSONL read/append, revert, list/export, index
    ├── plugin/                # plugin host + hook bus (see §8)
    ├── server/                # HTTP + SSE API (see §9)
    ├── catalog/               # embedded models.dev snapshot (§4)
    ├── index/                 # bbolt session index + `mtc index rebuild` (§5)
    ├── cmds/                  # data-tree slash commands (row 33)
    ├── app/                   # shared engine assembly: run + API (§9)
    └── tui/                   # the TUI (see §10)
        ├── app.go
        ├── keymap.go
        ├── routes/            # chat, messages, sidebar, image — one file per area
        ├── dialogs.go         # pickers, palette, settings, ask/btw (split landed: keys/turn/commands/tabs/helpers/choices/views/rollback — row 48 done)
        └── theme/             # theme loader for data/themes/*.json
```

All packages exist today, plus `mcp`, `plugin`, `server`, `tui` and the root `install.sh`
(rows 19–36). Component naming (`component/`, `mcp-dialog.go`) that this tree once promised
was folded into `routes/`/`dialogs.go` — tracked as TODO rows, not a silent drop.

Rules for this tree:

- `main.go` contains no logic — only dispatch to subcommands.
- A package exists only if it is imported. If you delete a feature, its package goes with it.
- No `utils/`, no `common/`, no `helpers/`. Shared code lives with the thing that owns it.
- Tests sit next to their package (`engine/session_test.go`), no parallel `test/` tree.

Subcommands implemented so far: `mtc run [-agent a] [-model p/m] [-session ses_…|-continue]
[-format default|json] [-file ref] "…"` (headless one-shot; `-file` repeatable, see §6
Attachments; `-continue`/`-c` resumes the most recently updated session and rejects pairing
with `-session`; `-format json` writes NDJSON events — `session`, `text`, `tool.start`,
`tool.end`, `usage`, `done`, `error`) · `mtc revert <session> <message>` ·
`mtc restore <session> <n>` · `mtc undo <session>` · `mtc redo <session>` ·
`mtc session list [-n] [-format table|json]` · `mtc session delete <id>` ·
`mtc session export [id] [-o file] [-sanitize]` (JSON: meta + messages + side files;
`-sanitize` redacts key/token/password values, known token shapes, PEM private keys and
the home path) · `mtc session import <file|->` (fresh id, original meta/timestamps kept;
flags may sit on either side of positionals) · `mtc stats [-days N] [-models] [-cost]
[-json]` (session.json totals over a window; `-models` adds a per-model table sorted by
spend, `-cost` prints money only) · `mtc models [provider] [-verbose] [-json]` (merged
provider catalog: dialect, base URL, key env name + whether it is set — never the value;
`*` marks the provider of the configured model; the fetched models.dev catalog is phase 8)
· `mtc api [-url base] [-timeout 30s] [-H "k: v"]… [-data body|@file|-] GET|POST <path>`
(one request against the running server; URL defaults to
`http://127.0.0.1:<api_port>`, body to stdout, non-2xx is an error carrying
status + body) · `mtc serve [-host 127.0.0.1] [-port <api_port>]` (the §9 HTTP + SSE API;
loopback bind — the port is the credential in the env-only auth model; Ctrl-C shuts the
listener down cleanly) · `mtc version`. Planned: `mtc tui` (default) ·
`mtc debug [agents|config|paths [selector]] [-json]` (resolved roster / config —
secrets only as env names + set/missing — / labeled data-tree paths; selectors:
home, config, project, data, sessions, logs, skills, db, bin, tmp; bare `debug`
prints the whole path table) · `mtc mcp add|list|rm` (§7) · `mtc plugin new|list|check` · `mtc index rebuild` ·
`mtc doctor` (validates the data tree and reports broken files).

---

## 3. Runtime filesystem — the heart of "open"

Two roots, identical shape; project overrides global file-by-file:

```
~/.config/mtc/          global (all projects)
<project>/.mtc/         project-local (wins on conflict)
├── config.toml         single config file; rice defaults are the built-in defaults
├── AGENTS.md           the one instructions file injected as system text
├── agents/
│   ├── build.md        main agent: frontmatter = model, tools, permissions, hidden; body
│   │                   #   = system prompt — overlays the builtin of the same id (§11)
│   ├── plan.md         planning agent (read-only tools)
│   ├── explore.md      fast search agent
│   ├── title.md        ≤10-line prompt: generate session titles
│   ├── summary.md      ≤10-line prompt: conversation summaries
│   └── compaction.md   ≤10-line prompt: checkpoint builder (fallback only, see §11)
│                       #   the compiled builtin roster lives in internal/agents/ (§11);
│                       #   these files land in phase 5, explore/compaction in phase 5/6
├── skills/
│   └── <name>/SKILL.md        one folder per skill (OpenCode-compatible format);
│                              #   flat skills/<name>.md works too — see "Skills" below
├── themes/
│   └── <name>.json             one JSON per theme (OpenCode-compatible format)
├── commands/
│   └── <name>.md        one file per slash command
├── tools/
│   └── <name>/          one folder per user tool:
│       ├── tool.toml        id, description, JSON-Schema input
│       └── exec.sh          invoked with JSON input on stdin, JSON output on stdout
├── plugins/
│   └── <name>/          one folder per plugin (see §8)
├── mcp.json             MCP servers; secrets referenced only as {env:VAR}
├── permissions.toml     allow / ask / deny rules (code-shaped: functions allowed)
├── sessions/
│   └── <sessionID>/     one folder per session (see §5)
│       ├── session.json     title, agent, model, timestamps, cost, token totals
│       ├── messages.jsonl   append-only transcript — the source of truth
│       ├── title.txt        generated title (TODO row 40 — not written yet)
│       ├── summary.txt      generated summary (TODO row 40 — not written yet)
│       ├── checkpoint.md    current short checkpoint (what the model sees)
│       └── compaction/
│           └── archive.md       every round ever compacted (grows forever)
├── logs/                plugin logs (session logs are winston-free: stderr + API)
└── index.db             REBUILDABLE bbolt index for search — never the truth
```

Not implemented from an earlier draft of this tree (parity audit 2026-09-26): `inbox.json`
(queuing is in-memory in the TUI) and `artifacts/` (attachments inline in messages.jsonl,
large shell output spills to `tmp/shell/`). `checkpoint.md` lives in the session root,
not under `compaction/` — code is the reference here.

### Resolution rules

1. Look in `<project>/.mtc/`, then `~/.config/mtc/`, then built-in defaults — first hit wins.
2. A project file *replaces* the global file; there is no deep merge (except `config.toml`,
   where keys merge individually).
3. The `config` package watches both roots with fsnotify. Save a file → the matching registry
   swaps on the next use. No restart, ever.

### `config.toml` (all keys, defaults in comments)

```toml
model        = "anthropic/claude-sonnet-4-5"   # default model
agent        = "build"                         # default agent: build|plan|summary|title
formatter    = false                           # run ext formatter after write/edit/patch (§6)
auto_compact = true                            # fold at context_limit−4000 (§11)
context_limit = 128000                         # model context window in tokens
api_port      = 8787                           # mtc serve listener; mtc api default target
theme        = "default"                       # or a name from themes/ (phase 5)
[media]                                        # attachments (§6)
auto_resize       = true                       # shrink images >2000px longest edge
max_base64_bytes  = 5242880                    # error above this (5 MiB)
max_file_bytes    = 20971520                   # read cap for file/dir refs (20 MiB)
[ui]                                           # phase 4
show_tokens  = true
editor       = "auto"                          # auto = $VISUAL/$EDITOR/external TUI editor
[providers.<name>]                             # per-provider overrides: dialect, base_url,
api_key      = { env = "MTC_<NAME>_KEY" }      # default_model — secrets only as env references

[references]                                   # named context roots (see below)
docs       = "./docs"                          # string shorthand: local path
api        = { repository = "owner/repo", branch = "main", description = "API spec" }

# permissions are NOT here: they live in permissions.toml (§11)
```

### References

A `[references]` table names a directory the model can attach with `-file ref:<alias>`:

- **Definition** — table with `path` *or* `repository` (mutually exclusive) plus optional
  `branch`, `description`, `hidden`. Unknown keys are a config error.
- **String shorthand** — `docs = "./docs"`: starts with `.`/`/`/`~` ⇒ local path,
  otherwise a git repo (bare `docs` = `owner/repo`-style GitHub shorthand). Repos accept
  GitHub `owner/repo`, `host/path`, https/git/ssh URLs, SCP `user@host:path`; `file:` is rejected.
- **Paths** — relative paths resolve from the project root for `.mtc/config.toml`, from the
  config file's directory for the global file; `~/` expands.
- **Git storage** — checked out under `<data>/repos/<host>/<repo-path>[@<branch>]`; cloned on
  first use, refreshed in the background at most once per 24 h (attempt timestamps persist in
  `repos/refresh.json`, failures append to `repos/refresh.log`). Prompts never wait on the network.
- **Instructions** — every reference with a `description` is appended to the system prompt as
  `alias -> resolved path`; `hidden = true` only removes it from interactive selectors.
- **Attach** — `ref:<alias>` resolves the directory and attaches a non-recursive root listing,
  like any directory attachment.
- **Permissions** — a reference grants nothing: files reached through it are still subject to
  the normal permission rules.

### Skills

`skills/<name>/SKILL.md` — one folder per skill, or a flat `skills/<name>.md` at the source
root (OpenCode V2 format). `internal/skills` discovers both roots, global first and project
second, so a project definition replaces a global one with the same ID.

- **ID from the path** — `<root>/<id>/SKILL.md` at any depth (`skills/teams/api/SKILL.md` ⇒
  `api`), or `<root>/<id>.md`. Frontmatter `name` is only a display label; a root-level
  `SKILL.md` has no folder to name it and is skipped.
- **Frontmatter optional** — `name`, `description`, `metadata.opencode/autoinvoke: false`
  (hides the skill from the model's list). Unknown keys are ignored; nested keys join with
  dots. No description ⇒ loadable by ID, never advertised.
- **`skill` tool** (`internal/tools/skill.go`) registers only when the tree defines at least
  one skill, so the model never sees a dead tool. Its description carries the
  `<available_skills>` block (id, name, description — never a body). Loading returns the body
  without frontmatter, the base directory, and up to 10 supporting file paths; file contents
  stay unread until the skill points at them.
- **Payload** — each loaded skill adds a `## Loaded skills` line to the system prompt (§11
  pipeline), the one part compaction never folds. `action = ["skill"]` rules match the skill
  ID as the resource: a deny hides the skill from the advertisement instead of erroring later.
- **Agents** — `build` (`*`) gets the tool; `plan` does not (§11 table).

---

## 4. Provider layer (`internal/providers`)

**All major providers, one interface.** Every provider implements the same call:

```go
type Provider interface {
    Name() string
    Stream(ctx, req Request) <-chan Chunk     // streaming chat completion
}
```

| Provider | Transport | Notes |
|----------|-----------|-------|
| Anthropic | native messages API | first-class: cache_control, thinking, tool_use blocks |
| OpenAI | chat completions | GPT models |
| Google | Gemini API | |
| **OpenRouter** | OpenAI-compatible | one key, hundreds of models |
| **OpenCode Zen** | OpenAI-compatible | curated hosted endpoints |
| Groq / OpenAI-compat | base-URL override | your existing setups; anything speaking OpenAI dialect |
| xAI / DeepSeek / Mistral | OpenAI-compatible | catalog entries; no default model — pass `provider/model` |

- One `openaiCompat(baseURL, key)` constructor covers OpenRouter, Zen, Groq, xAI, DeepSeek,
  Mistral, and any custom endpoint — no per-service forks. Anthropic has its own dialect file.
- Model catalog: embedded `models.dev` JSON (name, context window, cost, capabilities) merged
  with whatever the provider reports. `mtc doctor` shows which source a model came from. ✅
  (`internal/catalog`, rows 36: prices engine calls when the provider reports no cost,
  seeds the auto-compact window, adds CTX/$ per 1M to `mtc models -verbose`)
- Auth: keys via `{env:VAR}` references only — never a key literal in a file, never printed.

---

## 5. Sessions (`internal/store`) — folders are truth

- A session = `sessions/<sessionID>/`. `sessionID` is a sortable random id (`ses_…`).
- Every message, tool call, tool result, and compaction event is one line of `messages.jsonl`.
- **Append-only**: streaming events are flushed as they complete; a crash loses at most the
  in-flight line.
- Revert (`mtc revert <session> <message>`) = truncate `messages.jsonl` after a marked point;
  the full pre-revert transcript is archived as `messages.jsonl.bak.<n>`.
  `mtc restore <session> <n>` swaps a backup in and archives the current transcript first —
  undoable in both directions.
- Snapshots pair per tool-using assistant step (before-call / after-step) in
  `snapshots.jsonl`; file states live as git-style loose objects under `<data>/objects`
  (tracked + non-ignored untracked ≤2 MiB; git-ignored files and the data dir itself are
  never captured). `mtc undo <session>` reverts the transcript to the message before the
  last tool step and restores that step's before-state; `mtc redo <session>` re-applies the
  archived transcript and the step's after-state. Outside a git worktree, or with
  `snapshots = false` in config.toml, capture stays off and undo/redo move the transcript
  only.
- Compaction rewrites the *context*, never the transcript: history stays complete in JSONL;
  the model only sees `system` + checkpoint + messages since the checkpoint.
- `index.db` (bbolt) maps titles/text → session for fast search. `mtc index rebuild` regenerates
  it from folders at any time. Deleting a folder deletes the session — full stop.

---

## 6. Tools (`internal/tools`) — one Go file per builtin

```
read.go  write.go  edit.go  patch.go  bash.go  grep.go  glob.go
webfetch.go  websearch.go  todowrite.go  question.go  skill.go  mcp.go(dispatch)
```

- Each file exports a `Tool` value: `id`, `description`, input JSON-Schema,
  `execute(ctx, input) → Result{Text, Media}` — Text is the string the model
  reads; Media carries base64 attachments (images/PDF) that persist on the
  store message and are replayed to the provider on resume: OpenAI-dialect
  emits content-part arrays (tool-role images become a follow-up user
  message), Anthropic emits image/document blocks inside the `tool_result`.
- Feature parity checklist (open items) lives in `TODO.md` next to this file.
- `registry.go` merges: **builtins → `data/tools/<name>/` (yours) → MCP-discovered (§7)** —
  last registration wins on name collision (so you can shadow a builtin).
- User tools are *not* compiled: `tool.toml` declares the schema; `exec.sh` receives the JSON
  input on stdin and must print JSON `{ "output": … }` (or exit non-zero). Any language, no
  build step, hot-reloaded like everything else.
- Every tool call passes through `permissions` first (§11) — except under the build agent,
  which bypasses evaluation entirely (§11) — and the plugin `execute.before` /
  `execute.after` hooks (§8).
- **Formatters** (`formatter.go`): off by default; `formatter = true` in config.toml
  (global→project merge, pointer-merged so an explicit `false` overrides) runs an
  ext-matched formatter after a successful write/edit/patch. Ext→argv table with `$FILE`
  (gofmt, ruff|black, prettier, shfmt, rustfmt, clang-format, zig fmt), tried in order,
  first `exec.LookPath` hit that exits 0 wins; unknown ext or no candidate = silent
  no-op — a formatter never fails the edit. `Builtin(workdir, dataDir, format)` threads
  the flag.
- **Attachments** (`internal/attach`, `mtc run -file <ref>` repeatable): refs are
  `file://path?start=N&end=M` (1-based inclusive line slice), a plain path (abs or
  cwd-relative), a directory (non-recursive listing, dirs with trailing `/`), or
  `data:<mime>;base64,…`. Text refs append `--- name ---` (or `name (lines a-b)`) to the
  prompt; media refs (image/PDF, sniffed by content — extension lies) ride as
  `Message.Media` base64, auto-resized (`[media] auto_resize`, PNG/JPEG >2000px longest
  edge, JPEG q85) and capped (`max_base64_bytes` → error, `max_file_bytes` → read cap).
  `Engine.Turn`/`Oneshot` take variadic media; the user message carries it to the wire
  like §6 read-tool media. GIF/PDF/undecodable images pass through unresized.

---

## 7. MCP (`internal/mcp`) — parity with OpenCode

| Feature | Behavior |
|---------|----------|
| Transports | stdio, streamable HTTP (hand-rolled JSON-RPC framing — the official `modelcontextprotocol/go-sdk` was dropped for zero-dep builds); legacy SSE deferred → TODO 43 |
| Config | `mcp.json` (`{"mcp": {name: …}}`); secrets only as `{env:VAR}`; `"enabled": false` is the default posture (rice: nothing extra runs until asked) |
| CLI | `mtc mcp add <name> --command "cmd args" \| --url <https://…> [--env K=VARNAME]… [--header "k: v"]… [-enabled=false] [-global]` (writes project `.mtc/mcp.json`, `--global` the shared one; `--env` stores `{env:VARNAME}` — mcp.json never holds a value) · `mtc mcp list [-json] [-tools] [-global]` (`-tools` dials enabled servers and prints namespaced ids) · `mtc mcp rm <name> [-global]` |
| Discovery | server tools join the registry namespaced `server__tool`; `run` dials enabled servers at start, a failing server warns on stderr and the session continues (§7 lifecycle) |
| Lifecycle | startup timeout (one 30 s budget per build today), reconnect with backoff, `tools_list_changed` notification → live registry swap — all three are TODO row 43 |
| Auth | bearer tokens from env; OAuth2 flow for remote servers → deliberately out of scope (see TODO decisions table) |
| Permissions | MCP tools evaluate the same `permissions.toml` rules as builtins |
| UX | `/mcp` dialog: list servers, enable/disable, restart, view per-server logs — TODO row 46 |

---

## 8. Plugins (`internal/plugin`) — my pick: JSON-RPC subprocesses

**Decision: subprocess plugins, not an embedded interpreter.** Reasons: crash isolation (a
panicking plugin dies, the session lives), language-agnostic (Go, Python, Node, shell all
qualify), no cgo/interpreter version headaches, trivially hot-reloadable by killing the
process.

```
plugins/<name>/
├── plugin.toml     # id, version, enabled, permissions = ["hooks.session", "tools.register", …]
├── main.go         # or main.py / main.sh — declared in plugin.toml
└── README.md       # optional, one paragraph
```

**Protocol**: newline-delimited JSON-RPC 2.0 over stdin/stdout. matcode spawns the process on
enable and sends:

```json
{ "method": "hook.session.compaction", "params": { "sessionID": "ses_…", "messages": [...] } }
```

The plugin answers either `{ "result": { … } }` (skip the default behavior — this is how a
plugin replaces the compaction model call with a deterministic checkpoint) or an error, which
degrades gracefully to default behavior. Plugin failure never breaks a session.

**Hook surface (v1)**:

| Hook | Can do |
|------|--------|
| `session.compaction` | inspect messages → supply checkpoint (your compaction-log port) |
| `session.context` | append system lines to the next request |
| `tool.execute.before` / `.after` | rewrite input / augment output |
| `permission.evaluate` | decide allow/ask/deny alongside `permissions.toml` |
| `shell.create.before` | rewrite or veto a shell command (your "require-approval" idea) |

**Building plugins with the agent itself**: `mtc plugin new <name>` scaffolds the folder from
a template; because everything is a plain file, the coding agent reads/writes plugin code with
its normal edit tool and sees reload results immediately. `plugin.toml.permissions` gates what
a plugin may register; `internal/plugin` refuses anything undeclared.

---

## 9. `/api` (`internal/server`)

HTTP + SSE, URL shapes kept OpenCode-compatible (`/api/session`, `/api/session/{id}/prompt`,
`/api/session/{id}/compact`, `/api/session/{id}/context`, `/api/agent`, `/api/config`, …) so
existing scripts and muscle memory carry over:

- `GET  /api/health` — liveness (`{"ok":true}`)
- `POST /api/session` — create (`{"agent","model"}` optional) → 201 + meta
- `GET  /api/session` — list; `GET /api/session/{id}` — one meta
- `POST /api/session/{id}/prompt` — send (blocking: one turn at a time server-wide;
  body `{"text","agent","model"}` — empty reply streams only via SSE)
- `POST /api/session/{id}/compact` — force compaction
- `GET  /api/session/{id}/message` — transcript
- `GET  /api/session/{id}/context` — the payload the model would see: system text
  (instructions + references + checkpoint + skill guidance), live messages, token estimate
- `GET  /api/event` — SSE stream of everything happening
- `GET  /api/agent`, `/api/skill`, `/api/tool[?agent=id]`, `/api/theme`, `/api/mcp`, `/api/config`

Reads never expose secrets: `/api/config` reports provider `api_key_env` + `api_key_set`
(never the value), `/api/mcp` reports env *names* and header counts only. `mtc serve`
binds loopback (env-only auth scope) and takes `-host`/`-port` (default `api_port`).

Assembly is shared: `internal/app.New` builds agent → provider → permissions → skills →
MCP → engine for both `mtc run` and the prompt/compact handlers, so every surface runs
identical semantics.

The engine is the single semantic core: `internal/app.New` builds agent → provider →
permissions → skills → MCP → engine for every surface. The TUI assembles it in-process
rather than calling this API over HTTP — "TUI is an API client" stayed aspirational
(TODO row 50); semantics are identical because the assembly is shared.

**One event stream under all of them.** The engine exposes `Echo func(string)` (assistant
text deltas) and `Emit func(Event)` with `type` ∈ `session | text | tool.start | tool.end |
compact | usage | done | error` (`internal/engine/events.go`, `Event{Type,Tool,Input,Output,
Text,Usage}`). `mtc run -format json` writes that stream as NDJSON; the SSE endpoint
(`GET /api/event`) and the TUI render the same events, so every surface sees identical
milestones.

---

## 10. TUI (`internal/tui`) — the OpenCode experience

Stack: **bubbletea + lipgloss + glamour** (markdown). `bubbles` and `chroma` were planned
here; the composer is hand-rolled and code highlighting is glamour's — kept for v1 (row 47).

```
tui/
├── app.go            # top-level model: routes + global keymap dispatch
├── keymap.go         # leader/chords/slash/CSI decoding (plain Ctrl bindings live in app.go)
├── routes/           # chat, messages, sidebar, image — one file per area
├── keys/turn/commands/tabs/helpers/choices/views/rollback.go  # split out of app.go/dialogs.go (row 48 done)
├── dialogs.go        # session picker, settings, palette, ask/btw
└── theme/            # themes/*.json → lipgloss styles: flat keys + OpenCode defs/theme format (row 48 done)
```

Parity targets (rows 21–30 **done**, E2E-verified; row 35 TUI-B **done**):

- Streaming assistant text with live markdown, tool-call blocks collapsed by default ✅
- OpenCode default keybindings (picker, palette, sessions, editor-open, revert,
  undo/redo) ✅ — plus `@`/`!` composer modes, `/` list + Ctrl+P, leader `Ctrl+X`
  (N/L/U/R/E/W/M/A + C/T/Q), `Ctrl+O` recents, tabs (`Ctrl+Tab`/`Ctrl+Shift+T`),
  `F2` model cycle, `Shift+Tab` agent cycle, `Alt+Enter` queue, and Shift+Enter newline
  (decode: bubbletea v1 has no CSI-u parser, so `ESC[13;2u` arrives as an unknown-CSI
  message decoded by `keymap.go: csiKeyName`)
- `$EDITOR` deep-open for files (full multi-cursor editing delegated to zed/vim/vscode —
  this is the honest delta vs OpenCode's embedded zed editor; an embedded editor is a
  late-phase optional, not a v1 promise) ✅
- Diff view for edits ✅ (session export `/export`, `/undo`+`/redo` restore), file tree +
  sidebar widgets ✅ (row 35), token/cost footer ✅, theme picker ✅ (JSON themes → row 33)
- `/mcp` dialog: list/enable/disable/restart/per-server logs ✅ (row 46), MCP stderr
  captured per server ✅
- `retry` last turn ✅ (`Ctrl+X y`, row 47); OpenCode-format theme JSONs load ✅ (row 48);
  `compaction` agent fallback when checkpoint extraction throws ✅ (row 49, §11)
- Images: kitty/sixel/iterm2 protocols where the terminal supports them ✅ (row 35)
- Mini mode (`mtc mini`): single-line log + usage, `Ctrl+D` on an empty prompt exits

---

## 11. Engine + permissions + compaction (the invisible part, made visible)

### Agents (`internal/agents`) — one file per agent

| Agent | Selected via | Tools | Permissions | Session |
|-------|-------------|-------|-------------|---------|
| `build` (default) | `-agent build`, `agent = "build"` in config.toml | all builtins (`*`) | **bypassed: never asks, never denies** | yes |
| `plan` | `-agent plan` | read, glob, grep, webfetch, websearch, todowrite — read-only | honors `permissions.toml` | yes |
| `summary` | `-agent summary` | none | — (no tools) | no — stateless |
| `title` | `-agent title` | none | — (no tools) | no — stateless |

- Resolution: `-agent` flag → `config.toml agent` key → `build`.
- Each agent file fixes: role system prompt, tool allowlist (`*` = all builtins), whether
  `AGENTS.md` is prepended to the prompt, whether permissions are bypassed, and whether it
  is stateless.
- Stateless agents run one completion via `Engine.Oneshot`: prompt in, reply out, nothing
  persisted — no session folder, `-session` is rejected. They are the generators for
  §3's `title.txt` / `summary.txt` — wiring that auto-generation is TODO row 40.
- Overlays ✅ (row 32): `agents/*.md` data files with frontmatter (model, tools,
  permissions, steps, hidden, disabled, color, request) replace the compiled prompts per
  id — same overlay rule as tools. `explore` joined as a data-file agent; `compaction`
  (extraction-failure fallback) is TODO row 49.

**Request pipeline** (per user message):

```
payload = system(agent.md body)
        + env line
        + AGENTS.md
        + loaded-skill guidance        # only skills the user/agent loaded
        + session.context plugin lines
        + compaction checkpoint        # § below
        + messages since checkpoint
  → provider.Stream()
  → per tool call: permission.eval → plugin before-hook → execute → after-hook → append JSONL
```

**Permissions** (`permissions.toml`, functions not just strings):

```toml
default   = "allow"          # rice
[[rule]]
action  = ["bash", "webfetch"]   # or "*" — supports wildcards and prefixes
pattern = "rm -rf *"
decision = "deny"
```

Evaluator order: `deny` wins > `ask` > `allow` > default. **Exception (implemented): the
build agent bypasses evaluation entirely — it must never ask for or deny tool access;**
every other agent runs the full evaluation. Plugin hooks run *between* a rule miss and the
default, so plugins can tighten but never loosen a `deny`.

Evaluation is per **resource**, and the strictest result across all of them wins (any deny
beats any ask, any ask beats any allow):

- Every path-ish input field (`path`, `paths`, `workdir`, `directory`, `file_path`; string
  or array) is its own resource, matched against rules by pattern; `input` also contributes
  its text field (`command`, `query`, `prompt`, `url`, `path`, `pattern`) as a non-path
  resource.
- Any resource path that resolves **outside the working directory** is additionally matched
  against rules whose `action = ["external_directory"]`, using the resolved absolute path —
  so one rule can fence off `/etc/*` for every tool at once. `~`, `~/…`, `$HOME` and
  `${HOME}` expand in both patterns and inputs.
- Interactive approvals (TUI prompt, `run` denies headless): scope `once` proceeds without
  persisting; scope `always` appends an `approved = true` allow rule to `permissions.toml`
  matching that exact input. An approved allow outranks a plain `ask` but **never** a
  `deny` — `Approve` refuses while a deny matches, so approvals cannot be used to override
  one. Rule weight: `deny` > approved-`allow` > `ask` > `allow`.

**Compaction** (port of the verified compaction-log design, now built-in):

1. Trigger: `auto_compact` when the assembled payload (system + checkpoint + messages
   after the checkpoint) exceeds `context_limit − 4000`, or an explicit `/compact`
   (`engine.Compact()`; the TUI/API surfacing lands with rows 24/20).
2. Engine walks `messages.jsonl` and extracts every tool call + result deterministically
   (zero LLM): per-tool formatting, `(ERROR)` markers, consecutive-duplicate `×N` collapse,
   caps (80 changes / 5 decisions; objective = first user text, ≤300 chars).
3. Append a round to `sessions/<id>/compaction/archive.md` (`## Overall` index line +
   `### Round n` with Objective/Changes/Decisions) — grows forever.
4. Write `checkpoint.md`: `## Objective`, `## Done` (previous + new, ≤60 bullets),
   `## Next Move` (pending `todowrite` items, else the newest ask), `## Blockers/Facts`
   (verbatim), `## Archive` pointer.
5. The `compaction.md` agent file runs **only if step 2 throws** — the normal path never
   spends a model call. (Rice.) Implemented: `agents/compaction.go` roster entry,
   `Engine.CompactionLLM` hook, `RenderFold` prompt (row 49).
6. Transcript stays append-only: the fold is one `compaction` line whose `through` field
   names the last folded message id. The payload split anchors on `through`, not on the
   event's position, so the keep-tail (≥15 k estimated tokens plus the newest user
   message, which is never folded) preceding the event stays live. The checkpoint rides
   in the system prompt; folded messages are never replayed (§5). Token estimate is a
   heuristic (~4 runes/token), always on.

**Rice defaults baked in** (b2/b3 legacy): one AGENTS.md, allow-all permissions, skills and
MCP silent/off until loaded/enabled, no self-spawned subagents, minimal reads, no narration,
tiny title/summary/compaction prompts, token counter always on.

---

## 12. Build phases

| Phase | Deliverable | Done when | Status |
|-------|-------------|-----------|--------|
| 0 | Spike | `mtc run "list files"` works headless against 2 providers | done (2026-09-23) |
| 1 | Engine core | all builtin tools + JSONL store + permissions + revert + agents (§11) | done (2026-09-23) |
| 2 | MCP | stdio+HTTP transports, registry merge, `/mcp` data served over API | done (legacy SSE via mcp.json `type:"sse"` → row 43) |
| 3 | API | full `/api` + SSE; scripts can drive sessions without TUI | done |
| 4 | TUI-A | daily driver: stream, markdown, pickers, `$EDITOR` open, keymap | done |
| 5 | Openness | hot-reload of every data tree, `mtc doctor`, themes/agents/skills/commands/tools | done (skills watcher → row 37) |
| 6 | Plugins | JSON-RPC host, 5 hooks, scaffolding, compaction-log port as example plugin | done |
| 7 | TUI-B | diff, file tree, palette, images, sidebar widgets | done |
| 8 | Polish | install script, docs, index, parity checklist vs OpenCode | done (2026-09-26; TODO 37–51 closed — 50 skipped for v1, see TODO) |

**Definition of done for "clone"**: every OpenCode TUI feature reachable by keyboard works
identically; every feature of matcode corresponds to exactly one readable file; a stranger can
`find . -type f | sort` and account for every one.

---

## 13. Name

**matcode** — `mtc` in the terminal. ("code" at the back, as requested. Search-verified free;
`forgecode` was rejected as taken.)
