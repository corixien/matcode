package tui

import (
	"os"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"matcode/internal/cmds"
	"matcode/internal/config"
	"matcode/internal/store"
	"matcode/internal/tui/routes"
	"matcode/internal/tui/theme"
)

// TestSlashCommandsIncludeUserCommands: data-tree commands join the
// /-list and the palette after their description, while a file named
// after a builtin stays out of both (row 33).
func TestSlashCommandsIncludeUserCommands(t *testing.T) {
	setUserCmds([]cmds.Command{
		{ID: "review", Description: "review a diff"},
		{ID: "help", Description: "shadow attempt"},
	})
	t.Cleanup(func() { setUserCmds(nil) })

	seen := map[string]string{}
	for _, c := range slashCommands() {
		seen[c.ID] = c.Desc
	}
	if seen["review"] != "review a diff" {
		t.Errorf("user command missing or undetailed: %v", seen)
	}
	if seen["help"] != "show keybindings" {
		t.Errorf("a builtin owns /help: %q", seen["help"])
	}
	if len(seen) != len(slashCommands()) {
		t.Errorf("duplicate ids in the slash list")
	}
}

// TestResolveSlashInput: arguments go to user commands, builtins swallow
// theirs, and unknown names do not resolve.
func TestResolveSlashInput(t *testing.T) {
	setUserCmds([]cmds.Command{
		{ID: "review", Description: "review", Body: "Review $ARGUMENTS."},
	})
	t.Cleanup(func() { setUserCmds(nil) })

	cases := []struct {
		in, wantID, wantArgs string
	}{
		{"/review src/x.go", "review", "src/x.go"},
		{"/rev src/x.go", "review", "src/x.go"}, // prefix match still carries args
		{"/review", "review", ""},
		{"/help", "help", ""},
		{"/su", "compact", ""}, // builtin alias resolution
		{"/nonsense", "", ""},
	}
	for _, tc := range cases {
		cm, args, id, ok := resolveSlashInput(tc.in)
		if tc.wantID == "" {
			if ok {
				t.Errorf("%q resolved to %q, want unknown", tc.in, id)
			}
			continue
		}
		if !ok {
			t.Errorf("%q did not resolve", tc.in)
			continue
		}
		got := id
		if cm.ID != "" {
			got = cm.ID
		}
		if got != tc.wantID || args != tc.wantArgs {
			t.Errorf("%q → id=%q args=%q, want %q/%q", tc.in, got, args, tc.wantID, tc.wantArgs)
		}
	}

	// The builtin path keeps its id, the user path its command.
	if cm, _, id, ok := resolveSlashInput("/help x"); !ok || cm.ID != "" || id != "help" {
		t.Errorf("builtin resolved as cm=%+v id=%q ok=%v", cm, id, ok)
	}
	if cm, args, _, ok := resolveSlashInput("/review x"); !ok || cm.ID != "review" || args != "x" {
		t.Errorf("user command resolved as cm=%+v args=%q ok=%v", cm, args, ok)
	}
}

// TestUserCommandBuiltinWins: `/help.md` on disk cannot hijack /help.
func TestUserCommandBuiltinWins(t *testing.T) {
	setUserCmds([]cmds.Command{{ID: "help", Body: "mine"}, {ID: "review"}})
	t.Cleanup(func() { setUserCmds(nil) })

	if _, ok := userCommand("help"); ok {
		t.Error("the builtin owns help")
	}
	if _, ok := userCommand("quit"); ok {
		t.Error("an alias (quit → exit) is builtin-owned too")
	}
	c, ok := userCommand("review")
	if !ok || c.ID != "review" {
		t.Errorf("review = %+v, %v", c, ok)
	}
}

// TestRunCommandFallsThroughToUserCommand: the palette and the overlay
// route through runCommand, which must reach a data-tree command.
func TestRunCommandFallsThroughToUserCommand(t *testing.T) {
	setUserCmds([]cmds.Command{{ID: "review", Body: "Review $ARGUMENTS."}})
	t.Cleanup(func() { setUserCmds(nil) })

	a := &App{}
	a.runCommand("review")
	// No tab is open, so the command stops at that guard — proof it got in.
	if a.status != "no session" {
		t.Fatalf("status = %q, want the user-command path to run", a.status)
	}
}

