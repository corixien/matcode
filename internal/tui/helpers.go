package tui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"matcode/internal/attach"
	"matcode/internal/store"
)

// truncate cuts a string to n display cells.
func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= n {
		return s
	}
	r := []rune(s)
	cells := 0
	for i, ch := range r {
		w := lipgloss.Width(string(ch))
		if cells+w > n-1 {
			return string(r[:i]) + "…"
		}
		cells += w
	}
	return s
}

// errString renders an error for the footer.

// errString renders an error for the footer.
func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// sortStrings sorts in place (kept tiny so app.go avoids extra imports).

// sortStrings sorts in place (kept tiny so app.go avoids extra imports).
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// dedupe drops repeats while keeping order.

// dedupe drops repeats while keeping order.
func dedupe(s []string) []string {
	seen := map[string]bool{}
	out := s[:0]
	for _, v := range s {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// editorCmd resolves the editor binary: [ui] editor from config when set,
// else $VISUAL, $EDITOR, then vi (row 38).

// editorCmd resolves the editor binary: [ui] editor from config when set,
// else $VISUAL, $EDITOR, then vi (row 38).
func (a *App) editorCmd() string {
	if a.cfg != nil {
		if ed := a.cfg.UIEditor; ed != "" && ed != "auto" {
			return ed
		}
	}
	if ed := os.Getenv("VISUAL"); ed != "" {
		return ed
	}
	if ed := os.Getenv("EDITOR"); ed != "" {
		return ed
	}
	return "vi"
}

// openEditor hands the transcript to $EDITOR (row 21 deep-open).

// openEditor hands the transcript to $EDITOR (row 21 deep-open).
func (a *App) openEditor() (tea.Model, tea.Cmd) {
	t := a.cur()
	if t == nil {
		return a, nil
	}
	path := filepath.Join(t.sess.Dir, "transcript.md")
	if err := writeTranscript(path, t.chat.Messages()); err != nil {
		a.status = err.Error()
		return a, nil
	}
	// The editor takes the terminal: suspend the alt screen around it.
	return a, tea.ExecProcess(exec.Command(a.editorCmd(), path), func(error) tea.Msg { return nil })
}

// openFile hands one file to $EDITOR (row 35: the tree's deep-open),
// suspending the alt screen around it.

// openFile hands one file to $EDITOR (row 35: the tree's deep-open),
// suspending the alt screen around it.
func (a *App) openFile(path string) (tea.Model, tea.Cmd) {
	return a, tea.ExecProcess(exec.Command(a.editorCmd(), path), func(error) tea.Msg { return nil })
}

// exportSession writes the session to a file for /export (row 21).

// exportSession writes the session to a file for /export (row 21).
func (a *App) exportSession() (tea.Model, tea.Cmd) {
	t := a.cur()
	if t == nil {
		return a, nil
	}
	path := filepath.Join(a.cwd, t.sess.Meta.ID+".md")
	if err := writeTranscript(path, t.chat.Messages()); err != nil {
		a.status = err.Error()
		return a, nil
	}
	a.status = "exported to " + path
	return a, nil
}

// writeTranscript serializes messages as markdown.

// writeTranscript serializes messages as markdown.
func writeTranscript(path string, msgs []store.Message) error {
	var b strings.Builder
	for _, m := range msgs {
		fmt.Fprintf(&b, "### %s\n\n%s\n\n", m.Role, m.Content)
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// mediaConfig exposes the config's media limits to attach resolution.

// mediaConfig exposes the config's media limits to attach resolution.
func (a *App) mediaConfig() attach.MediaConfig {
	return attach.MediaConfig{
		AutoResize:     a.cfg.Media.AutoResize,
		MaxBase64Bytes: a.cfg.Media.MaxBase64Bytes,
		MaxFileBytes:   a.cfg.Media.MaxFileBytes,
	}
}

// keep the engine import anchored (Ask/Emit hook typing lives above).
