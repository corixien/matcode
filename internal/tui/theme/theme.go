package theme

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Fields is the palette every theme fills. "" falls back to Default.
type Fields struct {
	Background string `json:"background"`
	Foreground string `json:"foreground"`
	Primary    string `json:"primary"`
	Secondary  string `json:"secondary"`
	Accent     string `json:"accent"`
	Success    string `json:"success"`
	Warning    string `json:"warning"`
	Error      string `json:"error"`
	Muted      string `json:"muted"`
	Border     string `json:"border"`
	UserBubble string `json:"user_bubble"`
	Assistant  string `json:"assistant"`
	Tool       string `json:"tool"`
	System     string `json:"system"`
}

// Theme is a named palette. The Colors field is the palette itself;
// the accessor methods read it so call sites stay terse.
type Theme struct {
	Name   string `json:"name"`
	Colors Fields `json:"colors"`
	Path   string `json:"-"`
}

// palette is the parse form: either a flat object or {"colors":{...}}.
type palette struct {
	Name string `json:"name"`
	Fields
	Colors *Fields `json:"colors"`
}

// Default is the built-in theme and the fallback for missing fields.
var Default = Theme{
	Name: "default",
	Colors: Fields{
		Background: "#1e1e2e", Foreground: "#cdd6f4",
		Primary: "#89b4fa", Secondary: "#a6adc8", Accent: "#f9e2af",
		Success: "#a6e3a1", Warning: "#f9e2af", Error: "#f38ba8",
		Muted: "#6c7086", Border: "#45475a",
		UserBubble: "#313244", Assistant: "#cdd6f4", Tool: "#94e2d5",
		System: "#a6adc8",
	},
}

// Accessors read the palette, falling back to Default for empty slots.
func (t Theme) Background() string { return pick(t.Colors.Background, Default.Colors.Background) }
func (t Theme) Foreground() string { return pick(t.Colors.Foreground, Default.Colors.Foreground) }
func (t Theme) Primary() string    { return pick(t.Colors.Primary, Default.Colors.Primary) }
func (t Theme) Secondary() string  { return pick(t.Colors.Secondary, Default.Colors.Secondary) }
func (t Theme) Accent() string     { return pick(t.Colors.Accent, Default.Colors.Accent) }
func (t Theme) Success() string    { return pick(t.Colors.Success, Default.Colors.Success) }
func (t Theme) Warning() string    { return pick(t.Colors.Warning, Default.Colors.Warning) }
func (t Theme) Error() string      { return pick(t.Colors.Error, Default.Colors.Error) }
func (t Theme) Muted() string      { return pick(t.Colors.Muted, Default.Colors.Muted) }
func (t Theme) Border() string     { return pick(t.Colors.Border, Default.Colors.Border) }
func (t Theme) UserBubble() string { return pick(t.Colors.UserBubble, Default.Colors.UserBubble) }
func (t Theme) Assistant() string  { return pick(t.Colors.Assistant, Default.Colors.Assistant) }
func (t Theme) Tool() string       { return pick(t.Colors.Tool, Default.Colors.Tool) }
func (t Theme) System() string     { return pick(t.Colors.System, Default.Colors.System) }

// pick returns v, or fallback when unset.
func pick(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

// Get resolves name against dir (themes/*.json); "" or "default" is the
// built-in theme. A file that fails to parse falls back to Default with
// the error reported, so a bad theme never blocks startup.
func Get(dir, name string) (Theme, error) {
	if name == "" || name == "default" {
		return Default, nil
	}
	path := name
	if !filepath.IsAbs(path) && !strings.HasSuffix(path, ".json") {
		path = filepath.Join(dir, name+".json")
	}
	return File(path)
}

// File parses one themes/*.json file.
func File(path string) (Theme, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Default, err
	}
	t, err := Parse(b)
	if err != nil {
		return Default, err
	}
	t.Path = path
	if t.Name == "" {
		base := filepath.Base(path)
		t.Name = strings.TrimSuffix(base, filepath.Ext(base))
	}
	return t, nil
}

// Parse decodes a theme document (OpenCode shape, flat, or nested) and
// fills gaps from Default. Native matcode keys win over OpenCode ones.
func Parse(b []byte) (Theme, error) {
	var p palette
	if err := json.Unmarshal(b, &p); err != nil {
		return Default, err
	}
	fields := p.Fields
	if p.Colors != nil {
		fields = merge(*p.Colors, p.Fields)
	}
	if open, ok, err := fromOpen(b); err != nil {
		return Default, err
	} else if ok {
		fields = merge(fields, open) // native keys overlay the OpenCode base
	}
	t := Theme{Name: p.Name, Colors: fill(fields)}
	return t, nil
}

// merge overlays non-empty nested values over the flat form.
func merge(nested, flat Fields) Fields {
	out := flat
	set := func(dst *string, v string) {
		if v != "" {
			*dst = v
		}
	}
	set(&out.Background, nested.Background)
	set(&out.Foreground, nested.Foreground)
	set(&out.Primary, nested.Primary)
	set(&out.Secondary, nested.Secondary)
	set(&out.Accent, nested.Accent)
	set(&out.Success, nested.Success)
	set(&out.Warning, nested.Warning)
	set(&out.Error, nested.Error)
	set(&out.Muted, nested.Muted)
	set(&out.Border, nested.Border)
	set(&out.UserBubble, nested.UserBubble)
	set(&out.Assistant, nested.Assistant)
	set(&out.Tool, nested.Tool)
	set(&out.System, nested.System)
	return out
}

// fill replaces empty slots with the Default theme's values.
func fill(f Fields) Fields {
	return Fields{
		Background: pick(f.Background, Default.Colors.Background),
		Foreground: pick(f.Foreground, Default.Colors.Foreground),
		Primary:    pick(f.Primary, Default.Colors.Primary),
		Secondary:  pick(f.Secondary, Default.Colors.Secondary),
		Accent:     pick(f.Accent, Default.Colors.Accent),
		Success:    pick(f.Success, Default.Colors.Success),
		Warning:    pick(f.Warning, Default.Colors.Warning),
		Error:      pick(f.Error, Default.Colors.Error),
		Muted:      pick(f.Muted, Default.Colors.Muted),
		Border:     pick(f.Border, Default.Colors.Border),
		UserBubble: pick(f.UserBubble, Default.Colors.UserBubble),
		Assistant:  pick(f.Assistant, Default.Colors.Assistant),
		Tool:       pick(f.Tool, Default.Colors.Tool),
		System:     pick(f.System, Default.Colors.System),
	}
}

// Names lists the themes in dir (themes/*.json), sorted, "default" first.
func Names(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []string{"default"}
	}
	out := []string{"default"}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".json")
		if name != "default" {
			out = append(out, name)
		}
	}
	sort.Strings(out[1:])
	return out
}

// Dir returns the themes directory to search for a project.
func Dir(project string) string { return filepath.Join(project, "themes") }
