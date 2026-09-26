// Package permissions evaluates allow/ask/deny rules before a tool executes.
// Precedence: deny wins over ask, ask over allow; a rule miss falls through to
// the default. An approval persisted by "always" sits between ask and deny:
// it overrides an ask rule but never a deny. Plugins later hook between a
// miss and the default.
package permissions

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
)

// Decision strings returned by Eval.
const (
	Allow = "allow"
	Ask   = "ask"
	Deny  = "deny"
)

// ExternalDirectory is the implicit action evaluated for every resource
// path that resolves outside the working directory.
const ExternalDirectory = "external_directory"

// Rule matches a set of tool ids against the input's text and decides.
type Rule struct {
	Actions  []string `toml:"action"`             // tool ids, "*" or glob patterns ("bash*")
	Pattern  string   `toml:"pattern"`            // glob matched against the input text
	Decision string   `toml:"decision"`           // allow | ask | deny
	Approved bool     `toml:"approved,omitempty"` // persisted by an "always" approval
}

// Set is the parsed permissions.toml.
type Set struct {
	Default string `toml:"default"` // allow (rice) | ask | deny
	Rules   []Rule `toml:"rule"`

	// Path is the file Approve persists "always" approvals to. Load sets it;
	// an empty Path makes "always" fail with an error instead of a silent no-op.
	Path string `toml:"-"`
	// WorkDir anchors relative resource paths for the external_directory
	// check. Empty disables the check (every path counts as inside).
	WorkDir string `toml:"-"`
}

// Load parses path. An empty path returns the rice default: allow everything.
func Load(path string) (*Set, error) {
	if path == "" {
		return &Set{Default: Allow}, nil
	}
	var s Set
	if _, err := toml.DecodeFile(path, &s); err != nil {
		return nil, err
	}
	if s.Default == "" {
		s.Default = Allow
	}
	s.Path = path
	return &s, nil
}

// Eval applies the rules to one tool call and returns a decision. Every
// resource the input names (paths, workdir) is evaluated on its own; the
// strictest decision across all of them wins — any deny beats any ask, any
// ask beats any allow. input is the raw tool input.
func (s *Set) Eval(action string, input json.RawMessage) string {
	if d, matched := s.eval(action, input); matched {
		return d
	}
	return s.Default
}

// EvalMatch evaluates the rules without the default fallback: matched
// reports whether any rule matched at all. The engine uses it to consult
// the plugin permission hook between a rule miss and the default (§8).
func (s *Set) EvalMatch(action string, input json.RawMessage) (string, bool) {
	return s.eval(action, input)
}

// eval is Eval without the default fallback: matched reports whether any
// rule matched at all.
func (s *Set) eval(action string, input json.RawMessage) (string, bool) {
	var m map[string]json.RawMessage
	_ = json.Unmarshal(input, &m)

	type resource struct {
		text string
		path bool // true: subject to the external_directory check
	}
	var res []resource
	seen := map[string]bool{}
	add := func(t string, isPath bool) {
		if t == "" || seen[t] {
			return
		}
		seen[t] = true
		res = append(res, resource{t, isPath})
	}
	// Path-ish fields, string or array, are resources of their own.
	for _, k := range []string{"path", "paths", "workdir", "directory", "file_path"} {
		v, ok := m[k]
		if !ok {
			continue
		}
		var one string
		if json.Unmarshal(v, &one) == nil {
			add(one, true)
			continue
		}
		var many []string
		if json.Unmarshal(v, &many) == nil {
			for _, x := range many {
				add(x, true)
			}
		}
	}
	add(inputText(input), false) // the command-ish field, or the first path, or raw
	if len(res) == 0 {
		add(string(input), false)
	}

	best, bestW := "", 0
	consider := func(w int, d string) {
		if w > bestW {
			best, bestW = d, w
		}
	}
	for _, r := range res {
		for _, rule := range s.Rules {
			if !matchAny(rule.Actions, action) {
				continue
			}
			if rule.Pattern != "" && !glob(expand(rule.Pattern), expand(r.text)) {
				continue
			}
			consider(ruleWeight(rule), rule.Decision)
		}
		if r.path {
			if abs, outside := s.absExternal(r.text); outside {
				for _, rule := range s.Rules {
					if !matchAny(rule.Actions, ExternalDirectory) {
						continue
					}
					if rule.Pattern != "" && !glob(expand(rule.Pattern), expand(abs)) {
						continue
					}
					consider(ruleWeight(rule), rule.Decision)
				}
			}
		}
	}
	return best, bestW > 0
}

