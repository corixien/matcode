package tui

import (
	"context"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// --- overlay keys (rows 24, 25, 28, 29) ---

// key routes one press into the open overlay and applies the result to
// the app: filter, move, pick, or dismiss.
func (o *overlay) key(k tea.KeyMsg, a *App) (tea.Model, tea.Cmd) {
	// The approval dialog answers with single keystrokes (row 29).
	if o.kind == "ask" {
		switch decodeAsk(k) {
		case "once":
			o.reply(true, false)
			a.overlay = nil
		case "always":
			o.reply(true, true)
			a.overlay = nil
		case "deny":
			o.reply(false, false)
			a.overlay = nil
		}
		return a, nil
	}
	// The API-key dialog is a masked input: type the key, enter
	// verifies it against the provider and stores it in .env, esc cancels.
	if o.kind == "key" {
		switch k.String() {
		case "esc", "ctrl+c":
			a.overlay = nil
		case "enter":
			input := o.query
			name := o.keyProvider
			a.overlay = nil
			if name == "" {
				a.submitProviderKey(input)
			} else {
				a.checkProviderKey(name, input)
			}
		case "backspace":
			o.backspace()
		default:
			if k.Type == tea.KeyRunes || k.String() == " " {
				o.insert(string(k.Runes))
			}
		}
		return a, nil
	}
	// The side question is a small input dialog (row 25): type the
	// question, enter asks, enter/esc dismiss once answered.
	if o.kind == "btw" {
		if o.btwAnswer != "" {
			if k.String() == "enter" || k.String() == "esc" || k.String() == "ctrl+c" {
				a.overlay = nil
			}
			return a, nil
		}
		switch k.String() {
		case "esc", "ctrl+c":
			a.overlay = nil
		case "enter":
			q := strings.TrimSpace(o.query)
			t := a.cur()
			if q == "" || t == nil || o.btwPending {
				return a, nil
			}
			o.btwPending = true
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
				defer cancel()
				reply, err := t.built.Engine.Oneshot(ctx, q)
				a.send(btwResultMsg{text: reply, err: err})
			}()
		case "backspace":
			o.backspace()
		default:
			// Space arrives as KeySpace, not KeyRunes.
			if k.Type == tea.KeyRunes || k.String() == " " {
				o.insert(string(k.Runes))
			}
		}
		return a, nil
	}
	// The file tree and the diff view are scrollable browsers with their
	// own keys (row 35).
	if o.kind == "tree" {
		return a.treeKey(o, k)
	}
	if o.kind == "diff" || o.kind == "mcplog" {
		switch k.String() {
		case "esc", "ctrl+c", "q":
			a.overlay = nil
		case "up":
			o.scrollDiff(-1)
		case "down":
			o.scrollDiff(1)
		case "pgup", "home":
			o.scrollDiff(-10)
		case "pgdown", "end":
			o.scrollDiff(10)
		}
		return a, nil
	}
	switch decodeOverlay(k) {
	case keyUp:
		o.move(-1)
	case keyDown:
		o.move(1)
	case keyBackspace:
		o.backspace()
	case keyClose:
		a.closeOverlay(o)
	case keySelect:
		item := o.chosen()
		a.overlay = nil
		if item != "" {
			// A picked command replaces whatever was typed; attachMention
			// rewrites the composer itself for files.
			if o.kind == "slash" || o.kind == "palette" {
				if t := a.cur(); t != nil {
					t.chat.SetPrompt("")
				}
			}
			return a.overlayPick(o.kind, item)
		}
	case keyRune:
		// A space leaves the command word: hand the query back to the
		// composer so arguments can follow ("/review src/x.go", row 33).
		if o.kind == "slash" && (k.String() == " " || string(k.Runes) == " ") {
			a.handOffSlash(o)
			return a, nil
		}
		o.insert(string(k.Runes))
	}
	return a, nil
}

// handOffSlash closes the /-list and writes what was typed into the
// composer, cursor after a space: from there the keys are ordinary text
// editing and Enter reaches submit with the arguments intact.
func (a *App) handOffSlash(o *overlay) {
	a.overlay = nil
	a.dismissed = ""
	if t := a.cur(); t != nil {
		t.chat.SetPrompt("/" + strings.TrimSpace(o.query) + " ")
	}
}

// closeOverlay dismisses a list overlay, cancelling half-typed command
// entry so the next keypress does not immediately reopen it.
func (a *App) closeOverlay(o *overlay) {
	a.overlay = nil
	t := a.cur()
	if t == nil {
		return
	}
	p := t.chat.Prompt()
	switch o.kind {
	case "slash", "palette":
		if strings.HasPrefix(p, "/") && !strings.ContainsAny(p, " \t") {
			t.chat.SetPrompt("")
		}
	case "mention":
		// Keep the text; remember the token so backspacing does not
		// immediately reopen the picker.
		if tok, _ := mentionQuery(p); tok != "" {
			a.dismissed = tok
		}
	}
}

