package tui

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// csiKey applies a key decoded from a CSI-u/modifyOtherKeys sequence.
// alt+enter and enter are re-entered through the normal KeyMsg path;
// shift+enter has no bubbletea KeyType, so it is handled directly.
func (a *App) csiKey(name string) (tea.Model, tea.Cmd) {
	switch name {
	case "alt+enter", "enter":
		return a.onKey(tea.KeyMsg{Type: tea.KeyEnter, Alt: name == "alt+enter"})
	case "shift+enter":
		if a.overlay != nil {
			return a, nil
		}
		if t := a.cur(); t != nil {
			c := t.chat
			c.SetPrompt(c.Prompt() + "\n")
		}
		return a, nil
	}
	return a, nil
}

// cur returns the active tab (nil before one exists).

// onKey routes a key through the leader, overlays, and the active route.
func (a *App) onKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	now := time.Now()
	// Leader chord layer (row 26).
	if a.leader.armed(now) {
		a.leader.clear()
		if id := chord(k.String()); id != "" {
			return a.runCommand(id)
		}
		return a, nil
	}
	if k.String() == LeaderKey {
		a.leader.press(now)
		return a, nil
	}
	// Overlay first (rows 24, 25, 28, 29).
	if a.overlay != nil {
		return a.overlay.key(k, a)
	}
	switch k.String() {
	case "ctrl+c":
		return a.quit()
	case "ctrl+p":
		return a.openPalette()
	case "ctrl+o":
		return a.openRecents()
	case "ctrl+tab", "ctrl+pgdown":
		a.cycleTab(1)
		return a, nil
	case "ctrl+shift+tab", "ctrl+pgup":
		a.cycleTab(-1)
		return a, nil
	case "ctrl+w":
		return a.closeTab()
	// ctrl+shift+t is rarely deliverable (terminals fold it into ctrl+t),
	// so both spellings reopen the last closed tab.
	case "ctrl+shift+t", "ctrl+t":
		return a.reopenTab()
	case "f2":
		a.cycleModel(1)
		return a, nil
	case "shift+tab":
		a.cycleAgent(1)
		return a, nil
	}
	if a.route == "sessions" {
		return a.sessionKey(k)
	}
	if a.route == "settings" {
		if k.String() == "esc" || k.String() == "q" {
			a.route = ""
		}
		return a, nil
	}
	return a.chatKey(k)
}

// quit releases the ask dialog (denying a pending approval) and exits.

// quit releases the ask dialog (denying a pending approval) and exits.
func (a *App) quit() (tea.Model, tea.Cmd) {
	a.closeAsk()
	if a.plugins != nil {
		a.plugins.Close()
	}
	a.quitting = true
	return a, tea.Quit
}

// closeAsk answers a pending approval with a denial so no engine
// goroutine blocks on a dialog that will never render.

// closeAsk answers a pending approval with a denial so no engine
// goroutine blocks on a dialog that will never render.
func (a *App) closeAsk() {
	if a.overlay != nil && a.overlay.kind == "ask" {
		a.overlay.reply(false, false)
		a.overlay = nil
	}
}

// chatKey handles composer keys in the chat route (rows 21–26).

// chatKey handles composer keys in the chat route (rows 21–26).
func (a *App) chatKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	t := a.cur()
	if t == nil {
		return a, nil
	}
	c := t.chat
	switch {
	case k.String() == "esc" && c.Running():
		a.interrupt(t)
		return a, nil
	case isBreakLine(k):
		c.SetPrompt(c.Prompt() + "\n")
		return a, nil
	case isQueued(k):
		if msgs := c.Submit(); len(msgs) > 0 {
			a.startTurn(t, msgs[0])
		}
		return a, nil
	case isSubmit(k):
		return a.submit(t)
	case k.String() == "ctrl+e":
		return a.runCommand("editor")
	case k.String() == "ctrl+u":
		return a.runCommand("undo")
	case k.String() == "ctrl+r":
		return a.runCommand("redo")
	case k.String() == "pgup":
		c.Scroll(-5)
		return a, nil
	case k.String() == "pgdown":
		c.Scroll(5)
		return a, nil
	case k.String() == "up" && c.Prompt() == "":
		// cursor-free composer: arrows scroll the transcript.
		c.Scroll(-1)
		return a, nil
	case k.String() == "down" && c.Prompt() == "":
		c.Scroll(1)
		return a, nil
	}
	// Text editing.
	switch k.String() {
	case "backspace":
		if r := []rune(c.Prompt()); len(r) > 0 {
			c.SetPrompt(string(r[:len(r)-1]))
		}
	case "ctrl+a":
		c.SetPrompt("")
	case "ctrl+k":
		// kill to end of line: the composer is one logical line here.
		c.SetPrompt("")
	default:
		if k.Type == tea.KeyRunes || k.String() == " " {
			c.SetPrompt(c.Prompt() + string(k.Runes))
		}
	}
	// Live overlays while typing: /-commands and @-mentions (rows 22, 24).
	p := c.Prompt()
	if strings.HasPrefix(p, "/") && !strings.ContainsAny(p, " \t") {
		return a.openSlash(strings.TrimPrefix(p, "/"))
	}
	if tok, idx := mentionQuery(p); idx >= 0 && idx+1+len(tok) == len(p) {
		// Escaping out of a mention suppresses it while the token merely
		// shrinks (backspace); a different token reopens the search.
		if a.dismissed != "" && (tok == a.dismissed || strings.HasPrefix(a.dismissed, tok)) {
			return a, nil
		}
		return a.openMention(tok)
	}
	a.dismissed = "" // left the mention; forget the dismissal
	return a, nil
}

// submit commits the composer: slash commands, !shell, or a turn.
