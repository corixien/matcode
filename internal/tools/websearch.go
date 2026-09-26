package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"matcode/internal/engine"
)

// Websearch queries DuckDuckGo's lite endpoint (no API key, no account) and
// returns numbered title+URL results.
func Websearch(workdir string) engine.Tool {
	return engine.Tool{
		ID:          "websearch",
		Description: "Search the web and return result titles with URLs.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string", "description": "Search query."},
			},
			"required": []string{"query"},
		},
		Execute: func(ctx context.Context, input json.RawMessage) (engine.Result, error) {
			var in struct {
				Query string `json:"query"`
			}
			if err := json.Unmarshal(input, &in); err != nil {
				return engine.Result{}, err
			}
			endpoint := "https://lite.duckduckgo.com/lite/?q=" + url.QueryEscape(in.Query)
			b, _, err := fetch(ctx, endpoint, 0)
			if err != nil {
				return engine.Result{}, err
			}
			results := parseResults(string(b))
			if len(results) == 0 {
				return engine.Result{}, fmt.Errorf("no results parsed (search layout may have changed)")
			}
			var out []string
			for i, r := range results {
				out = append(out, fmt.Sprintf("%d. %s\n   %s", i+1, r.title, r.url))
			}
			return engine.Result{Text: strings.Join(out, "\n")}, nil
		},
	}
}

type result struct{ title, url string }

var reLink = regexp.MustCompile(`(?s)<a[^>]*href="(https?://[^"]+)"[^>]*>(.*?)</a>`)

// parseResults extracts external links from search HTML, dropping nav links
// and duplicates.
func parseResults(html string) []result {
	var out []result
	seen := map[string]bool{}
	for _, m := range reLink.FindAllStringSubmatch(html, -1) {
		u, title := m[1], stripHTML(m[2])
		if strings.Contains(u, "duckduckgo.com") || title == "" || seen[u] {
			continue
		}
		seen[u] = true
		out = append(out, result{title: title, url: u})
		if len(out) == 10 {
			break
		}
	}
	return out
}
