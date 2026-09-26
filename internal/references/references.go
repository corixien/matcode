// Package references gives mtc named access to directories outside the
// project: local paths, and git repositories cloned under the data tree
// and refreshed at most once every 24 hours. References grant no extra
// tool permissions — they only name a directory.
package references

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
)

// maxAge is how stale a checkout may be before Sync touches it again.
const maxAge = 24 * time.Hour

// Ref is one resolved reference. Dir is always the directory consumers
// use: the local root, or the git checkout under <data>/repos.
type Ref struct {
	Alias       string `json:"-"` // map key, echoed for messages
	Path        string // original local path form ("" for git refs)
	Repository  string // normalized clone URL ("" for local refs)
	Branch      string
	Description string
	Hidden      bool
	Dir         string
}

// Parse validates and resolves one config layer's references table.
// baseDir resolves relative local paths (the config file's directory);
// dataDir anchors git checkouts.
func Parse(baseDir, dataDir string, raw map[string]any) (map[string]Ref, error) {
	home, _ := os.UserHomeDir()
	out := make(map[string]Ref, len(raw))
	for alias, v := range raw {
		if err := validAlias(alias); err != nil {
			return nil, err
		}
		r := Ref{Alias: alias}
		switch t := v.(type) {
		case string:
			if isLocalShorthand(t) {
				r.Path = t
			} else {
				r.Repository = t
			}
		case map[string]any:
			for k := range t {
				switch k {
				case "path", "repository", "branch", "description", "hidden":
				default:
					return nil, fmt.Errorf("references.%s: unknown key %q", alias, k)
				}
			}
			var err error
			if r.Path, err = strField(t, "path"); err != nil {
				return nil, fmt.Errorf("references.%s: %w", alias, err)
			}
			if r.Repository, err = strField(t, "repository"); err != nil {
				return nil, fmt.Errorf("references.%s: %w", alias, err)
			}
			if r.Branch, err = strField(t, "branch"); err != nil {
				return nil, fmt.Errorf("references.%s: %w", alias, err)
			}
			if r.Description, err = strField(t, "description"); err != nil {
				return nil, fmt.Errorf("references.%s: %w", alias, err)
			}
			if h, ok := t["hidden"]; ok {
				b, ok := h.(bool)
				if !ok {
					return nil, fmt.Errorf("references.%s: hidden wants a bool", alias)
				}
				r.Hidden = b
			}
		default:
			return nil, fmt.Errorf("references.%s: want a path string or a table", alias)
		}
		if r.Path != "" && r.Repository != "" {
			return nil, fmt.Errorf("references.%s: path and repository are mutually exclusive", alias)
		}
		switch {
		case r.Path != "":
			p := expand(r.Path, home)
			if !filepath.IsAbs(p) {
				p = filepath.Join(baseDir, p)
			}
			r.Dir = filepath.Clean(p)
		case r.Repository != "":
			if r.Branch != "" && !validBranch(r.Branch) {
				return nil, fmt.Errorf("references.%s: invalid branch %q", alias, r.Branch)
			}
			cloneURL, host, repoPath, err := normalizeRepo(r.Repository)
			if err != nil {
				return nil, fmt.Errorf("references.%s: %w", alias, err)
			}
			r.Repository = cloneURL
			r.Dir = checkout(dataDir, host, repoPath, r.Branch)
		default:
			return nil, fmt.Errorf("references.%s: missing path or repository", alias)
		}
		out[alias] = r
	}
	return out, nil
}

// Block renders references that carry a description as agent-instructions
// lines: each advertises its alias and resolved path.
func Block(refs map[string]Ref) string {
	names := Names(refs, true)
	if len(names) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n\nReferences (attach with ref:<alias>):")
	for _, a := range names {
		fmt.Fprintf(&sb, "\n- %s -> %s: %s", a, refs[a].Dir, refs[a].Description)
	}
	return sb.String()
}

// Names lists aliases sorted; described=true keeps only references that
// advertise a description.
func Names(refs map[string]Ref, described bool) []string {
	var out []string
	for a, r := range refs {
		// hidden = true keeps the alias out of selectors/listings (row 38);
		// expansion by typing the exact name still works.
		if r.Hidden {
			continue
		}
		if described && r.Description == "" {
			continue
		}
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}

// Sync refreshes git references in the background: missing checkouts are
// cloned, stale ones (last attempt >= 24 h) are fetched and reset. Prompts
// never wait on it; failures land in <data>/repos/refresh.log and stay
// subject to the same 24-hour limit.
func Sync(dataDir string, refs map[string]Ref) {
	var todo []Ref
	for _, r := range refs {
		if r.Repository != "" {
			todo = append(todo, r)
		}
	}
	if len(todo) == 0 {
		return
	}
	root := filepath.Join(dataDir, "repos")
	go func() {
		attempts := loadAttempts(filepath.Join(root, "refresh.json"))
		for _, r := range todo {
			if last, ok := attempts[r.Dir]; ok && time.Since(time.Unix(last, 0)) < maxAge {
				continue
			}
			attempts[r.Dir] = time.Now().Unix()
			saveAttempts(filepath.Join(root, "refresh.json"), attempts)
			if err := refresh(r); err != nil {
				appendLog(filepath.Join(root, "refresh.log"), r.Alias, err)
			}
		}
	}()
}

// refresh clones a missing checkout, or fetches and hard-resets a stale one
// to the configured branch (or the remote's default branch).
func refresh(r Ref) error {
	if _, err := os.Stat(filepath.Join(r.Dir, ".git")); err == nil {
		if err := run("git", "-C", r.Dir, "fetch", "--quiet", "origin"); err != nil {
			return err
		}
		target := "origin/HEAD"
		if r.Branch != "" {
			target = "origin/" + r.Branch
		}
		return run("git", "-C", r.Dir, "reset", "--quiet", "--hard", target)
	}
	if err := os.MkdirAll(filepath.Dir(r.Dir), 0o755); err != nil {
		return err
	}
	args := []string{"clone", "--quiet"}
	if r.Branch != "" {
		args = append(args, "-b", r.Branch)
	}
	args = append(args, "--", r.Repository, r.Dir)
	return run("git", args...)
}

func run(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if len(msg) > 400 {
			msg = msg[len(msg)-400:]
		}
		return fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, msg)
	}
	return nil
}