// reply answers a pending approval without blocking the UI loop.
func (o *overlay) reply(allow, always bool) {
	if o.askReply == nil {
		return
	}
	select {
	case o.askReply <- askAnswer{allow: allow, always: always}:
	default:
	}
}

// overlayPick applies the selected item of a closed overlay.
func (a *App) overlayPick(kind, item string) (tea.Model, tea.Cmd) {
	if strings.HasPrefix(kind, "mcp:") { // per-server action menu (row 46)
		return a.mcpAction(strings.TrimPrefix(kind, "mcp:"), item)
	}
	switch kind {
	case "slash", "palette":
		if id := slashID(item); id != "" {
			return a.runCommand(id)
		}
	case "mention":
		return a.attachMention(item)
	case "mcp":
		return a.openMCPActions(item)
	case "recents":
		if t := a.cur(); t != nil {
			t.chat.SetPrompt(item)
		}
	case "model":
		return a.applyModelChoice(item)
	case "provider":
		a.openProviderKey(item)
		return a, nil
	case "agent":
		return a.applyAgentChoice(item)
	case "theme":
		return a.applyThemeChoice(item)
	case "undo":
		return a.applyUndo(item)
	case "redo":
		return a.applyRedo(item)
	}
	return a, nil
}

// --- openers ---

// openAsk renders the engine's approval request (row 29).
func (a *App) openAsk(m *askRequest) (tea.Model, tea.Cmd) {
	o := &overlay{
		kind: "ask", title: "approval required", theme: a.theme,
		askAction: m.action, askInput: m.input, askReply: m.reply,
	}
	a.overlay = o
	return a, nil
}

// openPalette opens the command palette (ctrl+p, row 24).
func (a *App) openPalette() (tea.Model, tea.Cmd) {
	a.status = ""
	var items []string
	for _, c := range slashCommands() {
		label := "/" + c.ID
		if c.Desc != "" {
			label += "  — " + c.Desc
		}
		items = append(items, label)
	}
	a.overlay = newOverlay("palette", "command palette", items, a.theme)
	return a, nil
}

// openRecents opens the recent-prompt list (ctrl+o, row 26).
func (a *App) openRecents() (tea.Model, tea.Cmd) {
	a.status = ""
	var items []string
	if t := a.cur(); t != nil {
		items = t.chat.Recent()
	}
	if len(items) == 0 {
		a.status = "no recent prompts"
		return a, nil
	}
	a.overlay = newOverlay("recents", "recent prompts", items, a.theme)
	return a, nil
}

// openPicker opens a generic single-column choice list.
func (a *App) openPicker(kind, title string, items []string) (tea.Model, tea.Cmd) {
	a.status = ""
	if len(items) == 0 {
		if kind == "model" {
			a.status = "no provider has an api key yet — /provider to add one"
		} else {
			a.status = "nothing to choose"
		}
		return a, nil
	}
	a.overlay = newOverlay(kind, title, items, a.theme)
	return a, nil
}

// openSlash opens the filtered slash list while typing "/" (row 24).
func (a *App) openSlash(query string) (tea.Model, tea.Cmd) {
	a.status = ""
	var items []string
	for _, c := range slashCommands() {
		items = append(items, c.ID)
	}
	o := newOverlay("slash", "commands", items, a.theme)
	o.query = query
	o.clamp()
	a.overlay = o
	return a, nil
}

// openMention opens fuzzy file search for "@path" (row 22).
func (a *App) openMention(query string) (tea.Model, tea.Cmd) {
	base := query
	if i := strings.IndexByte(base, '#'); i >= 0 {
		base = base[:i]
	}
	o := newOverlay("mention", "attach file", matchFiles(a.cwd, base, 200), a.theme)
	o.query = query
	o.clamp()
	a.overlay = o
	return a, nil
}

// openBtw opens the side-question dialog (row 25): the question is typed
// into the dialog, asked via Oneshot, and the answer never reaches the
// transcript.
func (a *App) openBtw() (tea.Model, tea.Cmd) {
	if a.cur() == nil {
		return a, nil
	}
	a.overlay = newOverlay("btw", "side question (not stored)", nil, a.theme)
	return a, nil
}

// --- choices ---

// slashID maps palette labels ("/compact  — fold…") and plain ids to a
// command id ("" = unknown).
func slashID(s string) string {
	s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "/"))
	if i := strings.Index(s, "—"); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	if id := resolveSlash(slashCommands(), s); id != "" {
		return id
	}
	// resolveSlash needs a query; plain ids pass through it above. As a
	// fallback allow exact ids that resolveSlash's prefix rule missed.
	for _, c := range slashCommands() {
		if c.ID == s {
			return c.ID
		}
	}
	return ""
}
