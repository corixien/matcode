# matcode — OpenCode feature parity TODO

Source: OpenCode V2 docs (https://opencode.ai/v2/docs/) + matcodeproject.md.
Ordered step-by-step; implement top-down. Update the status column as items land.
Full checklist lives here; matcodeproject.md is the design spec.

## Engine / tools (no TUI needed)

| # | Feature | OpenCode ref | Status |
|---|---------|--------------|--------|
| 1 | Token/cost usage parsing — engine reads provider `usage` from stream, writes session.json totals | /stats | done |
| 2 | shell tool: `workdir`, `timeout` (ms), `background` (notify on finish), large output → file | /tools/shell | done |
| 3 | read tool: `offset`/`limit` paging (2000 ln / 50 KiB), line-cap 2000, images PNG/JPEG/GIF/WebP + PDF ≤20 MiB | /tools/read | done |
| 4 | webfetch tool: `format` markdown(default)/text/html, timeout ≤120 s | /tools/webfetch | done |
| 5 | question tool: `header`, `multiple`, free-form answers | /tools/question | done |
| 6 | Formatters — off by default; run after write/edit/patch (gofmt, prettier, ruff, shfmt…; ext→cmd table, `$FILE` arg) | /formatters | done |
| 7 | Attachments — prompt inputs with `file://` URI (+`?start&end` lines) / `data:` base64, dir listing, media config (auto-resize, base64 cap); `run -file` repeatable flag | /attachments | done |
| 8 | Snapshots — per-assistant-step file snapshots in internal git object store; `mtc undo`/`mtc redo` = transcript revert + file restore (repo-less: transcript only) | /snapshots | done |
| 9 | References — config map alias→local path or git repo (clone to data dir, 24 h refresh, description→instructions) | /references | done |
| 10 | Permissions: `external_directory` action; ask-approvals `once`/`always` (always persists allow rule, never overrides deny); `~`/`$HOME` path expansion; multi-resource any-deny>any-ask | /permissions | done |
| 11 | Compaction engine (deterministic §11 design): auto token threshold (limit−buffer, keep 15 k tail), checkpoint.md+archive.md, `/compact` trigger | /compaction | done |
| 12 | Skill tool + data tree (`skills/<n>/SKILL.md`) | /skills, /tools/skill | done |

## CLI

| # | Feature | OpenCode ref | Status |
|---|---------|--------------|--------|
| 13 | `run --continue`, `run --format json` (json event/output stream) (`--file` done as part of #7) | /cli/run | done |
| 14 | `mtc session list\|delete\|export\|import` (+`--sanitize`) | /cli/session | done |
| 15 | `mtc stats --days --models --cost --json` (needs #1) | /cli/stats | done |
| 16 | `mtc models` (catalog list; models.dev JSON, phase 8) | /cli/models | done |
| 17 | `mtc api GET\|POST <path>` (talks to running server) | /cli/api | done |
| 18 | `mtc debug agents\|config\|paths` (overlaps `mtc doctor`, phase 8) | /cli/debug | done |

## Phase 2 — MCP
| 19 | `mtc mcp add\|list` (+`--url --env --header --global`), mcp.json, transports, `server__tool` naming, /mcp API | /mcp | done |

## Phase 3 — API
| 20 | full `/api` + SSE (session, prompt, compact, context, message, agent, skill, tool, theme, mcp, config, event) | /api | done |

## Phase 4 — TUI core (incl. input UX)
| 21 | stream + markdown + tool blocks, status/token footer, themes loader, `$EDITOR` open | /cli/tui | done |
| 22 | `@` file search attach with `#start-end` line ranges | /cli/tui#files | done |
| 23 | `!cmd` shell mode (empty prompt → run → output into session) | /cli/tui#shell-mode | done |
| 24 | `/` slash-command list + filter; Ctrl+P command palette | /cli/tui#commands | done |
| 25 | `/btw` side question (not appended to context; dialog answer) | /cli/tui#btw | done |
| 26 | Enter=steal/steer, Shift+Enter newline, Alt+Enter queue; leader Ctrl+X (N/L/U/R/E/W/M/A), Ctrl+O recents | /cli/tui#prompt, #leader | done |
| 27 | Tabs (Ctrl+Tab cycle, Ctrl+Shift+T reopen), F2 model cycle, Shift+Tab agent cycle | /cli/tui#tabs | done |
| 28 | `/undo` `/redo` UI over snapshots (#8) + revert/restore picker | /cli/tui#undo | done |
| 29 | ask-approval dialog (once/always/deny) for `ask` results — headless run keeps denying | /permissions | done |
| 30 | mini mode (`mtc mini`) | /cli/mini | done |

## Phase 5 — Openness (data trees)
| 31 | hot-reload (fsnotify) for all data trees; `mtc doctor` | /cli/config | done |
| 32 | agents as `.md` + frontmatter overlay (model/tools/permissions/steps/hidden/color/disabled/request), `explore` agent, `default_agent` | /agents | done |
| 33 | custom commands (`commands/*.md` → `/name`), themes JSON, user tools (`tools/<n>/tool.toml`+`exec.sh`) merged into registry | /commands, /themes, /tools | done |

## Phase 6 — Plugins
| 34 | JSON-RPC subprocess host, 5 hooks, `mtc plugin new\|list\|check` | /build/plugins | done |

## Phase 7 — TUI-B
| 35 | diff view, file tree, session picker/export, model picker w/ variants, image output (kitty/sixel), sidebar widgets | /cli/tui | done |

## Phase 8 — Polish
| 36 | index.db + `mtc index rebuild`; install script; models.dev catalog; `mtc upgrade`; parity checklist pass | — | done |

## Phase 8 parity audit — open items (spec §1–§12 vs code, 2026-09-26)

Severity: `missing` > `partial` > `cosmetic`; `no-test` = implemented but uncovered. Ref = spec section.

| # | Feature | Severity | Status |
|---|---------|----------|--------|
| 37 | `skills/` not on the fsnotify watcher and not in TUI `onReload` — row 31 "all data trees" overclaims | partial | done |
| 38 | config keys unread (§1 principle 4): `[ui] show_tokens`/`editor` table absent, `media.max_file_bytes` hardcoded not decoded, `references.<alias>.hidden` parsed but never consumed | partial | done |
| 39 | no rice default `model = "anthropic/claude-sonnet-4-5"` (§3): app errors when config omits `model` | missing | done |
| 40 | `title.txt`/`summary.txt` never written; stateless `title`/`summary` agents not wired as generators (§3, §11) | missing | done |
| 41 | payload env line (cwd/OS/date, §11) missing; AGENTS.md prepended before agent body — spec order is body first, then env line, then AGENTS.md | missing | done |
| 42 | Anthropic `cache_control` + `thinking`/`budget_tokens` not sent (§4) | missing | done |
| 43 | MCP lifecycle: reconnect+backoff, `tools_list_changed` → live registry swap, per-server startup timeout, legacy SSE transport (§7) | internal/mcp | done |
| 44 | `mtc mcp --header` stores values verbatim — §4/§7 say secrets are `{env:VAR}` only (the test fixture pins a literal bearer) | partial | done |
| 45 | `/api/mcp` iterates header map values, not names — a literal value would echo unredacted (§9); redaction untested | cosmetic | done (redaction test: mcp_endpoint_test.go) |
| 46 | TUI `/mcp` dialog absent (list/enable/disable/restart/per-server logs); stdio stderr discarded at transport level (§7) | tui/mcpdialog.go | done |
| 47 | `retry` keybinding absent though §10 claims ✅; go.mod stack: no `bubbles`, `chroma` indirect-only (never used for highlighting) (§10) | ctrl+x y | done |
| 48 | DoD "one readable file per feature" (§12): `dialogs.go` holds 8 surfaces, `app.go` >1100 lines — split routes/components; themes also parse only matcode's flat keys, a real OpenCode theme JSON falls back to defaults (§10) → split into `keys/turn/commands/tabs/helpers/choices/views/rollback.go`; `theme/opencode.go` parses `defs`+`theme` (refs, dark/light, ANSI, "none") | done |
| 49 | `compaction` agent + `compaction.md` fallback when checkpoint extraction throws (§11) — extraction failure just errors → `agents/compaction.go` roster entry (data/agents/compaction.md overrides), `Engine.CompactionLLM` hook wired in app, `RenderFold` prompt, panic → agent checkpoint, agent failure still surfaces the extraction error | done |
| 50 | TUI is not an HTTP client of `/api` — shared `app.New` assembly, but spec's "exactly one engine, TUI is a client" is aspirational (§9) | partial | skipped for v1 (architectural, no user-visible change — told user) |
| 51 | no-test cluster for rows marked done: tools (bash/read/webfetch/question/formatter/grep/glob/write/edit/patch/websearch/todowrite), `internal/attach`, `internal/snapshots`, `internal/references`, store `Append`/`Revert`/`AddUsage`, config key loading, provider dialect + media replay, `mtc serve`, MCP-tool permission path | no-test | done |

Spec-behind-code items (stale `component/` tree, §12 status column, hand-rolled JSON-RPC vs go-sdk, checkpoint path) were corrected in `matcodeproject.md` during this pass, not left as rows.

## Deliberately out of scope (spec decisions)

| Feature | Rationale |
|---------|-----------|
| LSP diagnostics | not in matcodeproject.md; add only if asked |
| `execute` JS code-mode tool, `browser` tools | OpenCode-host-specific; matcode is a local TUI |
| subagent tool / mode:subagent agents | rice default: "no self-spawned subagents" (§11) |
| auth login/logout (OAuth) | keys via env refs only (§4) |
| pair / service / web / desktop / acp | matcode scope = TUI + HTTP API + `run` (§2) |
| web search beyond DDG lite (extra providers, proxy, warming, policies, sharing) | decided 2026-09-26: DDG lite stays the only backend; proxy/warming/policy/sharing need network services matcode doesn't have (§2 scope: TUI + loopback API + `run`) |
