package tools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"matcode/internal/engine"
)

// Grep scans files under a directory for a regular expression.
func Grep(workdir string) engine.Tool {
	return engine.Tool{
		ID:          "grep",
		Description: "Search file contents with a regular expression.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{"type": "string", "description": "Go regular expression."},
				"path":    map[string]any{"type": "string", "description": "Directory to search (default: workdir)."},
			},
			"required": []string{"pattern"},
		},
		Execute: func(ctx context.Context, input json.RawMessage) (engine.Result, error) {
			var in struct {
				Pattern string `json:"pattern"`
				Path    string `json:"path"`
			}
			if err := json.Unmarshal(input, &in); err != nil {
				return engine.Result{}, err
			}
			re, err := regexp.Compile(in.Pattern)
			if err != nil {
				return engine.Result{}, err
			}
			base := resolve(workdir, in.Path)
			var hits []string
			err = filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
				if err != nil {
					return nil
				}
				name := d.Name()
				if d.IsDir() {
					if path != base && (strings.HasPrefix(name, ".") ||
						name == "node_modules" || name == "vendor") {
						return filepath.SkipDir
					}
					return nil
				}
				if len(hits) >= 200 || skipFile(name, path) {
					return nil
				}
				b, err := os.ReadFile(path)
				if err != nil || bytes.IndexByte(b, 0) >= 0 || len(b) > 1024*1024 {
					return nil
				}
				rel, rerr := filepath.Rel(workdir, path)
				if rerr != nil {
					rel = path
				}
				sc := bufio.NewScanner(bytes.NewReader(b))
				sc.Buffer(make([]byte, 64*1024), 1024*1024)
				for n := 1; sc.Scan() && len(hits) < 200; n++ {
					line := sc.Text()
					if re.MatchString(line) {
						if len(line) > 300 {
							line = line[:300]
						}
						hits = append(hits, fmt.Sprintf("%s:%d:%s", rel, n, line))
					}
				}
				return nil
			})
			if err != nil {
				return engine.Result{}, err
			}
			if len(hits) == 0 {
				return engine.Result{Text: "no matches"}, nil
			}
			return engine.Result{Text: strings.Join(hits, "\n")}, nil
		},
	}
}

func skipFile(name, path string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".ico", ".pdf", ".zip",
		".gz", ".tar", ".exe", ".bin", ".woff", ".woff2", ".ttf", ".mp4":
		return true
	}
	return false
}
