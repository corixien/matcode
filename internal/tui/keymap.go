// Package tui implements the interactive terminal UI (spec §10): one
// bubbletea app routing chat, session list, settings, and dialog layers.
package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"matcode/internal/cmds"
)

// LeaderKey starts the chord layer (ctrl+x): pressed alone it arms the
// leader for LeaderTimeout, then the next key runs its mapped command.
const LeaderKey = "ctrl+x"

// LeaderTimeout bounds how long the leader stays armed (config
// tui.json leader_timeout, default 2000ms).
const LeaderTimeout = 2 * time.Second

// leaderChord is one leader-mapped command (spec §10 row 26).
type leaderChord struct {
	key string // key after the leader
	id  string // command id
}

// leaderChords maps the documented ctrl+x sequence: n new, l sessions,
// u undo, r redo, e editor, w quit, m models, a agent, c compact,
// x export, t themes, q quit, y retry (row 47).
var leaderChords = []leaderChord{
	{"n", "new"}, {"l", "sessions"}, {"u", "undo"}, {"r", "redo"},
	{"e", "editor"}, {"w", "quit"}, {"q", "quit"}, {"m", "models"},
	{"a", "agent"}, {"c", "compact"}, {"x", "export"}, {"t", "themes"},
	{"s", "sidebar"}, {"f", "tree"}, {"d", "diff"}, {"y", "retry"},
}

// chord resolves a post-leader key to a command id ("" = unmapped).
func chord(k string) string {
	for _, c := range leaderChords {
		if c.key == k {
			return c.id
		}
	}
	return ""
}

// leaderState tracks the armed window.
type leaderState struct {
	on bool
	at time.Time
}

// press records a leader press.
func (l *leaderState) press(now time.Time) { l.on, l.at = true, now }

// armed reports whether the leader chord layer is currently active.
func (l *leaderState) armed(now time.Time) bool {
	if !l.on {
		return false
	}
	if now.Sub(l.at) > LeaderTimeout {
		l.on = false
		return false
	}
	return true
}

// clear disarms the leader.
func (l *leaderState) clear() { l.on = false }

// --- picker / palette keys (rows 24, 26) ---

// overlayKey is what an overlay accepts: navigate, filter, select, close.
type overlayKey int

const (
	keyNone overlayKey = iota
	keyUp
	keyDown
	keySelect
	keyClose
	keyRune
	keyBackspace
)

// decodeOverlay maps a key press to an overlay action.
func decodeOverlay(k tea.KeyMsg) overlayKey {
	switch k.String() {
	case "up", "ctrl+k":
		return keyUp
	case "down", "ctrl+j":
		return keyDown
	case "enter":
		return keySelect
	case "esc", "ctrl+c":
		return keyClose
	case "backspace":
		return keyBackspace
	}
	if k.Type == tea.KeyRunes || k.String() == " " {
		return keyRune
	}
	return keyNone
}

// decodeAsk maps a key press to an ask-dialog verdict ("" = ignore).
func decodeAsk(k tea.KeyMsg) string {
	switch k.String() {
	case "y", "enter":
		return "once"
	case "a":
		return "always"
	case "n", "esc":
		return "deny"
	}
	return ""
}

// SlashCommand is one entry of the /-list (spec §10 row 24): the id routes,
// aliases match the documented shortcut names, and key shows the binding.
type SlashCommand struct {
	ID      string
	Aliases []string
	Desc    string
	Key     string // documented leader shortcut, "" when none
}

// slashCommands is the built-in slash list followed by the data-tree
// commands (commands/*.md, row 33). A user file whose id collides with a
// builtin (or a builtin alias) stays out of the list: the builtin owns it.
func slashCommands() []SlashCommand {
	out := builtinSlashCommands()
	for _, c := range userCmds {
		if builtinSlashID(c.ID) {
			continue
		}
		out = append(out, SlashCommand{ID: c.ID, Desc: c.Description})
	}
	return out
}

// userCmds is the loaded command tree; New and onReload replace it
// wholesale so a file write shows up without a restart.
var userCmds []cmds.Command

