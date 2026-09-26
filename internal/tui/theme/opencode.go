package theme

import (
	"encoding/json"
	"fmt"
)

// openDoc is the OpenCode theme document shape:
//
//	{"$schema": "...", "defs": {...}, "theme": {"primary": ...}}
//
// Values are a hex color ("#rrggbb"), an ANSI index (0-255), a reference
// into defs, a {"dark": ...} / {"light": ...} variant pair, or "none"
// (terminal default, treated as unset here so Default fills it).
type openDoc struct {
	Defs  map[string]json.RawMessage `json:"defs"`
	Theme map[string]json.RawMessage `json:"theme"`
}

// fromOpen converts an OpenCode theme document. ok is false when the
// document carries no "theme" map, so matcode's native shape is used.
func fromOpen(b []byte) (out Fields, ok bool, err error) {
	var d openDoc
	if err = json.Unmarshal(b, &d); err != nil {
		return out, false, err
	}
	if len(d.Theme) == 0 {
		return out, false, nil
	}
	get := func(k string) string { return d.themeVal(k, 0) }
	out = Fields{
		// Priority pairs: first key that resolves wins.
		UserBubble: first(get("backgroundPanel"), get("backgroundElement")),
		// matcode-only slots OpenCode has no key for: lean on the
		// theme's text colors so bubbles stay readable.
		Assistant: first(get("markdownText"), get("text")),
		System:    first(get("textMuted"), get("text")),
		// Direct key mappings.
		Background: get("background"),
		Foreground: get("text"),
		Primary:    get("primary"),
		Secondary:  get("secondary"),
		Accent:     get("accent"),
		Success:    get("success"),
		Warning:    get("warning"),
		Error:      get("error"),
		Muted:      get("textMuted"),
		Border:     get("border"),
		Tool:       get("info"),
	}
	return out, true, nil
}

// themeVal resolves a top-level theme key.
func (d openDoc) themeVal(key string, depth int) string {
	raw, ok := d.Theme[key]
	if !ok {
		return ""
	}
	return d.value(raw, depth+1)
}

// defVal resolves a reference: defs first (the documented purpose of
// defs), then a theme key used as a named color.
func (d openDoc) defVal(ref string, depth int) string {
	if raw, ok := d.Defs[ref]; ok {
		return d.value(raw, depth+1)
	}
	return d.themeVal(ref, depth)
}

// value decodes one raw color value; depth caps reference chains.
func (d openDoc) value(raw json.RawMessage, depth int) string {
	if depth > 8 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		switch {
		case s == "none" || s == "":
			return "" // terminal default → Default fills it
		case s[0] == '#':
			return s
		default:
			return d.defVal(s, depth) // reference into defs/theme
		}
	}
	var n int
	if json.Unmarshal(raw, &n) == nil && n >= 0 && n <= 255 {
		return ansiHex(n)
	}
	var v struct {
		Dark json.RawMessage `json:"dark"`
	}
	if json.Unmarshal(raw, &v) == nil && len(v.Dark) > 0 {
		return d.value(v.Dark, depth) // matcode assumes a dark terminal
	}
	return ""
}

// first returns the first non-empty candidate.
func first(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// ansiHex maps an xterm-256 index to its hex color.
func ansiHex(n int) string {
	base := [16]string{"#000000", "#800000", "#008000", "#808000", "#000080",
		"#800080", "#008080", "#c0c0c0", "#808080", "#ff0000", "#00ff00",
		"#ffff00", "#0000ff", "#ff00ff", "#00ffff", "#ffffff"}
	if n < 16 {
		return base[n]
	}
	if n < 232 {
		cube := [6]int{0, 95, 135, 175, 215, 255}
		i := n - 16
		return fmt.Sprintf("#%02x%02x%02x", cube[i/36], cube[(i/6)%6], cube[i%6])
	}
	g := 8 + 10*(n-232)
	return fmt.Sprintf("#%02x%02x%02x", g, g, g)
}
