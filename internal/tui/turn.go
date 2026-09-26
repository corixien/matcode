package tui

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"matcode/internal/store"
	"matcode/internal/tui/routes"
)

// submit commits the composer: slash commands, !shell, or a turn.
func (a *App) submit(t *tab) (tea.Model, tea.Cmd) {
	c := t.chat
	text := c.Prompt()
	a.dismissed = ""
	switch {
	case strings.HasPrefix(text, "/"):
		cm, args, id, ok := resolveSlashInput(text)
		c.SetPrompt("")
		if !ok {
			a.status = "unknown command: " + text
			return a, nil
		}
		if cm.ID != "" {
			return a.runUserCommand(cm, args)
		}
		return a.runCommandArgs(id, args)
	case strings.HasPrefix(text, "!"):
		c.SetPrompt("")
		return a.runShell(t, strings.TrimSpace(strings.TrimPrefix(text, "!")))
	}
	if msgs := c.Submit(); len(msgs) > 0 {
		c.PushRecent(text)
		a.startTurn(t, msgs[0])
	}
	return a, nil
}

// startTurn launches the engine turn on a goroutine; Echo/Emit stream
// back onto the loop and turnDoneMsg ends it. The user message shows in
// the transcript immediately and is persisted by the engine.

// startTurn launches the engine turn on a goroutine; Echo/Emit stream
// back onto the loop and turnDoneMsg ends it. The user message shows in
// the transcript immediately and is persisted by the engine.
func (a *App) startTurn(t *tab, msg store.Message) tea.Cmd {
	if t.turn != nil {
		return nil
	}
	turn := make(chan struct{})
	t.turn = turn
	media := a.pendingMedia
	a.pendingMedia = nil
	t.chat.Update(&routes.AppendMsg{Msg: store.Message{Role: "user", Content: msg.Content}})
	t.chat.Update(&routes.TurnStartMsg{})
	eng := t.built.Engine
	ctx, cancel := context.WithCancel(context.Background())
	t.cancel = cancel
	go func() {
		// onTurnDone owns closing t.turn; this goroutine only reports.
		err := eng.Turn(ctx, msg.Content, media...)
		a.send(turnDoneMsg{tab: t, err: errString(err)})
	}()
	return nil
}

// startRetry replays the last prompt (row 47): the engine first drops
// the previous answer(s) from the transcript (PrepareRetry, with a
// revert backup), the view reloads to the kept prompt, and the turn
// then runs exactly like a fresh one — without re-appending the prompt.

// startRetry replays the last prompt (row 47): the engine first drops
// the previous answer(s) from the transcript (PrepareRetry, with a
// revert backup), the view reloads to the kept prompt, and the turn
// then runs exactly like a fresh one — without re-appending the prompt.
func (a *App) startRetry(t *tab) tea.Cmd {
	if t.turn != nil {
		a.status = "a turn is already running"
		return nil
	}
	eng := t.built.Engine
	if _, err := eng.PrepareRetry(); err != nil {
		a.status = err.Error()
		return nil
	}
	if err := t.reload(); err != nil {
		a.status = err.Error()
		return nil
	}
	t.chat.Update(&routes.TurnStartMsg{})
	turn := make(chan struct{})
	t.turn = turn
	ctx, cancel := context.WithCancel(context.Background())
	t.cancel = cancel
	go func() {
		err := eng.Retry(ctx)
		a.send(turnDoneMsg{tab: t, err: errString(err)})
	}()
	return nil
}

// loadCommands re-reads commands/*.md from the data tree (row 33) and
// publishes it for the /-list and the palette.

// onTurnDone flushes the queued prompt and refreshes the transcript.
func (a *App) onTurnDone(m turnDoneMsg) (tea.Model, tea.Cmd) {
	t := m.tab
	if t == nil {
		return a, nil
	}
	if t.cancel != nil {
		t.cancel()
		t.cancel = nil
	}
	t.chat.Update(&routes.TurnDoneMsg{Err: m.err})
	if t.turn != nil {
		close(t.turn)
		t.turn = nil
	}
	// Reload the persisted transcript so stored tool messages appear.
	if err := t.reload(); err != nil {
		a.status = err.Error()
	}
	// Flush a prompt queued mid-turn (row 26 alt+enter).
	if msgs := t.chat.Submit(); len(msgs) > 0 {
		a.startTurn(t, msgs[0])
	}
	return a, nil
}

// interrupt cancels a running turn.

// interrupt cancels a running turn.
func (a *App) interrupt(t *tab) {
	if t.cancel != nil {
		t.cancel()
	}
}

// runShell executes !cmd and appends its output as a tool result (row 23).
// The pair (assistant tool call, tool result) keeps the provider wire
// format legal: a tool message never stands alone.

// runShell executes !cmd and appends its output as a tool result (row 23).
// The pair (assistant tool call, tool result) keeps the provider wire
// format legal: a tool message never stands alone.
func (a *App) runShell(t *tab, cmdline string) (tea.Model, tea.Cmd) {
	if cmdline == "" {
		return a, nil
	}
	go func() {
		cmd := exec.Command("sh", "-c", cmdline)
		cmd.Dir = a.cwd
		out, err := cmd.CombinedOutput()
		text := strings.TrimRight(string(out), "\n")
		if err != nil {
			if text != "" {
				text += "\n"
			}
			text += err.Error()
		}
		callID := fmt.Sprintf("shell_%d", time.Now().UnixNano())
		call := store.ToolCall{ID: callID, Name: "shell", Arguments: cmdline}
		assistant := store.Message{Role: "assistant", ToolCalls: []store.ToolCall{call}}
		tool := store.Message{Role: "tool", Content: text, ToolCallID: callID}
		if err := t.sess.Append(&assistant); err != nil {
			a.status = err.Error()
			return
		}
		if err := t.sess.Append(&tool); err != nil {
			a.status = err.Error()
			return
		}
		a.send(&routes.AppendMsg{Msg: assistant})
		a.send(&routes.AppendMsg{Msg: tool})
	}()
	return a, nil
}

// --- tabs (row 27) ---

// cycleTab moves the selection (ctrl+tab).
