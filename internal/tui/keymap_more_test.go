package tui

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestChordMaps(t *testing.T) {
	for key, want := range map[string]string{
		"n": "new", "l": "sessions", "u": "undo", "r": "redo",
		"e": "editor", "w": "quit", "q": "quit", "m": "models",
		"a": "agent", "c": "compact", "x": "export", "t": "themes",
	} {
		if got := chord(key); got != want {
			t.Errorf("chord(%q) = %q, want %q", key, got, want)
		}
	}
	if got := chord("z"); got != "" {
		t.Errorf("chord(z) = %q, want \"\"", got)
	}
}

func TestLeaderArming(t *testing.T) {
	var l leaderState
	now := time.Now()
	if l.armed(now) {
		t.Fatal("fresh leader armed")
	}
	l.press(now)
	if !l.armed(now.Add(LeaderTimeout - time.Millisecond)) {
		t.Fatal("leader disarmed before timeout")
	}
	if l.armed(now.Add(LeaderTimeout + time.Millisecond)) {
		t.Fatal("leader still armed after timeout")
	}
	l.press(now)
	l.clear()
	if l.armed(now) {
		t.Fatal("leader armed after clear")
	}
}

func TestDecodeOverlay(t *testing.T) {
	cases := []struct {
		key  tea.KeyMsg
		want overlayKey
	}{
		{tea.KeyMsg{Type: tea.KeyUp}, keyUp},
		{tea.KeyMsg{Type: tea.KeyDown}, keyDown},
		{tea.KeyMsg{Type: tea.KeyCtrlK}, keyUp},
		{tea.KeyMsg{Type: tea.KeyCtrlJ}, keyDown},
		{tea.KeyMsg{Type: tea.KeyEnter}, keySelect},
		{tea.KeyMsg{Type: tea.KeyEsc}, keyClose},
		{tea.KeyMsg{Type: tea.KeyBackspace}, keyBackspace},
		{tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")}, keyRune},
		{tea.KeyMsg{Type: tea.KeySpace}, keyRune},
		{tea.KeyMsg{Type: tea.KeyTab}, keyNone},
		{tea.KeyMsg{Type: tea.KeyShiftTab}, keyNone},
	}
	for _, c := range cases {
		if got := decodeOverlay(c.key); got != c.want {
			t.Errorf("decodeOverlay(%q) = %v, want %v", c.key.String(), got, c.want)
		}
	}
}

func TestDecodeAsk(t *testing.T) {
	cases := []struct {
		key  string
		want string
	}{
		{"y", "once"}, {"enter", "once"},
		{"a", "always"},
		{"n", "deny"}, {"esc", "deny"},
		{"q", ""}, {"ctrl+c", ""}, {"tab", ""},
	}
	for _, c := range cases {
		// Build the key from its name the way bubbletea reports it.
		k := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(c.key)}
		if c.key == "enter" {
			k = tea.KeyMsg{Type: tea.KeyEnter}
		}
		if c.key == "esc" {
			k = tea.KeyMsg{Type: tea.KeyEsc}
		}
		if got := decodeAsk(k); got != c.want {
			t.Errorf("decodeAsk(%q) = %q, want %q", c.key, got, c.want)
		}
	}
}

func TestResolveSlash(t *testing.T) {
	cmds := slashCommands()
	cases := []struct{ text, want string }{
		{"/compact", "compact"},   // exact id
		{"compact", "compact"},    // bare id
		{"/su", "compact"},        // unique alias prefix
		{"/summ", "compact"},      // alias prefix
		{"/sessions", "sessions"}, // exact id
		{"/ls", "sessions"},       // exact alias
		{"/z", ""},                // no match
		{"/", ""},                 // empty query
		{"/e", ""},                // ambiguous: editor, export, exit
	}
	for _, c := range cases {
		if got := resolveSlash(cmds, c.text); got != c.want {
			t.Errorf("resolveSlash(%q) = %q, want %q", c.text, got, c.want)
		}
	}
}

func TestFilterSlash(t *testing.T) {
	got := filterSlash(slashCommands(), "mo")
	if len(got) != 1 || got[0].ID != "models" {
		t.Fatalf("filterSlash(mo) = %v, want [models]", got)
	}
	if all := filterSlash(slashCommands(), ""); len(all) != len(slashCommands()) {
		t.Fatalf("empty query returned %d of %d", len(all), len(slashCommands()))
	}
}

func TestSlashID(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/compact", "compact"},
		{"compact", "compact"},
		{"/compact  — fold the transcript", "compact"},
		{"  /help  ", "help"},
		{"/nope", ""},
	}
	for _, c := range cases {
		if got := slashID(c.in); got != c.want {
			t.Errorf("slashID(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestMentionQuery(t *testing.T) {
	cases := []struct {
		text  string
		want  string
		wantI int
	}{
		{"read @file", "file", 5},
		{"@src", "src", 0},
		{"mail me@example.com", "", -1}, // @ inside a word
		{"look @", "", -1},              // empty token
		{"@a b", "", -1},                // token ended by space
		{"nothing here", "", -1},
	}
	for _, c := range cases {
		tok, idx := mentionQuery(c.text)
		if tok != c.want || idx != c.wantI {
			t.Errorf("mentionQuery(%q) = (%q,%d), want (%q,%d)", c.text, tok, idx, c.want, c.wantI)
		}
	}
}
