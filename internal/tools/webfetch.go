package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"matcode/internal/engine"
)

// Webfetch retrieves a URL and returns it in the requested format. HTML is
// converted per `format`; everything else passes through as text.
func Webfetch(workdir string) engine.Tool {
	return engine.Tool{
		ID:          "webfetch",
		Description: "Fetch a URL and return its content as text.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url":     map[string]any{"type": "string", "description": "Absolute http(s) URL."},
				"format":  map[string]any{"type": "string", "enum": []string{"markdown", "text", "html"}, "description": "Output format for HTML pages (default markdown: structure preserved, tags stripped). text strips tags; html returns the raw source."},
				"timeout": map[string]any{"type": "number", "description": "Timeout in milliseconds (default 20000, max 120000)."},
			},
			"required": []string{"url"},
		},
		Execute: func(ctx context.Context, input json.RawMessage) (engine.Result, error) {
			var in struct {
				URL     string `json:"url"`
				Format  string `json:"format"`
				Timeout int    `json:"timeout"`
			}
			if err := json.Unmarshal(input, &in); err != nil {
				return engine.Result{}, err
			}
			if in.Format == "" {
				in.Format = "markdown"
			}
			body, ct, err := fetch(ctx, in.URL, in.Timeout)
			if err != nil {
				return engine.Result{}, err
			}
			text := string(body)
			isHTML := strings.Contains(ct, "html") || looksLikeHTML(text)
			switch in.Format {
			case "html":
				// raw source, as requested
			case "text":
				if isHTML {
					text = stripHTML(text)
				}
			default: // markdown
				if isHTML {
					text = toMarkdown(text)
				}
			}
			if len(text) > 256*1024 {
				text = text[:256*1024]
			}
			return engine.Result{Text: text}, nil
		},
	}
}

// fetch GETs a URL with a 1MB cap shared by webfetch and websearch.
// timeoutMS is capped at OpenCode's 120s ceiling.
func fetch(ctx context.Context, url string, timeoutMS int) ([]byte, string, error) {
	if timeoutMS <= 0 {
		timeoutMS = 20000
	}
	if timeoutMS > 120000 {
		timeoutMS = 120000
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", "matcode/0.1")
	client := &http.Client{Timeout: time.Duration(timeoutMS) * time.Millisecond}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, "", fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return nil, "", err
	}
	return b, resp.Header.Get("Content-Type"), nil
}

var (
	reScript = regexp.MustCompile(`(?is)<(script|style|noscript)[^>]*>.*?</\s*(script|style|noscript)>`)
	reTag    = regexp.MustCompile(`(?s)<[^>]+>`)
	reSpace  = regexp.MustCompile(`[ \t]+`)
	reBlank  = regexp.MustCompile(`\n{3,}`)
)

// looksLikeHTML sniffs markup when the server's content type is missing or
// wrong (many endpoints answer text/plain with a full HTML page).
func looksLikeHTML(s string) bool {
	head := strings.ToLower(s[:min(len(s), 512)])
	return strings.Contains(head, "<html") || strings.Contains(head, "<!doctype html")
}

// stripHTML removes scripts, tags and excess whitespace, then decodes the
// few entities that matter for reading.
func stripHTML(s string) string {
	s = reScript.ReplaceAllString(s, "")
	s = reTag.ReplaceAllString(s, " ")
	for _, e := range entityReplacements {
		s = strings.ReplaceAll(s, e[0], e[1])
	}
	s = reSpace.ReplaceAllString(s, " ")
	s = reBlank.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}

var entityReplacements = [][2]string{
	{"&nbsp;", " "}, {"&amp;", "&"}, {"&lt;", "<"}, {"&gt;", ">"},
	{"&quot;", `"`}, {"&#39;", "'"}, {"&#x27;", "'"},
}

// Structural regexes for toMarkdown: reAnchor links, reBlock block wrappers,
// reListItem list items, reStrong/reEm inline emphasis, reCode inline code,
// rePre fenced blocks, reBR line breaks, reTD table cells. (RE2 has no
// backreferences, so emphasis/heading pairs match by explicit alternatives.)
var (
	reAnchor   = regexp.MustCompile(`(?is)<a\s[^>]*href="([^"]+)"[^>]*>(.*?)</a>`)
	reBlock    = regexp.MustCompile(`(?is)</?(p|div|section|article|header|footer|nav|blockquote|ul|ol|table|tr|tbody|thead)[^>]*>`)
	reListItem = regexp.MustCompile(`(?is)<li[^>]*>(.*?)</li>`)
	reStrong   = regexp.MustCompile(`(?is)<(?:strong|b)>(.*?)</(?:strong|b)>`)
	reEm       = regexp.MustCompile(`(?is)<(?:em|i)>(.*?)</(?:em|i)>`)
	reCode     = regexp.MustCompile(`(?is)<code[^>]*>(.*?)</code>`)
	rePre      = regexp.MustCompile(`(?is)<pre[^>]*>(.*?)</pre>`)
	reBR       = regexp.MustCompile(`(?i)<br\s*/?>`)
	reTD       = regexp.MustCompile(`(?is)</?(td|th)[^>]*>`)
	reComment  = regexp.MustCompile(`(?s)<!--.*?-->`)
)

// toMarkdown converts HTML into readable markdown: headings, links, lists,
// code fences and emphasis survive; everything else is stripped. Structure
// first, tags last — block markers are in place before reTag runs.
func toMarkdown(s string) string {
	s = reComment.ReplaceAllString(s, "")
	s = reScript.ReplaceAllString(s, "")
	// Fences before generic tag stripping so code keeps its payload.
	s = rePre.ReplaceAllString(s, "\n```\n$1\n```\n")
	for lvl := 6; lvl >= 1; lvl-- {
		re := regexp.MustCompile(`(?is)<h` + string(rune('0'+lvl)) + `[^>]*>(.*?)</h` + string(rune('0'+lvl)) + `>`)
		s = re.ReplaceAllString(s, strings.Repeat("#", lvl)+" $1\n")
	}
	s = reListItem.ReplaceAllString(s, "\n- $1")
	s = reAnchor.ReplaceAllString(s, "[$2]($1)")
	s = reStrong.ReplaceAllString(s, "**$1**")
	s = reEm.ReplaceAllString(s, "*$1*")
	s = reCode.ReplaceAllString(s, "`$1`")
	s = reTD.ReplaceAllString(s, " | ")
	s = reBR.ReplaceAllString(s, "\n")
	s = reBlock.ReplaceAllString(s, "\n")
	s = reTag.ReplaceAllString(s, "")
	for _, e := range entityReplacements {
		s = strings.ReplaceAll(s, e[0], e[1])
	}
	s = reSpace.ReplaceAllString(s, " ")
	s = regexp.MustCompile(` +\n`).ReplaceAllString(s, "\n")
	s = reBlank.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}