// TestSlashOverlayHandsOffOnSpace: while the /-list is open every key
// filters it, so the first space must hand the command word back to the
// composer — otherwise `/hello world` could never carry arguments.
func TestSlashOverlayHandsOffOnSpace(t *testing.T) {
	a := &App{theme: theme.Default}
	a.tabs = []*tab{{chat: routes.NewChat(theme.Default, store.Meta{})}}
	a.tabs[0].chat.SetPrompt("/")
	o := newOverlay("slash", "commands", []string{"hello", "help"}, theme.Default)
	o.query = "hello"
	a.overlay = o

	a.onKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})

	if a.overlay != nil {
		t.Fatal("the slash overlay must close on the first space")
	}
	if got := a.tabs[0].chat.Prompt(); got != "/hello " {
		t.Fatalf("prompt = %q, want the command word plus a space", got)
	}
	// The keys after the hand-off are ordinary text editing.
	a.onKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'w', 'o', 'r', 'l', 'd'}})
	got := a.tabs[0].chat.Prompt()
	if got != "/hello world" {
		t.Fatalf("prompt = %q, want the arguments typed into the composer", got)
	}
	if id2, rest := splitSlash(got); id2 != "hello" || rest != "world" {
		t.Errorf("splitSlash = %q/%q, want hello/world", id2, rest)
	}
}

// TestOnReloadLoadsCommands mirrors the agents-reload contract: a
// command file written after startup appears on the next reload.
func TestOnReloadLoadsCommands(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cwd := t.TempDir()
	mtc := filepath.Join(cwd, ".mtc")
	if err := os.MkdirAll(filepath.Join(mtc, "commands"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mtc, "config.toml"), []byte("theme = \"default\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	body := "---\ndescription: review a diff\nagent: plan\n---\nReview $ARGUMENTS."
	if err := os.WriteFile(filepath.Join(mtc, "commands", "review.md"), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { setUserCmds(nil) })

	cfg, err := config.Load(cwd)
	if err != nil {
		t.Fatal(err)
	}
	a := &App{cfg: cfg, opts: Options{Config: cfg}, cwd: cwd, theme: theme.Default}
	a.tabs = []*tab{{chat: routes.NewChat(theme.Default, store.Meta{})}}

	if _, err := a.onReload(); err != nil {
		t.Fatal(err)
	}
	c, ok := userCommand("review")
	if !ok {
		t.Fatal("onReload must load commands/*.md")
	}
	if c.Description != "review a diff" || c.Agent != "plan" || c.Body != "Review $ARGUMENTS." {
		t.Errorf("loaded = %+v", c)
	}
	if a.status != "config reloaded" {
		t.Errorf("status = %q", a.status)
	}

	// Removing the file clears it on the next reload.
	if err := os.Remove(filepath.Join(mtc, "commands", "review.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := a.onReload(); err != nil {
		t.Fatal(err)
	}
	if _, ok := userCommand("review"); ok {
		t.Error("a deleted command must disappear without a restart")
	}
}

// TestLoadCommandsKeepsPreviousList: one broken file must not wipe the
// commands the user already had.
func TestLoadCommandsKeepsPreviousList(t *testing.T) {
	setUserCmds([]cmds.Command{{ID: "keep", Body: "keep it"}})
	t.Cleanup(func() { setUserCmds(nil) })

	cwd := t.TempDir()
	mtc := filepath.Join(cwd, ".mtc")
	if err := os.MkdirAll(filepath.Join(mtc, "commands"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mtc, "commands", "broken.md"),
		[]byte("---\ndescription: x\n---\n   "), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cwd)
	if err != nil {
		t.Fatal(err)
	}
	a := &App{cfg: cfg, cwd: cwd, theme: theme.Default}
	if err := a.loadCommands(); err == nil {
		t.Fatal("an empty body must be reported")
	}
	if _, ok := userCommand("keep"); !ok {
		t.Error("the previous list must survive a failed load")
	}
}
