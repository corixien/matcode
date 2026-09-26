package commands

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withAPIProject creates a temp project tree with a minimal config and
// chdirs into it (sessionConfig reads config.toml from the cwd).
func withAPIProject(t *testing.T, tomlText string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".mtc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if tomlText != "" {
		if err := os.WriteFile(filepath.Join(dir, ".mtc", "config.toml"), []byte(tomlText), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
	return dir
}

func TestAPIValidation(t *testing.T) {
	cases := [][]string{
		{},
		{"PUT", "/x"},
		{"GET"},
		{"GET", "no-slash"},
		{"GET", "/x", "/y"},
		{"GET", "/x", "-timeout", "bogus"},
		{"GET", "/x", "-H", "nocolon"},
	}
	for _, args := range cases {
		if err := API(args); err == nil {
			t.Errorf("API(%q) = nil, want error", args)
		}
	}
}

func TestAPIGetPost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/health":
			if r.Header.Get("X-Test") != "1" {
				t.Errorf("missing -H header, got %q", r.Header.Get("X-Test"))
			}
			w.Write([]byte(`{"ok":true}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/echo":
			b, _ := io.ReadAll(r.Body)
			if r.Header.Get("Content-Type") != "application/json" {
				t.Errorf("content-type = %q", r.Header.Get("Content-Type"))
			}
			w.Write([]byte(`{"echo":"` + string(b) + `"}`))
		case r.URL.Path == "/api/boom":
			http.Error(w, `{"error":"nope"}`, 404)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	out := captureStdout(t, func() error {
		return API([]string{"-url", srv.URL, "-H", "X-Test: 1", "GET", "/api/health"})
	})
	if !strings.Contains(out, `{"ok":true}`) {
		t.Errorf("GET body = %q", out)
	}

	out = captureStdout(t, func() error {
		return API([]string{"-url", srv.URL, "-data", `{"a":1}`, "POST", "/api/echo"})
	})
	if !strings.Contains(out, `{"echo":"{"a":1}"}`) {
		t.Errorf("POST body = %q", out)
	}

	if err := API([]string{"-url", srv.URL, "GET", "/api/boom"}); err == nil {
		t.Error("404 = nil error, want error")
	} else if !strings.Contains(err.Error(), "HTTP 404") || !strings.Contains(err.Error(), "nope") {
		t.Errorf("404 error = %v", err)
	}

	// connection refused surfaces a hint
	if err := API([]string{"-url", "http://127.0.0.1:1", "GET", "/x"}); err == nil {
		t.Error("refused = nil error")
	} else if !strings.Contains(err.Error(), "mtc serve") {
		t.Errorf("refused error = %v", err)
	}
}

func TestAPIDataFromFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		w.Write(b)
	}))
	defer srv.Close()

	dir := withAPIProject(t, "")
	name := filepath.Join(dir, "body.json")
	if err := os.WriteFile(name, []byte(`{"from":"file"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() error {
		return API([]string{"-url", srv.URL, "-data", "@" + name, "POST", "/x"})
	})
	if !strings.Contains(out, `{"from":"file"}`) {
		t.Errorf("body = %q", out)
	}
}

func TestAPIURLFromConfigPort(t *testing.T) {
	// No -url: the default comes from config api_port (8787 unless set).
	withAPIProject(t, "api_port = 9999\n")
	err := API([]string{"GET", "/api/health"})
	if err == nil {
		t.Fatal("expected connection error against configured port")
	}
	if !strings.Contains(err.Error(), "127.0.0.1:9999") {
		t.Errorf("error = %v, want 127.0.0.1:9999 (config api_port) in message", err)
	}
}
