package engine

import "encoding/json"

// Event types the engine emits while a turn runs. One stream serves both
// `run --format json` (NDJSON on stdout) and the TUI's tool blocks.
const (
	EventToolStart = "tool.start"
	EventToolEnd   = "tool.end"
	EventCompact   = "compact"
	EventUsage     = "usage"
)

// Event is one structured step of a turn. Text deltas stay on Echo — that
// is the continuous stream — while events are the milestones around them.
type Event struct {
	Type   string          `json:"type"`
	Tool   string          `json:"tool,omitempty"`
	Input  json.RawMessage `json:"input,omitempty"`
	Output string          `json:"output,omitempty"`
	Text   string          `json:"text,omitempty"`
	Usage  *UsageEvent     `json:"usage,omitempty"`
}

// UsageEvent is the token/cost slice attached to a usage event. Its keys
// are fixed here rather than reusing the provider's struct, so consumers
// of `run --format json` never follow provider field renames.
type UsageEvent struct {
	Input  int     `json:"input"`
	Output int     `json:"output"`
	Cost   float64 `json:"cost"`
}
