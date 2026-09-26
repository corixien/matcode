// Package skills discovers skill definitions in the data trees and hands
// them to the skill tool. Format follows OpenCode V2: a source root holds
// one folder per skill (`<name>/SKILL.md`) or one flat `<name>.md` file; the
// path picks the ID, the frontmatter only labels and describes it. Nothing
// enters a prompt until the model asks for a skill by ID.
package skills

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// MaxFiles caps the supporting-file sample a loaded skill advertises.
const MaxFiles = 10

// Skill is one definition as the model sees it.
type Skill struct {
	ID          string   // path-derived, case-sensitive
	Name        string   // display label (frontmatter name, else the ID)
	Description string   // one-line summary advertised to the model
	Body        string   // markdown after the frontmatter
	Dir         string   // base directory: the skill folder, else the source root
	Files       []string // supporting files under Dir, relative, capped at MaxFiles
	Advertised  bool     // listed for the model (has a description, not hidden)
}

// Set is the merged skill registry: sources load low→high precedence, so a
// project definition replaces a global one with the same ID.
type Set struct {
	byID   map[string]Skill
	ids    []string
	loaded []string
}

// Load reads every source root in order. Missing roots are skipped — an
// empty data tree yields an empty set, not an error.
func Load(dirs ...string) (*Set, error) {
	s := &Set{byID: map[string]Skill{}}
	for _, root := range dirs {
		if root == "" {
			continue
		}
		if st, err := os.Stat(root); err != nil || !st.IsDir() {
			continue
		}
		if err := s.loadRoot(root); err != nil {
			return nil, err
		}
	}
	s.ids = make([]string, 0, len(s.byID))
	for id := range s.byID {
		s.ids = append(s.ids, id)
	}
	sort.Strings(s.ids)
	return s, nil
}

// loadRoot walks one source root and merges every definition it finds.
func (s *Set) loadRoot(root string) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entry: skip, never fail the whole load
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		dir := filepath.Dir(path)
		var id string
		switch {
		case name == "SKILL.md" && dir != root:
			// <root>/<anything>/SKILL.md at any depth: the containing
			// folder names the ID (V2 rule).
			id = filepath.Base(dir)
		case name == "SKILL.md":
			return nil // a root-level SKILL.md has no folder to name it
		case strings.EqualFold(filepath.Ext(name), ".md") && dir == root:
			// Flat form: <root>/<id>.md
			id = strings.TrimSuffix(name, filepath.Ext(name))
		default:
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		sk, ok := parse(id, dir, string(b))
		if !ok {
			return nil
		}
		sk.Files = supporting(dir)
		s.byID[id] = sk // later source wins
		return nil
	})
}

// parse turns one SKILL.md into a Skill. Frontmatter is optional (V2); a
// file without it still loads, just without anything to advertise.
func parse(id, dir, text string) (Skill, bool) {
	fm, body := SplitFrontmatter(text)
	sk := Skill{ID: id, Name: id, Dir: dir, Body: strings.TrimLeft(body, "\n")}
	if v := fm["name"]; v != "" {
		sk.Name = v
	}
	sk.Description = fm["description"]
	// Advertised = the model may discover it: a description exists and the
	// skill did not opt out of the available list. (`slash` stays a TUI
	// concern: it hides a skill from the command catalog, not from here.)
	auto := strings.ToLower(fm["metadata.opencode/autoinvoke"])
	sk.Advertised = sk.Description != "" && auto != "false" && auto != "no"
	return sk, sk.Body != "" || sk.Description != ""
}

// SplitFrontmatter returns the frontmatter key/value pairs and the body.
// Keys from nested blocks join with dots (metadata.opencode/autoinvoke).
func SplitFrontmatter(text string) (map[string]string, string) {
	lines := strings.Split(text, "\n")
	if len(lines) == 0 || strings.TrimSpace(strings.TrimSuffix(lines[0], "\r")) != "---" {
		return map[string]string{}, text
	}
	fm := map[string]string{}
	type frame struct {
		indent int
		key    string
	}
	var stack []frame
	for i := 1; i < len(lines); i++ {
		raw := strings.TrimSuffix(lines[i], "\r")
		if strings.TrimSpace(raw) == "---" {
			return fm, strings.Join(lines[i+1:], "\n")
		}
		trim := strings.TrimLeft(raw, " ")
		if trim == "" || strings.HasPrefix(trim, "#") {
			continue
		}
		indent := len(raw) - len(trim)
		for len(stack) > 0 && stack[len(stack)-1].indent >= indent {
			stack = stack[:len(stack)-1]
		}
		key, value, found := strings.Cut(trim, ":")
		if !found {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" {
			continue
		}
		if value == "" {
			stack = append(stack, frame{indent, key})
			continue
		}
		path := key
		for i := len(stack) - 1; i >= 0; i-- {
			path = stack[i].key + "." + path
		}
		fm[path] = unquote(value)
	}
	return fm, ""
}

