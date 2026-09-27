package providers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"matcode/internal/config"
)

// TestListModelsParsesDataIDs: the OpenAI-shaped {"data":[{"id":…}]}
// body is the whole contract — every id comes back, in order, with
// duplicates dropped.
func TestListModelsParsesDataIDs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("path = %q, want /models", r.URL.Path)
		}
		w.Write([]byte(`{"object":"list","data":[` +
			`{"id":"claude-fable-5"},{"id":"claude-opus-5-5"},` +
			`{"id":"claude-fable-5"}]}`))
	}))
	defer srv.Close()

	got, err := ListModels(config.Provider{BaseURL: srv.URL}, "sk-live")
	if err != nil {
		t.Fatalf("ListModels = %v, want nil", err)
	}
	want := []string{"claude-fable-5", "claude-opus-5-5"}
	if len(got) != len(want) {
		t.Fatalf("ListModels = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ListModels = %v, want %v", got, want)
		}
	}
}

// TestListModelsAuthHeaders: the same dialect split CheckKey uses.
func TestListModelsAuthHeaders(t *testing.T) {
	var gotKey, gotVer, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("x-api-key")
		gotVer = r.Header.Get("anthropic-version")
		gotAuth = r.Header.Get("Authorization")
		w.Write([]byte(`{"data":[{"id":"m"}]}`))
	}))
	defer srv.Close()

	if _, err := ListModels(config.Provider{BaseURL: srv.URL, Dialect: "anthropic"}, "sk-a"); err != nil {
		t.Fatalf("anthropic ListModels = %v", err)
	}
	if gotKey != "sk-a" || gotVer == "" {
		t.Errorf("anthropic headers = x-api-key %q version %q", gotKey, gotVer)
	}
	if _, err := ListModels(config.Provider{BaseURL: srv.URL}, "sk-b"); err != nil {
		t.Fatalf("ListModels = %v", err)
	}
	if gotAuth != "Bearer sk-b" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer sk-b")
	}
}

// TestListModelsRejectsUnusableAnswers: a non-200, an unparsable body,
// or an empty catalogue is an error so the picker falls back to its
// static list instead of showing nothing.
func TestListModelsRejectsUnusableAnswers(t *testing.T) {
	for _, tc := range []struct {
		name string
		code int
		body string
	}{
		{"unauthorized", http.StatusUnauthorized, `{"data":[{"id":"m"}]}`},
		{"html", http.StatusOK, "<html>gateway</html>"},
		{"empty", http.StatusOK, `{"data":[]}`},
		{"no-ids", http.StatusOK, `{"data":[{"object":"model"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.code)
				w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			if got, err := ListModels(config.Provider{BaseURL: srv.URL}, "sk"); err == nil {
				t.Fatalf("ListModels = %v, want error", got)
			}
		})
	}
}

// TestListModelsNeedsAKeyAndABaseURL: without either there is nothing
// to ask.
func TestListModelsNeedsAKeyAndABaseURL(t *testing.T) {
	if _, err := ListModels(config.Provider{BaseURL: "http://x"}, ""); err == nil {
		t.Error("empty key accepted")
	}
	if _, err := ListModels(config.Provider{}, "sk"); err == nil {
		t.Error("missing base url accepted")
	}
}
