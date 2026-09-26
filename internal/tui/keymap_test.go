package tui

import (
	"fmt"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// fakeCSI mirrors bubbletea's unexported unknownCSISequenceMsg.String()
// ("?CSI%+v?" over bytes[2:]) so the decoder is testable.
type fakeCSI []byte

func (f fakeCSI) String() string { return fmt.Sprintf("?CSI%+v?", []byte(f)[2:]) }

func TestCSIRawBytes(t *testing.T) {
	// The decoder reassembles bytes[2:], so fakeCSI is built over the
	// whole sequence: ESC [ 13 ; 2 u.
	seq := append([]byte("\x1b["), []byte("13;2u")...)
	if got := csiKeyName(fakeCSI(seq)); got != "shift+enter" {
		t.Fatalf("csi-u shift+enter = %q, want shift+enter", got)
	}
}

func TestCSIKeyName(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"\x1b[13;2u", "shift+enter"},
		{"\x1b[13;3u", "alt+enter"},
		{"\x1b[13;5u", "ctrl+enter"},
		{"\x1b[13;6u", "ctrl+shift+enter"},
		{"\x1b[13u", "enter"},
		{"\x1b[13;1u", "enter"},
		{"\x1b[27;2;13~", "shift+enter"}, // xterm modifyOtherKeys
		{"\x1b[27;3;13~", "alt+enter"},
		{"\x1b[9;2u", "shift+tab"},
		{"\x1b[999;2u", ""}, // unmapped code
		{"\x1b[13;2x", ""},  // not a CSI-u/modifyOtherKeys final byte
	}
	for _, c := range cases {
		if got := csiKeyName(fakeCSI(append([]byte("\x1b["), []byte(c.raw[2:])...))); got != c.want {
			t.Errorf("csiKeyName(%q) = %q, want %q", c.raw, got, c.want)
		}
	}
}

func TestCSIKeyNameIgnoresOtherMsgs(t *testing.T) {
	if got := csiKeyName(tea.KeyMsg{Type: tea.KeyEnter}); got != "" {
		t.Fatalf("KeyMsg decoded as %q, want \"\"", got)
	}
	if got := csiKeyName("plain string"); got != "" {
		t.Fatalf("string decoded as %q, want \"\"", got)
	}
}

func TestIsSubmitQueueBreak(t *testing.T) {
	enter := tea.KeyMsg{Type: tea.KeyEnter}
	altEnter := tea.KeyMsg{Type: tea.KeyEnter, Alt: true}
	if !isSubmit(enter) || isQueued(enter) || isBreakLine(enter) {
		t.Fatal("plain enter misclassified")
	}
	if !isQueued(altEnter) || isSubmit(altEnter) {
		t.Fatal("alt+enter misclassified")
	}
}
