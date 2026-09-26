package providers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// captureTransport intercepts every request postSSE would make. Nothing is
// dialed: the recorder hands back an empty 200 so the stream just ends.
type captureTransport struct {
	reqs   []*http.Request
	bodies [][]byte
}

func (c *captureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	var b []byte
	if r.Body != nil {
		b, _ = io.ReadAll(r.Body)
		r.Body.Close()
	}
	c.reqs = append(c.reqs, r)
	c.bodies = append(c.bodies, b)
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("")),
		Request:    r,
	}, nil
}

// captureClient routes http.DefaultClient (postSSE's client) into the
// recorder for the lifetime of the test.
func captureClient(t *testing.T) *captureTransport {
	t.Helper()
	c := &captureTransport{}
	prev := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: c}
	t.Cleanup(func() { http.DefaultClient = prev })
	return c
}

func drain(t *testing.T, chunks <-chan Chunk) {
	t.Helper()
	for c := range chunks {
		if c.Err != nil {
			t.Fatalf("stream error: %v", c.Err)
		}
	}
}

func bodyAt(t *testing.T, c *captureTransport, i int) map[string]any {
	t.Helper()
	if i >= len(c.bodies) {
		t.Fatalf("captured %d requests, want %d", len(c.bodies), i+1)
	}
	var m map[string]any
	if err := json.Unmarshal(c.bodies[i], &m); err != nil {
		t.Fatalf("request %d body: %v", i, err)
	}
	return m
}

// openAIImageURI reports whether any replayed message still carries the
// attachment as an OpenAI image part.
func openAIImageURI(body map[string]any, want string) bool {
	msgs, _ := body["messages"].([]any)
	for _, raw := range msgs {
		m, _ := raw.(map[string]any)
		parts, ok := m["content"].([]any)
		if !ok {
			continue
		}
		for _, p := range parts {
			pm, _ := p.(map[string]any)
			if pm["type"] != "image_url" {
				continue
			}
			if u, _ := pm["image_url"].(map[string]any); u["url"] == want {
				return true
			}
		}
	}
	return false
}

// anthropicImageData reports whether any replayed message still carries the
// attachment as an Anthropic base64 source block.
func anthropicImageData(body map[string]any, want string) bool {
	msgs, _ := body["messages"].([]any)
	for _, raw := range msgs {
		m, _ := raw.(map[string]any)
		blocks, ok := m["content"].([]any)
		if !ok {
			continue
		}
		for _, b := range blocks {
			bm, _ := b.(map[string]any)
			src, _ := bm["source"].(map[string]any)
			if src["data"] == want {
				return true
			}
		}
	}
	return false
}

// TestOpenAIReplaysMediaAcrossTurns proves the second turn's request — built
// from the whole transcript — re-sends turn one's attachment, and pins the
// OpenAI wire envelope (path, headers, stream flags, system message).
func TestOpenAIReplaysMediaAcrossTurns(t *testing.T) {
	c := captureClient(t)
	p := &openAICompat{name: "mock", baseURL: "http://fake.invalid/v1", apiKey: "dummy-key"}
	turn1 := []Message{
		{Role: "user", Content: "look", Media: []Media{{Type: "image/png", Data: "QUJD", Name: "a.png"}}},
		{Role: "assistant", Content: "seen"},
	}
	drain(t, p.Stream(context.Background(), Request{
		Model: "m", System: "sys", Messages: turn1, MaxTokens: 32,
	}))
	turn2 := append(append([]Message{}, turn1...), Message{Role: "user", Content: "and now?"})
	drain(t, p.Stream(context.Background(), Request{
		Model: "m", System: "sys", Messages: turn2,
		Headers: map[string]string{"X-Mtc-Agent": "build"},
	}))

	if len(c.reqs) != 2 {
		t.Fatalf("captured %d requests, want 2", len(c.reqs))
	}
	for i, r := range c.reqs {
		if r.URL.Host != "fake.invalid" || r.URL.Path != "/v1/chat/completions" {
			t.Errorf("req %d target = %s%s", i, r.URL.Host, r.URL.Path)
		}
		if r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Accept") != "text/event-stream" {
			t.Errorf("req %d headers = %v", i, r.Header)
		}
	}
	if got := c.reqs[0].Header.Get("Authorization"); got != "Bearer dummy-key" {
		t.Errorf("Authorization = %q", got)
	}
	if got := c.reqs[1].Header.Get("X-Mtc-Agent"); got != "build" {
		t.Errorf("overlay header = %q", got)
	}

	first := bodyAt(t, c, 0)
	if first["stream"] != true || first["model"] != "m" || first["max_tokens"] != float64(32) {
		t.Errorf("envelope = %v", first)
	}
	opts, _ := first["stream_options"].(map[string]any)
	if opts["include_usage"] != true {
		t.Errorf("stream_options = %v", opts)
	}
	msgs := first["messages"].([]any)
	if len(msgs) != 3 { // system + turn 1 (user, assistant)
		t.Fatalf("turn 1 messages = %d, want 3", len(msgs))
	}
	if sys, _ := msgs[0].(map[string]any); sys["role"] != "system" || sys["content"] != "sys" {
		t.Errorf("system message = %v", sys)
	}
	const uri = "data:image/png;base64,QUJD"
	if !openAIImageURI(first, uri) {
		t.Errorf("turn 1 payload lost the attachment")
	}

	second := bodyAt(t, c, 1)
	msgs = second["messages"].([]any)
	if len(msgs) != 4 { // system + turn 1 + turn 2 prompt
		t.Fatalf("turn 2 messages = %d, want 4", len(msgs))
	}
	if !openAIImageURI(second, uri) {
		t.Errorf("media not replayed on the next turn")
	}
	if last, _ := msgs[3].(map[string]any); last["role"] != "user" || last["content"] != "and now?" {
		t.Errorf("turn 2 prompt = %v", last)
	}
}

