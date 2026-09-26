package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestWithBodyOverlay proves the agent body overlay only replaces the keys
// it names (dialect defaults survive) and that absent overlay = untouched.
func TestWithBodyOverlay(t *testing.T) {
	type body struct {
		Model       string  `json:"model"`
		MaxTokens   int     `json:"max_tokens"`
		Temperature float64 `json:"temperature"`
	}
	base := body{Model: "m", MaxTokens: 4096, Temperature: 1}

	// No overlay: the original typed value comes back, no JSON round trip.
	got, err := withBodyOverlay(base, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got.(body); !ok {
		t.Errorf("no overlay must return the typed body, got %T", got)
	}

	got, err = withBodyOverlay(base, map[string]any{"temperature": 0.2, "stream": true})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if m["temperature"] != 0.2 {
		t.Errorf("temperature = %v", m["temperature"])
	}
	if m["stream"] != true {
		t.Errorf("stream = %v", m["stream"])
	}
	if m["max_tokens"] != float64(4096) {
		t.Errorf("max_tokens = %v, must keep its default", m["max_tokens"])
	}
	if m["model"] != "m" {
		t.Errorf("model = %v", m["model"])
	}
}

// TestRequestOverlaysOnTheWire stands up a fake OpenAI-compatible endpoint
// and proves the per-agent headers arrive and the body overlay is merged
// into the payload that actually goes out.
func TestRequestOverlaysOnTheWire(t *testing.T) {
	var gotHeader string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("X-Mtc-Agent")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	p := &openAICompat{name: "mockt", baseURL: srv.URL, apiKey: "k"}
	req := Request{
		Model:     "t",
		System:    "s",
		Messages:  []Message{{Role: "user", Content: "hi"}},
		MaxTokens: 64,
		Headers:   map[string]string{"X-Mtc-Agent": "build"},
		Body:      map[string]any{"temperature": 0.3},
	}
	var text strings.Builder
	for c := range p.Stream(context.Background(), req) {
		if c.Err != nil {
			t.Fatal(c.Err)
		}
		text.WriteString(c.Text)
	}
	if text.String() != "ok" {
		t.Errorf("stream text = %q", text.String())
	}
	if gotHeader != "build" {
		t.Errorf("X-Mtc-Agent = %q", gotHeader)
	}
	if gotBody["temperature"] != 0.3 {
		t.Errorf("temperature = %v", gotBody["temperature"])
	}
	if gotBody["model"] != "t" {
		t.Errorf("model = %v, overlay must not clobber dialect fields", gotBody["model"])
	}
	if gotBody["max_tokens"] != float64(64) {
		t.Errorf("max_tokens = %v", gotBody["max_tokens"])
	}
}
