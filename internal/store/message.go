package store

import "encoding/json"

// ToolCall is an assistant's request to run a tool. It is persisted as one
// line in messages.jsonl alongside the messages that surround it.
type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Media is one attachment riding along a message: an image or PDF as base64.
// Images PNG/JPEG/GIF/WebP and PDFs up to 20 MiB are supported.
type Media struct {
	Type string `json:"type"` // mime type, e.g. "image/png"
	Data string `json:"data"` // base64
	Name string `json:"name,omitempty"`
}

// RoleCompaction marks a checkpoint event: one line of messages.jsonl whose
// Content is the checkpoint and Through is the last message it folds in. The
// transcript stays append-only; compaction rewrites context, never history.
const RoleCompaction = "compaction"

// Message is one line of messages.jsonl.
type Message struct {
	ID         string     `json:"id"`
	Role       string     `json:"role"` // user | assistant | tool | compaction
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Media      []Media    `json:"media,omitempty"`
	Through    string     `json:"through,omitempty"` // compaction: last folded message id
	Time       int64      `json:"time"`
}

// RawJSON reports whether a string is valid JSON, for arguments round-tripping.
func RawJSON(s string) json.RawMessage {
	if json.Valid([]byte(s)) {
		return json.RawMessage(s)
	}
	return json.RawMessage(`{}`)
}