// TestAnthropicReplaysMediaAcrossTurns proves the same replay in the
// Anthropic dialect plus its envelope: /messages path, auth headers, the
// cached system block, and the max_tokens default when unset.
func TestAnthropicReplaysMediaAcrossTurns(t *testing.T) {
	c := captureClient(t)
	p := &anthropic{name: "mock", baseURL: "http://fake.invalid/v1", apiKey: "dummy-key"}
	turn1 := []Message{
		{Role: "user", Content: "look", Media: []Media{{Type: "image/png", Data: "QUJD", Name: "a.png"}}},
		{Role: "assistant", Content: "seen"},
	}
	drain(t, p.Stream(context.Background(), Request{Model: "m", System: "sys", Messages: turn1}))
	turn2 := append(append([]Message{}, turn1...), Message{Role: "user", Content: "and now?"})
	drain(t, p.Stream(context.Background(), Request{Model: "m", System: "sys", Messages: turn2}))

	if len(c.reqs) != 2 {
		t.Fatalf("captured %d requests, want 2", len(c.reqs))
	}
	for i, r := range c.reqs {
		if r.URL.Host != "fake.invalid" || r.URL.Path != "/v1/messages" {
			t.Errorf("req %d target = %s%s", i, r.URL.Host, r.URL.Path)
		}
		if r.Header.Get("x-api-key") != "dummy-key" || r.Header.Get("anthropic-version") != "2023-06-01" {
			t.Errorf("req %d auth headers = %v", i, r.Header)
		}
	}

	first := bodyAt(t, c, 0)
	if first["stream"] != true || first["model"] != "m" {
		t.Errorf("envelope = %v", first)
	}
	// MaxTokens unset → the dialect default applies.
	if first["max_tokens"] != float64(4096) {
		t.Errorf("max_tokens = %v, want default 4096", first["max_tokens"])
	}
	sys, _ := first["system"].([]any)
	if len(sys) != 1 || sys[0].(map[string]any)["text"] != "sys" {
		t.Fatalf("system = %v", sys)
	}
	cc, _ := sys[0].(map[string]any)["cache_control"].(map[string]any)
	if cc["type"] != "ephemeral" {
		t.Errorf("cache_control = %v", cc)
	}
	const data = "QUJD"
	if !anthropicImageData(first, data) {
		t.Errorf("turn 1 payload lost the attachment")
	}

	second := bodyAt(t, c, 1)
	msgs, _ := second["messages"].([]any)
	if len(msgs) != 3 { // user, assistant, turn 2 prompt
		t.Fatalf("turn 2 messages = %d, want 3", len(msgs))
	}
	if !anthropicImageData(second, data) {
		t.Errorf("media not replayed on the next turn")
	}
	if last, _ := msgs[2].(map[string]any); last["role"] != "user" {
		t.Errorf("turn 2 prompt = %v", last)
	}
}