// normalizeRepo maps every supported remote form (GitHub owner/repo,
// host/path, https/git/ssh URLs, SCP remotes) to a clone URL plus the
// host and repository path used for the checkout location.
func normalizeRepo(spec string) (cloneURL, host, repoPath string, err error) {
	if strings.HasPrefix(spec, "file:") {
		return "", "", "", fmt.Errorf("local file: repositories are not supported")
	}
	if strings.Contains(spec, "://") {
		u, e := url.Parse(spec)
		if e != nil || u.Host == "" {
			return "", "", "", fmt.Errorf("bad repository %q", spec)
		}
		return spec, u.Host, strings.TrimPrefix(strings.TrimSuffix(strings.TrimPrefix(u.Path, "/"), ".git"), "/"), nil
	}
	// SCP form: [user@]host:path (colon with no slash before it, no scheme).
	if i := strings.IndexByte(spec, ':'); i > 0 && strings.IndexByte(spec[:i], '/') < 0 {
		left, path := spec[:i], spec[i+1:]
		host = left
		if j := strings.LastIndexByte(left, '@'); j >= 0 {
			host = left[j+1:]
		}
		if host == "" || path == "" {
			return "", "", "", fmt.Errorf("bad repository %q", spec)
		}
		return spec, host, strings.Trim(strings.TrimSuffix(path, ".git"), "/"), nil
	}
	parts := strings.Split(strings.Trim(spec, "/"), "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", "", fmt.Errorf("bad repository %q (want owner/repo, host/path, or a git URL)", spec)
	}
	if strings.ContainsAny(parts[0], " \t") {
		return "", "", "", fmt.Errorf("bad repository %q", spec)
	}
	if strings.Contains(parts[0], ".") {
		// host/path form.
		return "https://" + spec, parts[0], strings.Trim(strings.TrimSuffix(strings.Join(parts[1:], "/"), ".git"), "/"), nil
	}
	// GitHub owner/repo shorthand.
	return "https://github.com/" + spec, "github.com", strings.Trim(strings.TrimSuffix(strings.Join(parts, "/"), ".git"), "/"), nil
}

// checkout is one clone per remote and branch:
// <data>/repos/<host>/<repository-path>[@<branch>].
func checkout(dataDir, host, repoPath, branch string) string {
	p := filepath.Join(dataDir, "repos", host, repoPath)
	if branch != "" {
		p += "@" + url.PathEscape(branch)
	}
	return p
}

func isLocalShorthand(s string) bool {
	return strings.HasPrefix(s, ".") || strings.HasPrefix(s, "/") || strings.HasPrefix(s, "~")
}

func expand(p, home string) string {
	if p == "~" {
		return home
	}
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		return filepath.Join(home, rest)
	}
	return p
}

func validAlias(a string) error {
	if a == "" || strings.ContainsAny(a, "/\\`,") || strings.ContainsFunc(a, unicode.IsSpace) {
		return fmt.Errorf("invalid reference alias %q (no / \\ whitespace ` ,)", a)
	}
	return nil
}

var branchRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_./-]*$`)

func validBranch(b string) bool {
	return branchRe.MatchString(b) && !strings.Contains(b, "..")
}

func strField(m map[string]any, key string) (string, error) {
	v, ok := m[key]
	if !ok {
		return "", nil
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("%s wants a string", key)
	}
	return s, nil
}

// loadAttempts reads the persisted refresh timestamps (dir -> unix).
func loadAttempts(path string) map[string]int64 {
	m := map[string]int64{}
	b, err := os.ReadFile(path)
	if err == nil {
		json.Unmarshal(b, &m)
	}
	return m
}

func saveAttempts(path string, m map[string]int64) {
	b, err := json.Marshal(m)
	if err != nil {
		return
	}
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, append(b, '\n'), 0o644)
}

func appendLog(path, alias string, err error) {
	os.MkdirAll(filepath.Dir(path), 0o755)
	f, openErr := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if openErr != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s reference %s: %v\n", time.Now().UTC().Format(time.RFC3339), alias, err)
}
