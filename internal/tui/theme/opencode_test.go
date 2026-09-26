package theme

import "testing"

// The nord example shape from the OpenCode theme docs: defs + refs +
// dark/light variants + "none".
const openNord = `{
  "$schema": "https://opencode.ai/theme.json",
  "defs": {"nord0": "#2E3440", "nord3": "#4C566A", "nord4": "#D8DEE9",
           "nord8": "#88C0D0", "nord9": "#81A1C1", "nord11": "#BF616A"},
  "theme": {
    "primary":   {"dark": "nord8", "light": "nord9"},
    "error":     {"dark": "nord11", "light": "nord11"},
    "text":      {"dark": "nord4", "light": "nord0"},
    "textMuted": {"dark": "nord3", "light": "nord0"},
    "background":      {"dark": "nord0", "light": "nord4"},
    "backgroundPanel": {"dark": "#3B4252", "light": "nord4"},
    "border":     {"dark": "nord9", "light": "nord3"},
    "info":       "none"
  }
}`

func TestParseOpenCodeTheme(t *testing.T) {
	th, err := Parse([]byte(openNord))
	if err != nil {
		t.Fatal(err)
	}
	refs := map[string][2]string{
		"primary":     {th.Primary(), "#88C0D0"},
		"error":       {th.Error(), "#BF616A"},
		"foreground":  {th.Foreground(), "#D8DEE9"},
		"muted":       {th.Muted(), "#4C566A"},
		"background":  {th.Background(), "#2E3440"},
		"user_bubble": {th.UserBubble(), "#3B4252"},
	}
	for name, want := range refs {
		if got := want[0]; got != want[1] {
			t.Errorf("%s = %s, want %s", name, got, want[1])
		}
	}
	if th.Tool() != Default.Colors.Tool {
		t.Errorf("\"none\" info should fall back to Default, got %s", th.Tool())
	}
}

// ANSI index, plain hex, and hex-without-theme-missing keys.
func TestParseOpenCodeANSIAndNative(t *testing.T) {
	th, err := Parse([]byte(`{"theme": {"primary": 12, "text": "#ffffff"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if th.Primary() != "#0000ff" {
		t.Errorf("ansi 12 = %s, want #0000ff", th.Primary())
	}
	if th.Foreground() != "#ffffff" {
		t.Errorf("text = %s, want #ffffff", th.Foreground())
	}
	// Native keys win over OpenCode ones in a hybrid document.
	th, err = Parse([]byte(`{"primary": "#111111", "theme": {"primary": 12}}`))
	if err != nil {
		t.Fatal(err)
	}
	if th.Primary() != "#111111" {
		t.Errorf("native primary = %s, want #111111", th.Primary())
	}
}

func TestParseNativeFlatAndNested(t *testing.T) {
	th, err := Parse([]byte(`{"name":"x","primary":"#aaaaaa"}`))
	if err != nil {
		t.Fatal(err)
	}
	if th.Name != "x" || th.Primary() != "#aaaaaa" {
		t.Errorf("flat: name=%s primary=%s", th.Name, th.Primary())
	}
	th, err = Parse([]byte(`{"colors":{"accent":"#bbbbbb"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if th.Accent() != "#bbbbbb" {
		t.Errorf("nested accent = %s", th.Accent())
	}
}

func TestAnsiHex(t *testing.T) {
	cases := map[int]string{0: "#000000", 9: "#ff0000", 16: "#000000",
		231: "#ffffff", 232: "#080808", 255: "#eeeeee"}
	for n, want := range cases {
		if got := ansiHex(n); got != want {
			t.Errorf("ansiHex(%d) = %s, want %s", n, got, want)
		}
	}
}
