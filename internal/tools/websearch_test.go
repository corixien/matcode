package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"matcode/internal/engine"
)

type websearchRoundTrip func(*http.Request) (*http.Response, error)

func (f websearchRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// stubWebsearchHTTP routes the tool's hardcoded endpoint through fn instead
// of the network — Websearch has no client injection point of its own.
func stubWebsearchHTTP(t *testing.T, fn func(*http.Request) (*http.Response, error)) {
	t.Helper()
	prev := http.DefaultTransport
	http.DefaultTransport = websearchRoundTrip(fn)
	t.Cleanup(func() { http.DefaultTransport = prev })
}

func websearchResponse(r *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:     http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    r,
	}
}

func execWebsearch(t *testing.T, input string) (engine.Result, error) {
	t.Helper()
	return Websearch(t.TempDir()).Execute(context.Background(), json.RawMessage(input))
}

// TestWebsearchFormatsResults: numbered title+URL lines, the query escaped
// onto the lite endpoint, no API key involved.
func TestWebsearchFormatsResults(t *testing.T) {
	var got *http.Request
	stubWebsearchHTTP(t, func(r *http.Request) (*http.Response, error) {
		got = r
		return websearchResponse(r, http.StatusOK,
			`<html><body>
<a href="https://lite.duckduckgo.com/lite/?q=nav">nav</a>
<a href="https://example.com/one">Example <b>One</b></a>
<a href="https://example.com/one">Example One duplicate</a>
<a href="https://example.net/">Net</a>
<a href="https://example.org/blank"></a>
</body></html>`), nil
	})

	res, err := execWebsearch(t, `{"query":"go lang"}`)
	if err != nil {
		t.Fatal(err)
	}
	want := "1. Example One\n   https://example.com/one\n" +
		"2. Net\n   https://example.net/"
	if res.Text != want {
		t.Errorf("output =\n%s\nwant\n%s", res.Text, want)
	}
	if got == nil {
		t.Fatal("no request issued")
	}
	if got.Method != http.MethodGet {
		t.Errorf("method = %s, want GET", got.Method)
	}
	if got.URL.String() != "https://lite.duckduckgo.com/lite/?q=go+lang" {
		t.Errorf("url = %q, want the escaped lite endpoint", got.URL)
	}
	if ua := got.Header.Get("User-Agent"); ua != "matcode/0.1" {
		t.Errorf("User-Agent = %q", ua)
	}
}

// TestWebsearchNoResultsIsError: an unparseable layout fails loudly instead
// of returning an empty list.
func TestWebsearchNoResultsIsError(t *testing.T) {
	stubWebsearchHTTP(t, func(r *http.Request) (*http.Response, error) {
		return websearchResponse(r, http.StatusOK, `<html><body>no results here</body></html>`), nil
	})
	_, err := execWebsearch(t, `{"query":"nothing"}`)
	if err == nil || !strings.Contains(err.Error(), "no results parsed") {
		t.Errorf("err = %v, want the parse failure", err)
	}
}

// TestWebsearchHTTPError: upstream failures surface as errors.
func TestWebsearchHTTPError(t *testing.T) {
	stubWebsearchHTTP(t, func(r *http.Request) (*http.Response, error) {
		return websearchResponse(r, http.StatusInternalServerError, "boom"), nil
	})
	_, err := execWebsearch(t, `{"query":"x"}`)
	if err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Errorf("err = %v, want HTTP 500", err)
	}
}

// TestWebsearchTransportError: a dead transport is reported as-is.
func TestWebsearchTransportError(t *testing.T) {
	stubWebsearchHTTP(t, func(r *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("transport down")
	})
	_, err := execWebsearch(t, `{"query":"x"}`)
	if err == nil || !strings.Contains(err.Error(), "transport down") {
		t.Errorf("err = %v, want the transport failure", err)
	}
}

// TestParseResultsFilters: internal links, empty titles and duplicates are
// dropped, and the list stops at ten.
func TestParseResultsFilters(t *testing.T) {
	var b strings.Builder
	b.WriteString(`<a href="https://duckduckgo.com/">ddg</a>`)
	b.WriteString(`<a href="https://a.example/">Alpha</a>`)
	b.WriteString(`<a href="https://a.example/">Alpha again</a>`)
	b.WriteString(`<a href="https://b.example/"><b></b></a>`)
	b.WriteString(`<a href="/relative">rel</a>`)
	for i := 0; i < 15; i++ {
		fmt.Fprintf(&b, `<a href="https://site%d.example/">Site %d</a>`, i, i)
	}

	got := parseResults(b.String())
	if len(got) != 10 {
		t.Fatalf("got %d results, want the cap of 10", len(got))
	}
	if got[0].title != "Alpha" || got[0].url != "https://a.example/" {
		t.Errorf("first = %+v, want Alpha", got[0])
	}
	for i, r := range got[1:] {
		if r.url != fmt.Sprintf("https://site%d.example/", i) || r.title != fmt.Sprintf("Site %d", i) {
			t.Errorf("result %d = %+v", i+1, r)
		}
	}
	if parseResults("<html><body>nothing</body></html>") != nil {
		t.Error("no links must parse to no results")
	}
}

// TestWebsearchRegistration: the tool ships under the id the registry uses,
// with a single required query.
func TestWebsearchRegistration(t *testing.T) {
	tool := Websearch(t.TempDir())
	if tool.ID != "websearch" {
		t.Errorf("id = %q", tool.ID)
	}
	req, _ := tool.Schema["required"].([]string)
	if len(req) != 1 || req[0] != "query" {
		t.Errorf("required = %v, want [query]", tool.Schema["required"])
	}
}
