package tools

import (
	"os/exec"
	"path/filepath"
	"strings"
)

// formatters maps a lowercased file extension to candidate commands, tried in
// order until one runs. "$FILE" is replaced by the absolute target path.
var formatters = map[string][][]string{
	".go":   {{"gofmt", "-w", "$FILE"}},
	".py":   {{"ruff", "format", "$FILE"}, {"black", "$FILE"}},
	".js":   {{"prettier", "--write", "$FILE"}},
	".jsx":  {{"prettier", "--write", "$FILE"}},
	".ts":   {{"prettier", "--write", "$FILE"}},
	".tsx":  {{"prettier", "--write", "$FILE"}},
	".json": {{"prettier", "--write", "$FILE"}},
	".css":  {{"prettier", "--write", "$FILE"}},
	".html": {{"prettier", "--write", "$FILE"}},
	".md":   {{"prettier", "--write", "$FILE"}},
	".sh":   {{"shfmt", "-w", "$FILE"}},
	".rs":   {{"rustfmt", "$FILE"}},
	".c":    {{"clang-format", "-i", "$FILE"}},
	".h":    {{"clang-format", "-i", "$FILE"}},
	".cpp":  {{"clang-format", "-i", "$FILE"}},
	".zig":  {{"zig", "fmt", "$FILE"}},
}

// format runs the first available formatter for path's extension in the
// background of the write: disabled, unknown extension, or no candidate in
// PATH is a no-op, and a failing formatter never fails the edit.
func format(workdir string, enabled bool, path string) {
	if !enabled {
		return
	}
	cands := formatters[strings.ToLower(filepath.Ext(path))]
	for _, argv := range cands {
		bin, err := exec.LookPath(argv[0])
		if err != nil {
			continue
		}
		args := make([]string, len(argv)-1)
		for i, a := range argv[1:] {
			args[i] = strings.ReplaceAll(a, "$FILE", path)
		}
		cmd := exec.Command(bin, args...)
		cmd.Dir = workdir
		if err := cmd.Run(); err == nil {
			return
		}
	}
}
