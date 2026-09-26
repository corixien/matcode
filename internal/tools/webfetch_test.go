package tools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"matcode/internal/engine"
)

func execWebfetch(t *testing.T, input string) (engine.Result, error) {
	t.Helper()
	return Webfetch(t.TempDir()).Execute(context.Background(), json.RawMessage(input))
}

// webfetchServer serves a fixed body under the given content type.
func webfetchServer(t *testing.T, ct, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

const webfetchPage = `<!doctype html>
<html><head><title>Page</title><script>evil();</script><style>.x{}</style></head>
<body><h1>Title</h1><p>Visit <a href="https://example.com/a">Example</a> now.</p>
<ul><li>first</li><li>second</li></ul></body></html>`

// TestWebfetchMarkdown: default format turns HTML into structure — headings,
// links, list items survive; scripts and tags do not.
func TestWebfetchMarkdown(t *testing.T) {
	srv := webfetchServer(t, "text/html; charset=utf-8", webfetchPage)
	res, err := execWebfetch(t, `{"url":"`+srv.URL+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# Title", "[Example](https://example.com/a)", "- first", "- second"} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("markdown missing %q in:\n%s", want, res.Text)
		}
	}
	for _, bad := range []string{"<script", "<h1", "evil", "<html"} {
		if strings.Contains(res.Text, bad) {
			t.Errorf("markdown leaked %q:\n%s", bad, res.Text)
		}
	}
}

// TestWebfetchTextStripsTags: format=text keeps words, drops markup.
func TestWebfetchTextStripsTags(t *testing.T) {
	srv := webfetchServer(t, "text/html; charset=utf-8", webfetchPage)
	res, err := execWebfetch(t, `{"url":"`+srv.URL+`","format":"text"}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Title", "Example", "first"} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("text output missing %q: %q", want, res.Text)
		}
	}
	for _, bad := range []string{"<", "evil", "https://example.com"} {
		if strings.Contains(res.Text, bad) {
			t.Errorf("text output leaked %q: %q", bad, res.Text)
		}
	}
}

// TestWebfetchHTMLIsRaw: format=html returns the source untouched.
func TestWebfetchHTMLIsRaw(t *testing.T) {
	srv := webfetchServer(t, "text/html; charset=utf-8", webfetchPage)
	res, err := execWebfetch(t, `{"url":"`+srv.URL+`","format":"html"}`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != webfetchPage {
		t.Errorf("html output = %q, want the raw source", res.Text)
	}
}

// TestWebfetchPlainPassthrough: non-HTML bodies are not mangled by any
// conversion, and a wrong content type does not trigger HTML handling unless
// the body actually looks like markup.
func TestWebfetchPlainPassthrough(t *testing.T) {
	srv := webfetchServer(t, "text/plain", "plain\ncontent\n")
	res, err := execWebfetch(t, `{"url":"`+srv.URL+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "plain\ncontent" && res.Text != "plain\ncontent\n" {
		t.Errorf("plain body = %q, want it untouched", res.Text)
	}

	// Same content type, HTML payload: sniffing kicks in.
	mislabelled := webfetchServer(t, "text/plain", webfetchPage)
	res, err = execWebfetch(t, `{"url":"`+mislabelled.URL+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "# Title") || strings.Contains(res.Text, "<h1") {
		t.Errorf("mislabelled HTML not converted: %q", res.Text)
	}
}

// TestWebfetchNon2xx: HTTP errors reach the model as errors, bodies are not
// silently returned.
func TestWebfetchNon2xx(t *testing.T) {
	for _, status := range []int{404, 500} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			io.WriteString(w, "denied")
		}))
		_, err := execWebfetch(t, `{"url":"`+srv.URL+`"}`)
		if err == nil || !strings.Contains(err.Error(), "HTTP") {
			t.Errorf("status %d: err = %v, want an HTTP %d error", status, err, status)
		}
		srv.Close()
	}
}

// TestWebfetchTimeout: the timeout field bounds the request; a hanging
// server produces a timeout error instead of a hang.
func TestWebfetchTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // stall until the client gives up
	}))
	t.Cleanup(srv.Close)

	_, err := execWebfetch(t, `{"url":"`+srv.URL+`","timeout":100}`)
	if err == nil {
		t.Fatal("a stalled server must time out")
	}
	if !strings.Contains(err.Error(), "Timeout") {
		t.Errorf("err = %v, want a Client.Timeout error", err)
	}
}

// TestWebfetchOutputCap: results never exceed 256KiB regardless of size.
func TestWebfetchOutputCap(t *testing.T) {
	srv := webfetchServer(t, "text/plain", strings.Repeat("a", 300*1024))
	res, err := execWebfetch(t, `{"url":"`+srv.URL+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Text) != 256*1024 {
		t.Errorf("output = %d bytes, want the 256KiB cap", len(res.Text))
	}
}

// TestFetchBodyCap: the shared fetch reads at most 1MB from the wire.
func TestFetchBodyCap(t *testing.T) {
	srv := webfetchServer(t, "text/plain", strings.Repeat("a", 1024*1024+4096))
	b, _, err := fetch(context.Background(), srv.URL, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != 1024*1024 {
		t.Errorf("body = %d bytes, want the 1MB read cap", len(b))
	}
}

// TestWebfetchBadURL: malformed URLs fail before any request is made.
func TestWebfetchBadURL(t *testing.T) {
	if _, err := execWebfetch(t, `{"url":"://x"}`); err == nil {
		t.Fatal("a malformed URL must error")
	}
}