func unquote(v string) string {
	if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
		return v[1 : len(v)-1]
	}
	return v
}

// supporting lists up to MaxFiles files beside the skill — the
// alphabetically first ones — excluding the definition itself. Contents are
// never loaded automatically.
func supporting(dir string) []string {
	var out []string
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Base(path) == "SKILL.md" {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return nil
		}
		out = append(out, rel)
		return nil
	})
	sort.Strings(out)
	if len(out) > MaxFiles {
		out = out[:MaxFiles]
	}
	return out
}

// Get looks one skill up by its exact ID.
func (s *Set) Get(id string) (Skill, bool) {
	sk, ok := s.byID[id]
	return sk, ok
}

// IDs lists every known ID, sorted.
func (s *Set) IDs() []string {
	if s == nil {
		return nil
	}
	return s.ids
}

// Len counts every definition, advertised or not.
func (s *Set) Len() int {
	if s == nil {
		return 0
	}
	return len(s.ids)
}

// List returns every skill in ID order.
func (s *Set) List() []Skill {
	if s == nil {
		return nil
	}
	out := make([]Skill, 0, len(s.ids))
	for _, id := range s.ids {
		out = append(out, s.byID[id])
	}
	return out
}

// Advertise renders the model-facing `<available_skills>` block: ID, name
// and description only — never the body.
func (s *Set) Advertise() string {
	var b strings.Builder
	for _, sk := range s.List() {
		if !sk.Advertised {
			continue
		}
		b.WriteString("  <skill>\n")
		fmt.Fprintf(&b, "    <id>%s</id>\n", escape(sk.ID))
		fmt.Fprintf(&b, "    <name>%s</name>\n", escape(sk.Name))
		fmt.Fprintf(&b, "    <description>%s</description>\n", escape(oneLine(sk.Description, 400)))
		b.WriteString("  </skill>\n")
	}
	if b.Len() == 0 {
		return ""
	}
	return "<available_skills>\n" + b.String() + "</available_skills>"
}

// MarkLoaded records that the model loaded a skill, for the payload
// guidance block (spec §11 pipeline).
func (s *Set) MarkLoaded(id string) {
	if s == nil {
		return
	}
	for _, x := range s.loaded {
		if x == id {
			return
		}
	}
	s.loaded = append(s.loaded, id)
}

// LoadedIDs lists loaded skill IDs in load order.
func (s *Set) LoadedIDs() []string {
	if s == nil {
		return nil
	}
	return append([]string(nil), s.loaded...)
}

// Guidance is the payload block naming the skills loaded so far. It is the
// one part of a skill that survives compaction with the system prompt.
func (s *Set) Guidance() string {
	if s == nil || len(s.loaded) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Loaded skills\n")
	for _, id := range s.loaded {
		sk, ok := s.byID[id]
		if !ok {
			continue
		}
		if sk.Description == "" {
			fmt.Fprintf(&b, "- %s\n", sk.ID)
			continue
		}
		fmt.Fprintf(&b, "- %s (%s): %s\n", sk.ID, sk.Name, oneLine(sk.Description, 400))
	}
	b.WriteString("Instructions arrived with the skill tool result; call the skill tool again " +
		"to restore them if compaction folded them away.")
	return b.String()
}

// Without copies the set minus every ID the predicate rejects — how a
// denied skill stays hidden from the model instead of erroring later.
func (s *Set) Without(fn func(id string) bool) *Set {
	if s == nil {
		return nil
	}
	out := &Set{byID: map[string]Skill{}, loaded: append([]string(nil), s.loaded...)}
	for _, id := range s.ids {
		if fn != nil && fn(id) {
			continue
		}
		out.byID[id] = s.byID[id]
		out.ids = append(out.ids, id)
	}
	out.loaded = out.loaded[:0]
	for _, id := range s.loaded {
		if _, ok := out.byID[id]; ok {
			out.loaded = append(out.loaded, id)
		}
	}
	return out
}

func escape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	return strings.ReplaceAll(s, ">", "&gt;")
}

func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > max {
		return s[:max-1] + "…"
	}
	return s
}