// setUserCmds swaps the user command list (nil clears it).
func setUserCmds(list []cmds.Command) { userCmds = list }

// userCommand resolves an id against the data tree. Ids a builtin owns
// resolve to nothing, which is how `/help.md` loses to `/help`.
func userCommand(id string) (cmds.Command, bool) {
	if id == "" || builtinSlashID(id) {
		return cmds.Command{}, false
	}
	return cmds.Find(userCmds, id)
}

// builtinSlashID reports whether the builtin list owns an id or alias.
func builtinSlashID(id string) bool {
	for _, c := range builtinSlashCommands() {
		if c.ID == id {
			return true
		}
		for _, a := range c.Aliases {
			if a == id {
				return true
			}
		}
	}
	return false
}

// builtinSlashCommands is the compiled-in list.
func builtinSlashCommands() []SlashCommand {
	return []SlashCommand{
		{ID: "agent", Desc: "switch agent", Key: "ctrl+x a"},
		{ID: "btw", Desc: "side question: answered without touching context"},
		{ID: "compact", Desc: "fold the transcript", Aliases: []string{"summarize"}, Key: "ctrl+x c"},
		{ID: "details", Desc: "session details"},
		{ID: "diff", Desc: "unified diff of the last tool step", Key: "ctrl+x d"},
		{ID: "editor", Desc: "open $EDITOR", Key: "ctrl+x e"},
		{ID: "exit", Desc: "quit", Aliases: []string{"quit"}},
		{ID: "export", Desc: "write the session to a file", Key: "ctrl+x x"},
		{ID: "help", Desc: "show keybindings"},
		{ID: "mcp", Desc: "mcp servers: list, enable, restart, logs"},
		{ID: "models", Desc: "switch model", Key: "ctrl+x m"},
		{ID: "new", Desc: "start a new session", Key: "ctrl+x n"},
		{ID: "recents", Desc: "reuse a recent prompt", Key: "ctrl+o"},
		{ID: "retry", Desc: "drop the last answer and re-run its prompt", Key: "ctrl+x y"},
		{ID: "redo", Desc: "reapply an undone step", Key: "ctrl+x r"},
		{ID: "sessions", Desc: "browse sessions", Aliases: []string{"ls"}, Key: "ctrl+x l"},
		{ID: "settings", Desc: "inspect this session's settings"},
		{ID: "sidebar", Desc: "toggle the widget sidebar", Key: "ctrl+x s"},
		{ID: "themes", Desc: "switch theme", Key: "ctrl+x t"},
		{ID: "tree", Desc: "browse the working tree", Key: "ctrl+x f"},
		{ID: "undo", Desc: "revert the last tool step", Key: "ctrl+x u"},
	}
}

// splitSlash separates the first word of a slash line from its arguments:
// "/review src/x.go" → ("review", "src/x.go"). Arguments only reach the
// data-tree commands, whose body expands them through cmds.Expand.
func splitSlash(s string) (id, args string) {
	s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "/"))
	if i := strings.IndexAny(s, " \t"); i >= 0 {
		return s[:i], strings.TrimSpace(s[i:])
	}
	return s, ""
}

// resolveSlashInput parses a typed slash line ("/review src/x.go", the
// palette label, or a bare "/help"): it yields the data-tree command with
// its arguments, or the builtin id when the builtin list owns the name.
// ok is false for an unknown command.
func resolveSlashInput(text string) (cm cmds.Command, args string, id string, ok bool) {
	id, args = splitSlash(text)
	rid := resolveSlash(slashCommands(), id)
	if rid == "" {
		return cmds.Command{}, "", "", false
	}
	if c, found := userCommand(rid); found {
		return c, args, "", true
	}
	return cmds.Command{}, args, rid, true
}

// matchSlash reports whether cmd matches query (id or alias, prefix match).
func matchSlash(cmd SlashCommand, query string) bool {
	if query == "" {
		return true
	}
	if strings.HasPrefix(cmd.ID, query) {
		return true
	}
	for _, a := range cmd.Aliases {
		if strings.HasPrefix(a, query) {
			return true
		}
	}
	return false
}

