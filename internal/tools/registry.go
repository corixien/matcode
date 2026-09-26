package tools

import (
	"path/filepath"

	"matcode/internal/engine"
	"matcode/internal/skills"
)

// Builtin registers every builtin tool — one entry per tool file.
// User tools (data/tools/) and MCP dispatch merge on top via Merge: last
// registration wins on a name collision.
// format enables ext-matched formatters after write/edit/patch.
// set supplies the skill tool; it is omitted when the data tree defines no
// skills so the model never sees a dead tool.
func Builtin(workdir, dataDir string, format bool, set *skills.Set) []engine.Tool {
	list := []engine.Tool{
		Bash(workdir, dataDir),
		Glob(workdir),
		Read(workdir),
		Write(workdir, format),
		Edit(workdir, format),
		Patch(workdir, format),
		Grep(workdir),
		Webfetch(workdir),
		Websearch(workdir),
		Todo(dataDir),
		Question(workdir),
	}
	if set != nil && set.Len() > 0 {
		list = append(list, Skill(set))
	}
	return list
}

// Merge appends extra to base and drops the earlier entry when an id
// repeats: the last registration wins, so a data-tree tool shadows a
// builtin and an MCP server shadows both (§registry).
func Merge(base, extra []engine.Tool) []engine.Tool {
	if len(extra) == 0 {
		return base
	}
	out := append([]engine.Tool(nil), base...)
	at := map[string]int{} // id → position in out
	for i, t := range out {
		if _, dup := at[t.ID]; !dup {
			at[t.ID] = i
		}
	}
	for _, t := range extra {
		if i, ok := at[t.ID]; ok {
			out[i] = t
			continue
		}
		at[t.ID] = len(out)
		out = append(out, t)
	}
	return out
}

// resolve joins a relative path to workdir and leaves absolute paths alone.
func resolve(workdir, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(workdir, path)
}
