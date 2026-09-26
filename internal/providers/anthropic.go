package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// anthropic speaks the Anthropic messages API.
type anthropic struct {
	name    string
	baseURL string
	apiKey  string
}

func (a *anthropic) Name() string { return a.name }

type anthropicRequest struct {
	Model string `json:"model"`
	// System is a plain string, or (prompt caching, §4) a text block
	// array whose last entry carries cache_control. nil omits it.
	System    any             `json:"system,omitempty"`
	Messages  []anthropicMsg  `json:"messages"`
	Tools     []anthropicTool `json:"tools,omitempty"`
	MaxTokens int             `json:"max_tokens"`
	Stream    bool            `json:"stream"`
	Thinking  any             `json:"thinking,omitempty"` // {type, budget_tokens}
}

// anthropicSystemBlock is one system block; the last one may carry the
// cache breakpoint (cache_control) so the system+tools prefix caches.
type anthropicSystemBlock struct {
	Type         string             `json:"type"`
	Text         string             `json:"text"`
	CacheControl *anthropicCacheCtl `json:"cache_control,omitempty"`
}

type anthropicCacheCtl struct {
	Type string `json:"type"` // "ephemeral"
}

type anthropicMsg struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type anthropicTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema"`
}

type anthropicContent struct {
	Type  string          `json:"type"`
	Text  string          `json:"text,omitempty"`
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
	// Source is set on image/document blocks: base64 media.
	Source *anthropicSource `json:"source,omitempty"`
	// Content wraps the blocks inside a tool_result (text + images).
	Content json.RawMessage `json:"content,omitempty"`
}

