package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// openAICompat speaks the OpenAI chat-completions dialect. It covers OpenAI,
// OpenRouter, Groq, Google's compatibility endpoint, and any custom base URL.
type openAICompat struct {
	name    string
	baseURL string
	apiKey  string
}

func (o *openAICompat) Name() string { return o.name }

type openAIRequest struct {
	Model         string            `json:"model"`
	Messages      []openAIMessage   `json:"messages"`
	Tools         []openAITool      `json:"tools,omitempty"`
	Stream        bool              `json:"stream"`
	MaxTokens     int               `json:"max_tokens,omitempty"`
	StreamOptions *openAIStreamOpts `json:"stream_options,omitempty"`
}

type openAIStreamOpts struct {
	IncludeUsage bool `json:"include_usage"`
}

type openAIImageURL struct {
	URL string `json:"url"`
}

// openAIContent is one content part: text, or an image data URI. OpenAI
// messages carry either a bare string or an array of these.
type openAIContent struct {
	Type     string          `json:"type"`
	Text     string          `json:"text,omitempty"`
	ImageURL *openAIImageURL `json:"image_url,omitempty"`
}

type openAIMessage struct {
	Role       string           `json:"role"`
	Content    any              `json:"content"` // string, or []openAIContent with media
	ToolCalls  []openAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
}

type openAITool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string         `json:"name"`
		Description string         `json:"description,omitempty"`
		Parameters  map[string]any `json:"parameters,omitempty"`
	} `json:"function"`
}

type openAIToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type openAIChunk struct {
	Choices []struct {
		Delta struct {
			Content   string           `json:"content"`
			ToolCalls []openAIToolCall `json:"tool_calls"`
		} `json:"delta"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int     `json:"prompt_tokens"`
		CompletionTokens int     `json:"completion_tokens"`
		Cost             float64 `json:"cost"` // OpenRouter extension
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (o *openAICompat) Stream(ctx context.Context, req Request) <-chan Chunk {
	out := make(chan Chunk)
	go func() {
		defer close(out)

		body := openAIRequest{
			Model:         req.Model,
			Messages:      buildOpenAIMessages(req.Messages),
			Stream:        true,
			MaxTokens:     req.MaxTokens,
			StreamOptions: &openAIStreamOpts{IncludeUsage: true},
		}
		for _, t := range req.Tools {
			tool := openAITool{Type: "function"}
			tool.Function.Name = t.Name
			tool.Function.Description = t.Description
			tool.Function.Parameters = t.Parameters
			body.Tools = append(body.Tools, tool)
		}
		if req.System != "" {
			body.Messages = append([]openAIMessage{{Role: "system", Content: req.System}}, body.Messages...)
		}
		headers := map[string]string{}
		if o.apiKey != "" {
			headers["Authorization"] = "Bearer " + o.apiKey
		}
		for k, v := range req.Headers {
			headers[k] = v
		}
		payload, err := withBodyOverlay(body, req.Body)
		if err != nil {
			send(ctx, out, Chunk{Err: err})
			return
		}
		resp, err := postSSE(ctx, o.baseURL+"/chat/completions", headers, payload)
		if err != nil {
			send(ctx, out, Chunk{Err: err})
			return
		}
		defer resp.Body.Close()

		// Streamed tool calls arrive as deltas keyed by index; we forward each
		// delta and let the engine accumulate them by position.
		err = eachSSE(ctx, resp.Body, func(data string) error {
			if data == "[DONE]" {
				return errDone
			}
			var c openAIChunk
			if json.Unmarshal([]byte(data), &c) != nil {
				return nil
			}
			if c.Error != nil {
				return errors.New(o.name + ": " + c.Error.Message)
			}
			// The usage summary arrives on a final chunk with no choices.
			if c.Usage != nil {
				return sendErr(ctx, out, Chunk{Usage: &Usage{
					Input:   c.Usage.PromptTokens,
					Output:  c.Usage.CompletionTokens,
					CostUSD: c.Usage.Cost,
				}})
			}
			if len(c.Choices) == 0 {
				return nil
			}
			d := c.Choices[0].Delta
			if d.Content != "" {
				if err := sendErr(ctx, out, Chunk{Text: d.Content}); err != nil {
					return err
				}
			}
			for _, tc := range d.ToolCalls {
				return sendErr(ctx, out, Chunk{ToolCalls: []ChunkTool{{
					ID:        tc.ID,
					Name:      tc.Function.Name,
					Arguments: tc.Function.Arguments,
				}}})
			}
			return nil
		})
		if err != nil && !errors.Is(err, errDone) {
			send(ctx, out, Chunk{Err: err})
		}
	}()
	return out
}

// buildOpenAIMessages converts internal history into the OpenAI wire format,
// attaching system text and threading tool results by call id. Messages with
// media become content-part arrays; a tool result carrying images emits its
// text as the tool message plus a follow-up user message with the images,
// because the tool role has no image support.
func buildOpenAIMessages(msgs []Message) []openAIMessage {
	out := make([]openAIMessage, 0, len(msgs)+1)
	for _, m := range msgs {
		switch m.Role {
		case "user", "assistant":
			if len(m.Media) == 0 {
				out = append(out, openAIMessage{Role: m.Role, Content: m.Content})
				continue
			}
			out = append(out, openAIMessage{Role: m.Role, Content: openAIContentParts(m.Content, m.Media)})
		case "tool":
			out = append(out, openAIMessage{Role: "tool", Content: m.Content, ToolCallID: m.ToolCallID})
			if len(m.Media) > 0 {
				out = append(out, openAIMessage{Role: "user", Content: openAIContentParts("", m.Media)})
			}
		}
	}
	return out
}

// openAIContentParts builds the part array: one text part (kept even when
// empty so the message is never content-less), then an image part per media
// item. Non-image media (PDF) is OpenAI-unsupported and stays a text note.
func openAIContentParts(text string, media []Media) []openAIContent {
	parts := []openAIContent{{Type: "text", Text: text}}
	for _, med := range media {
		if strings.HasPrefix(med.Type, "image/") {
			parts = append(parts, openAIContent{
				Type:     "image_url",
				ImageURL: &openAIImageURL{URL: "data:" + med.Type + ";base64," + med.Data},
			})
			continue
		}
		parts[0].Text += fmt.Sprintf("\n[attached %s (%s)]", med.Type, med.Name)
	}
	return parts
}
