// Package cmds loads user slash commands from the data tree: one
// `<dir>/<name>.md` per command, frontmatter + a body template. The body is
// what the model receives; `$ARGUMENTS` expands to whatever the user typed
// after the command name.
package cmds

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"matcode/internal/skills"
)

// Command is one user-defined slash command.
type Command struct {
	ID          string
	Description string
	// Agent/Model optionally pin the session while the command runs; both
	// are "" when the file does not state them.
	Agent string
	Model string
	// Body is the prompt template (frontmatter-less part of the file).
	Body string
	// Path is the file it came from (diagnostics).
	Path string
}

// Load reads every `<dir>/*.md`, later dirs winning per file (project
// replaces global file-by-file, §3 resolution rules). A missing dir is
// skipped; a broken file fails the whole load so `mtc doctor` can name it.
func Load(dirs ...string) ([]Command, error) {
	files := map[string]string{}
	var order []string
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		var names []string
		for _, e := range entries {
			if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".md") {
				names = append(names, e.Name())
			}
		}
		sort.Strings(names)
		for _, name := range names {
			b, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				return nil, err
			}
			id := strings.TrimSuffix(name, filepath.Ext(name))
			if _, seen := files[id]; !seen {
				order = append(order, id)
			}
			files[id] = string(b)
		}
	}

	out := make([]Command, 0, len(order))
	for _, id := range order {
		fm, body := skills.SplitFrontmatter(files[id])
		if strings.TrimSpace(body) == "" {
			return nil, fmt.Errorf("command %s.md: empty body", id)
		}
		out = append(out, Command{
			ID:          id,
			Description: fm["description"],
			Agent:       fm["agent"],
			Model:       fm["model"],
			Body:        strings.TrimSpace(body),
			Path:        id + ".md",
		})
	}
	return out, nil
}

// Find returns the command with the given id ("" = not found).
func Find(list []Command, id string) (Command, bool) {
	for _, c := range list {
		if c.ID == id {
			return c, true
		}
	}
	return Command{}, false
}

// Expand renders the body: every `$ARGUMENTS` becomes args; a body with no
// placeholder still receives them, appended as a trailing line so
// `/review src/x.go` never drops its argument.
func Expand(c Command, args string) string {
	args = strings.TrimSpace(args)
	if strings.Contains(c.Body, "$ARGUMENTS") {
		return strings.ReplaceAll(c.Body, "$ARGUMENTS", args)
	}
	if args == "" {
		return c.Body
	}
	return c.Body + "\n\n" + args
}