// filterSlash returns the commands matching query in declaration order.
func filterSlash(cmds []SlashCommand, query string) []SlashCommand {
	var out []SlashCommand
	for _, c := range cmds {
		if matchSlash(c, query) {
			out = append(out, c)
		}
	}
	return out
}

// resolveSlash maps typed text ("/su" or "su") to a command id ("" = no
// match). Exact id/alias matches win over prefix matches.
func resolveSlash(cmds []SlashCommand, text string) string {
	text = strings.TrimPrefix(strings.TrimSpace(text), "/")
	if text == "" {
		return ""
	}
	for _, c := range cmds {
		if c.ID == text {
			return c.ID
		}
		for _, a := range c.Aliases {
			if a == text {
				return c.ID
			}
		}
	}
	matches := filterSlash(cmds, text)
	if len(matches) == 1 {
		return matches[0].ID
	}
	return ""
}

// isSubmit reports whether the key sends/steals the pending prompt.
func isSubmit(msg tea.KeyMsg) bool { return msg.String() == "enter" }

// isQueued reports alt+enter: queue the prompt behind the running turn.
func isQueued(msg tea.KeyMsg) bool { return msg.String() == "alt+enter" }

// isBreakLine reports shift+enter: a newline in the composer. Terminals
// that speak CSI-u (kitty, foot, WezTerm, xterm modifyOtherKeys) send it
// as ESC[13;2u, which bubbletea v1 (no CSI-u parser) reports as an
// unknown CSI message — see csiKeyName; the fallback when a terminal
// sends nothing distinguishable is alt+enter.
func isBreakLine(msg tea.KeyMsg) bool { return msg.String() == "shift+enter" }

// csiKeyName decodes a bubbletea unknownCSISequenceMsg — String() looks
// like "?CSI[49 51 59 50 117]?" for ESC[13;2u — into a key name such as
// "shift+enter". Returns "" for anything it does not recognise.
func csiKeyName(msg tea.Msg) string {
	s, ok := msg.(fmt.Stringer)
	if !ok {
		return ""
	}
	out := s.String()
	if !strings.HasPrefix(out, "?CSI") || !strings.HasSuffix(out, "?") {
		return ""
	}
	body := strings.TrimSuffix(strings.TrimPrefix(out, "?CSI"), "?")
	body = strings.TrimSuffix(strings.TrimPrefix(body, "["), "]")
	raw := make([]byte, 0, 8)
	for _, f := range strings.Fields(body) {
		n, err := strconv.Atoi(f)
		if err != nil || n < 0 || n > 255 {
			return ""
		}
		raw = append(raw, byte(n))
	}
	seq := string(raw)
	if strings.HasSuffix(seq, "u") { // CSI-u: <code>;<modifier>u
		return csiUName(strings.TrimSuffix(seq, "u"))
	}
	if strings.HasSuffix(seq, "~") { // xterm modifyOtherKeys: 27;<mod>;<code>~
		parts := strings.Split(strings.TrimSuffix(seq, "~"), ";")
		if len(parts) == 3 && parts[0] == "27" {
			return csiUName(parts[2] + ";" + parts[1])
		}
	}
	return ""
}

// csiUName maps CSI-u params ("13;2") to a bubbletea-style key name.
// The modifier is 1 + bitmask (shift=1, alt=2, ctrl=4); code 13 is CR.
func csiUName(params string) string {
	parts := strings.Split(params, ";")
	code, err := strconv.Atoi(parts[0])
	if err != nil {
		return ""
	}
	base := map[int]string{13: "enter", 27: "esc", 9: "tab", 127: "backspace"}[code]
	if base == "" {
		return ""
	}
	mod := 1
	if len(parts) > 1 {
		if m, err := strconv.Atoi(parts[1]); err == nil && m > 0 {
			mod = m
		}
	}
	bits := mod - 1
	var name string
	if bits&4 != 0 {
		name += "ctrl+"
	}
	if bits&2 != 0 {
		name += "alt+"
	}
	if bits&1 != 0 {
		name += "shift+"
	}
	return name + base
}
