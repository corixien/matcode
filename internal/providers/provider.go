package providers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Message is one turn in a conversation. ToolCallID marks a tool result.
// Media carries base64 attachments (images/PDF) when present.
type Message struct {
	Role       string // "user", "assistant", or "tool"
	Content    string
	ToolCallID string
	Media      []Media
}

// Media is one base64 attachment for the provider wire format.
type Media struct {
	Type string // mime type
	Data string // base64
	Name string
}

// ToolSchema describes a callable tool for the provider's wire format.
type ToolSchema struct {
	Name        string
	Description string
	Parameters  map[string]any
}

// Request is a provider-agnostic streaming request.
type Request struct {
	Model     string
	System    string
	Messages  []Message
	Tools     []ToolSchema
	MaxTokens int
	// Headers/Body are per-agent request overlays (frontmatter `request.*`):
	// headers override the defaults, body keys are merged top-level into the
	// marshaled payload. Neither is used by a plain config-only session.
	Headers map[string]string
	Body    map[string]any
}

// ChunkTool is a partial tool call accumulated across stream deltas.
type ChunkTool struct {
	ID        string
	Name      string
	Arguments string
}

// Usage is one response's token accounting as reported by the provider.
// CostUSD is only set when the provider prices the call itself (e.g.
// OpenRouter); otherwise it stays 0 until the models.dev catalog lands.
type Usage struct {
	Input   int
	Output  int
	CostUSD float64
}

// Chunk is one increment of streamed output. Text carries assistant prose;
// ToolCalls carries tool-call deltas indexed by their position in the request;
// Usage arrives at most once, on the stream's final chunk.
type Chunk struct {
	Text      string
	ToolCalls []ChunkTool
	Usage     *Usage
	Err       error
}

// Provider streams chat completions from one upstream service.
type Provider interface {
	Name() string
	Stream(ctx context.Context, req Request) <-chan Chunk
}

// errDone stops SSE parsing on a provider's end-of-stream marker.
var errDone = errors.New("stream done")

// postSSE posts a JSON body and returns the SSE response for reading.
func postSSE(ctx context.Context, url string, headers map[string]string, body any) (*http.Response, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("%s: %s: %s", url, resp.Status, strings.TrimSpace(string(b)))
	}
	return resp, nil
}

// withBodyOverlay merges a per-agent body overlay into a typed request body
// by round-tripping through JSON: only top-level keys are replaced, so a
// field the agent did not name keeps its dialect default.
func withBodyOverlay(body any, overlay map[string]any) (any, error) {
	if len(overlay) == 0 {
		return body, nil
	}
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	for k, v := range overlay {
		m[k] = v
	}
	return m, nil
}

// eachSSE reads Server-Sent Events and calls fn with each data payload. It
// returns when the stream ends, fn errors, or ctx is cancelled.
func eachSSE(ctx context.Context, body io.Reader, fn func(data string) error) error {
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	var data strings.Builder
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !sc.Scan() {
			return sc.Err()
		}
		line := sc.Text()
		switch {
		case line == "":
			if data.Len() == 0 {
				continue
			}
			payload := data.String()
			data.Reset()
			if err := fn(payload); err != nil {
				return err
			}
		case strings.HasPrefix(line, "data:"):
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
}

// sendErr reports whether the chunk was delivered; it abandons delivery if the
// caller stopped reading or the context ended.
func sendErr(ctx context.Context, out chan<- Chunk, c Chunk) error {
	select {
	case out <- c:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// send is sendErr without the error return, for fire-and-forget chunks.
func send(ctx context.Context, out chan<- Chunk, c Chunk) {
	select {
	case out <- c:
	case <-ctx.Done():
	}
}
