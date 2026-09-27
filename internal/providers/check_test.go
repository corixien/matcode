package providers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"matcode/internal/config"
)

// TestCheckKeyAccepts200: a reachable /models endpoint verifying the
// credential (or a public list) passes.
func TestCheckKeyAccepts200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"object":"list","data":[]}`))
	}))
	defer srv.Close()

	p := config.Provider{BaseURL: srv.URL, APIKey: config.EnvRef{Env: "X"}}
	if err := CheckKey(p, "sk-good"); err != nil {
		t.Errorf("CheckKey = %v, want nil", err)
	}
}

// TestCheckKeyRejectsUnauthorized: 401/403 mean the key is wrong.
func TestCheckKeyRejectsUnauthorized(t *testing.T) {
	for _, code := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusBadRequest} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(code)
		}))
		err := CheckKey(config.Provider{BaseURL: srv.URL}, "sk-bad")
		if err == nil || !strings.Contains(err.Error(), "rejected") {
			t.Errorf("code %d: CheckKey = %v, want rejected", code, err)
		}
		srv.Close()
	}
}

// TestCheckKeyTolerates: providers without an endpoint, transport
// failures, and empty input behave predictably: only the empty key is
// an error — an offline verifier must not block storing a credential.
func TestCheckKeyTolerates(t *testing.T) {
	if err := CheckKey(config.Provider{}, "sk-x"); err != nil {
		t.Errorf("empty BaseURL must not be probed: %v", err)
	}
	if err := CheckKey(config.Provider{}, ""); err == nil {
		t.Error("empty key: want error")
	}
	// Closed server → transport error → nil (best effort).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close()
	if err := CheckKey(config.Provider{BaseURL: url}, "sk-x"); err != nil {
		t.Errorf("refused connection = %v, want nil", err)
	}
	// 404: no /models route on this gateway → cannot verify → nil.
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv2.Close()
	if err := CheckKey(config.Provider{BaseURL: srv2.URL}, "sk-x"); err != nil {
		t.Errorf("404 = %v, want nil", err)
	}
}

// TestCheckKeyAuthHeaders: dialects pick the header the provider reads.
func TestCheckKeyAuthHeaders(t *testing.T) {
	var gotAPIKey, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAPIKey = r.Header.Get("x-api-key")
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := CheckKey(config.Provider{Dialect: "anthropic", BaseURL: srv.URL}, "sk-a"); err != nil {
		t.Fatal(err)
	}
	if gotAPIKey != "sk-a" || gotAuth != "" {
		t.Errorf("anthropic: x-api-key=%q auth=%q", gotAPIKey, gotAuth)
	}
	if err := CheckKey(config.Provider{Dialect: "openai", BaseURL: srv.URL}, "sk-o"); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer sk-o" || gotAPIKey != "" {
		t.Errorf("openai: auth=%q x-api-key=%q", gotAuth, gotAPIKey)
	}
}