type anthropicSource struct {
	Type      string `json:"type"` // "base64"
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

// anthropicSystem wraps the system prompt in a text block carrying the
// cache breakpoint: everything up to it (tools + system) caches (§4).
// An empty system stays absent.
func anthropicSystem(sys string) any {
	if sys == "" {
		return nil
	}
	return []anthropicSystemBlock{{
		Type:         "text",
		Text:         sys,
		CacheControl: &anthropicCacheCtl{Type: "ephemeral"},
	}}
}

// anthropicThinkingBudget lifts max_tokens above thinking.budget_tokens
// plus output headroom: the API rejects a budget that is not strictly
// below max_tokens (§4). Operates on the merged overlay payload.
func anthropicThinkingBudget(payload any) any {
	m, ok := payload.(map[string]any)
	if !ok {
		return payload
	}
	th, ok := m["thinking"].(map[string]any)
	if !ok {
		return payload
	}
	b, ok := th["budget_tokens"].(float64)
	if !ok {
		return payload
	}
	need := float64(int(b) + 4096)
	if cur, ok := m["max_tokens"].(float64); !ok || float64(cur) < need {
		m["max_tokens"] = need
	}
	return m
}

type anthropicEvent struct {
	Type  string `json:"type"`
	Delta *struct {
		Type        string `json:"type"`
		Text        string `json:"text,omitempty"`
		PartialJSON string `json:"partial_json,omitempty"`
	} `json:"delta"`
	ContentBlock *anthropicContent `json:"content_block"`
	Message      *struct {
		Usage *struct {
			InputTokens int `json:"input_tokens"`
		} `json:"usage"`
	} `json:"message"`
	Usage *struct {
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (a *anthropic) Stream(ctx context.Context, req Request) <-chan Chunk {
	out := make(chan Chunk)
	go func() {
		defer close(out)

		msgs, err := buildAnthropicMessages(req.Messages)
		if err != nil {
			send(ctx, out, Chunk{Err: err})
			return
		}
		maxTokens := req.MaxTokens
		if maxTokens <= 0 {
			maxTokens = 4096
		}
		body := anthropicRequest{
			Model:     req.Model,
			System:    anthropicSystem(req.System),
			Messages:  msgs,
			MaxTokens: maxTokens,
			Stream:    true,
			Thinking:  req.Body["thinking"], // forwarded if the agent set it
		}
		for _, t := range req.Tools {
			body.Tools = append(body.Tools, anthropicTool{
				Name:        t.Name,
				Description: t.Description,
				InputSchema: t.Parameters,
			})
		}
		headers := map[string]string{
			"x-api-key":         a.apiKey,
			"anthropic-version": "2023-06-01",
		}
		for k, v := range req.Headers {
			headers[k] = v
		}
		payload, err := withBodyOverlay(body, req.Body)
		if err != nil {
			send(ctx, out, Chunk{Err: err})
			return
		}
		payload = anthropicThinkingBudget(payload)
		resp, err := postSSE(ctx, a.baseURL+"/messages", headers, payload)
		if err != nil {
			send(ctx, out, Chunk{Err: err})
			return
		}
		defer resp.Body.Close()

		// input_tokens arrives on message_start, output_tokens on
		// message_delta; the pair is reported together at delta time.
		inputTokens := 0
		err = eachSSE(ctx, resp.Body, func(data string) error {
			var ev anthropicEvent
			if json.Unmarshal([]byte(data), &ev) != nil {
				return nil
			}
			if ev.Error != nil {
				return errors.New(a.name + ": " + ev.Error.Message)
			}
			switch ev.Type {
			case "message_start":
				if ev.Message != nil && ev.Message.Usage != nil {
					inputTokens = ev.Message.Usage.InputTokens
				}
			case "content_block_start":
				if ev.ContentBlock != nil && ev.ContentBlock.Type == "tool_use" {
					return sendErr(ctx, out, Chunk{ToolCalls: []ChunkTool{{
						ID:   ev.ContentBlock.ID,
						Name: ev.ContentBlock.Name,
					}}})
				}
			case "content_block_delta":
				if ev.Delta == nil {
					return nil
				}
				switch ev.Delta.Type {
				case "text_delta":
					if ev.Delta.Text != "" {
						return sendErr(ctx, out, Chunk{Text: ev.Delta.Text})
					}
				case "input_json_delta":
					if ev.Delta.PartialJSON != "" {
						return sendErr(ctx, out, Chunk{ToolCalls: []ChunkTool{{Arguments: ev.Delta.PartialJSON}}})
					}
				}
			case "message_delta":
				if ev.Usage == nil {
					return nil
				}
				return sendErr(ctx, out, Chunk{Usage: &Usage{
					Input:  inputTokens,
					Output: ev.Usage.OutputTokens,
				}})
			case "message_stop":
				return errDone
			}
			return nil
		})
		if err != nil && !errors.Is(err, errDone) {
			send(ctx, out, Chunk{Err: err})
		}
	}()
	return out
}

// buildAnthropicMessages converts the internal history into Anthropic's
// content-block form, tagging tool results with their call id. Media becomes
// image/document blocks — inside the tool_result when the media came from a
// tool, or alongside the text on a user/assistant message.
func buildAnthropicMessages(msgs []Message) ([]anthropicMsg, error) {
	out := make([]anthropicMsg, 0, len(msgs))
	for _, m := range msgs {
		switch m.Role {
		case "user", "assistant":
			raw, err := json.Marshal(anthropicBlocks(m.Content, m.Media))
			if err != nil {
				return nil, err
			}
			out = append(out, anthropicMsg{Role: m.Role, Content: raw})
		case "tool":
			var raw []byte
			var err error
			if len(m.Media) > 0 {
				inner, ierr := json.Marshal(anthropicBlocks(m.Content, m.Media))
				if ierr != nil {
					return nil, ierr
				}
				raw, err = json.Marshal([]anthropicContent{{
					Type: "tool_result", ID: m.ToolCallID, Content: inner,
				}})
			} else {
				raw, err = json.Marshal([]anthropicContent{{
					Type: "tool_result", ID: m.ToolCallID, Text: m.Content,
				}})
			}
			if err != nil {
				return nil, err
			}
			out = append(out, anthropicMsg{Role: "user", Content: raw})
		}
	}
	return out, nil
}

// anthropicBlocks builds [text?, image/document blocks...]: text is omitted
// when empty (Anthropic rejects empty text blocks). PDFs map to document
// blocks; other non-image types degrade to a text note.
func anthropicBlocks(text string, media []Media) []anthropicContent {
	var blocks []anthropicContent
	for _, med := range media {
		switch {
		case strings.HasPrefix(med.Type, "image/"):
			blocks = append(blocks, anthropicContent{
				Type: "image", Source: &anthropicSource{Type: "base64", MediaType: med.Type, Data: med.Data},
			})
		case med.Type == "application/pdf":
			blocks = append(blocks, anthropicContent{
				Type: "document", Source: &anthropicSource{Type: "base64", MediaType: med.Type, Data: med.Data},
			})
		default:
			text += fmt.Sprintf("\n[attached %s (%s)]", med.Type, med.Name)
		}
	}
	if text != "" || len(blocks) == 0 {
		blocks = append([]anthropicContent{{Type: "text", Text: text}}, blocks...)
	}
	return blocks
}
