package commands

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// headers is a repeatable -H flag: `-H "k: v" -H "k2: v2"`.
type headers []string

func (h *headers) String() string { return strings.Join(*h, ", ") }
func (h *headers) Set(v string) error {
	if !strings.Contains(v, ":") {
		return fmt.Errorf("header %q must be \"Name: value\"", v)
	}
	*h = append(*h, v)
	return nil
}

// API performs one HTTP request against the running server (mtc serve).
//
//	mtc api [-url base] [-timeout 30s] [-H "k: v"]... [-data body|@file] GET|POST <path>
//
// The response body goes to stdout; a non-2xx status is an error carrying
// the status and body.
func API(args []string) error {
	fs := flag.NewFlagSet("api", flag.ContinueOnError)
	var hdrs headers
	var url, data, timeout string
	fs.Var(&hdrs, "H", "request header \"Name: value\" (repeatable)")
	fs.StringVar(&url, "url", "", "server base URL (default http://127.0.0.1:<api_port>)")
	fs.StringVar(&data, "data", "", "request body; @file reads a file, - reads stdin")
	fs.StringVar(&timeout, "timeout", "30s", "request timeout (e.g. 30s, 2m)")
	if err := parse(fs, args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) != 2 {
		return fmt.Errorf("usage: mtc api [flags] GET|POST <path>")
	}
	method := strings.ToUpper(rest[0])
	if method != "GET" && method != "POST" {
		return fmt.Errorf("api: method must be GET or POST, got %q", rest[0])
	}
	path := rest[1]
	if !strings.HasPrefix(path, "/") {
		return fmt.Errorf("api: path must start with / (got %q)", path)
	}
	d, err := time.ParseDuration(timeout)
	if err != nil || d <= 0 {
		return fmt.Errorf("api: invalid -timeout %q", timeout)
	}

	if url == "" {
		cfg, _, err := sessionConfig()
		if err != nil {
			return err
		}
		url = cfg.APIBaseURL()
	}
	base := strings.TrimRight(url, "/")

	var body io.Reader
	if data != "" {
		b, err := readData(data)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, base+path, body)
	if err != nil {
		return fmt.Errorf("api: %w", err)
	}
	if data != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, h := range hdrs {
		i := strings.Index(h, ":")
		req.Header.Set(strings.TrimSpace(h[:i]), strings.TrimSpace(h[i+1:]))
	}

	resp, err := (&http.Client{Timeout: d}).Do(req)
	if err != nil {
		return fmt.Errorf("api: %s %s: %w (is `mtc serve` running?)", method, path, err)
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("api: reading response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("api: %s %s: HTTP %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(out)))
	}
	if len(out) > 0 {
		fmt.Print(string(out))
		if out[len(out)-1] != '\n' {
			fmt.Println()
		}
	}
	return nil
}

// readData resolves the -data value: "@file" reads a file, "-" reads stdin,
// anything else is the literal body.
func readData(v string) ([]byte, error) {
	switch {
	case v == "-":
		return io.ReadAll(os.Stdin)
	case strings.HasPrefix(v, "@"):
		b, err := os.ReadFile(v[1:])
		if err != nil {
			return nil, fmt.Errorf("api: -data: %w", err)
		}
		return b, nil
	default:
		return []byte(v), nil
	}
}