// Approve records an interactive approval for a call Eval asked about.
// scope "once" is a no-op (the caller proceeds). scope "always" appends an
// allow rule to Path — but never while a deny rule matches: deny wins over
// any approval, so this cannot be used to override one.
func (s *Set) Approve(action string, input json.RawMessage, scope string) error {
	if scope != "always" {
		return nil
	}
	if d, matched := s.eval(action, input); matched && d == Deny {
		return errors.New("approval can never override a deny rule")
	}
	if s.Path == "" {
		return errors.New("no permissions.toml to persist the approval to")
	}
	rule := Rule{
		Actions:  []string{action},
		Pattern:  escapeGlob(inputText(input)),
		Decision: Allow,
		Approved: true,
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(map[string][]Rule{"rule": {rule}}); err != nil {
		return err
	}
	info, err := os.Stat(s.Path)
	lead := ""
	if err == nil && info.Size() > 0 {
		lead = "\n" // blank line between blocks; harmless if the file already ends with one
	}
	f, err := os.OpenFile(s.Path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(lead + buf.String()); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	s.Rules = append(s.Rules, rule)
	return nil
}

// absExternal resolves a resource path against WorkDir and reports whether
// it lands outside it. Empty WorkDir disables the check.
func (s *Set) absExternal(p string) (string, bool) {
	if s.WorkDir == "" {
		return "", false
	}
	p = expand(p)
	if !filepath.IsAbs(p) {
		p = filepath.Join(s.WorkDir, p)
	}
	rel, err := filepath.Rel(s.WorkDir, p)
	if err != nil {
		return p, true
	}
	return p, rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func ruleWeight(r Rule) int {
	switch {
	case r.Decision == Deny:
		return 4
	case r.Decision == Ask:
		return 2
	case r.Decision == Allow && r.Approved:
		return 3
	case r.Decision == Allow:
		return 1
	}
	return 0
}

// expand resolves ~ and $HOME in a path or pattern.
func expand(p string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home, p[2:])
	}
	p = strings.ReplaceAll(p, "${HOME}", home)
	return strings.ReplaceAll(p, "$HOME", home)
}

// escapeGlob quotes * ? \ so an approval pattern matches its text literally.
func escapeGlob(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r == '*' || r == '?' || r == '\\' {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// inputText extracts the value rules should match: the first command-ish
// field present, else the raw input.
func inputText(input json.RawMessage) string {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(input, &m); err == nil {
		for _, k := range []string{"command", "query", "prompt", "url", "path", "pattern", "id"} {
			if v, ok := m[k]; ok {
				var s string
				if json.Unmarshal(v, &s) == nil {
					return s
				}
			}
		}
	}
	return string(input)
}

func matchAny(patterns []string, action string) bool {
	for _, p := range patterns {
		if glob(p, action) {
			return true
		}
	}
	return false
}

// glob matches a pattern where * and ? span any characters (unlike
// path.Match, * also crosses spaces in commands like "rm -rf * /tmp").
// A backslash escapes the next character so approvals can match literally.
func glob(pattern, s string) bool {
	if pattern == "*" {
		return true
	}
	var re strings.Builder
	re.WriteString("^")
	rs := []rune(pattern)
	for i := 0; i < len(rs); i++ {
		switch rs[i] {
		case '\\':
			if i+1 < len(rs) {
				re.WriteString(regexp.QuoteMeta(string(rs[i+1])))
				i++
			} else {
				re.WriteString(`\\`)
			}
		case '*':
			re.WriteString(".*")
		case '?':
			re.WriteString(".")
		default:
			re.WriteString(regexp.QuoteMeta(string(rs[i])))
		}
	}
	re.WriteString("$")
	ok, err := regexp.MatchString(re.String(), s)
	return err == nil && ok
}
