package engine

import (
	"context"
	"reflect"
	"testing"

	"matcode/internal/providers"
	"matcode/internal/store"
)

// scriptProvider replays a fixed stream of chunks.
type scriptProvider struct {
	chunks []providers.Chunk
}

func (p *scriptProvider) Name() string { return "script" }

func (p *scriptProvider) Stream(_ context.Context, _ providers.Request) <-chan providers.Chunk {
	out := make(chan providers.Chunk, len(p.chunks))
	for _, c := range p.chunks {
		out <- c
	}
	close(out)
	return out
}

// callDelta is one streamed tool-call delta.
func callDelta(id, name, args string) providers.Chunk {
	return providers.Chunk{ToolCalls: []providers.ChunkTool{{ID: id, Name: name, Arguments: args}}}
}

// TestToolCallDeltaAccumulation pins how streamed deltas assemble into calls.
// Arguments must survive a provider that repeats id and name on every delta —
// shell.create.before reads the assembled "command" field (§8) — a new id must
// start a new call, and an id-less delta must append to the open one.
func TestToolCallDeltaAccumulation(t *testing.T) {
	tests := []struct {
		name   string
		chunks []providers.Chunk
		want   []store.ToolCall
	}{
		{
			name: "repeated id and name",
			chunks: []providers.Chunk{
				callDelta("call_1", "bash", ""),
				callDelta("call_1", "bash", `{"command":`),
				callDelta("call_1", "bash", ` "echo hi"}`),
			},
			want: []store.ToolCall{{ID: "call_1", Name: "bash", Arguments: `{"command": "echo hi"}`}},
		},
		{
			name: "id on the first delta only",
			chunks: []providers.Chunk{
				callDelta("call_1", "bash", `{"command":`),
				callDelta("", "", ` "echo hi"}`),
			},
			want: []store.ToolCall{{ID: "call_1", Name: "bash", Arguments: `{"command": "echo hi"}`}},
		},
		{
			name: "a new id starts a new call",
			chunks: []providers.Chunk{
				callDelta("call_1", "read", `{"path":"a`),
				callDelta("call_1", "read", `"}`),
				callDelta("call_2", "read", `{"path":"b"}`),
			},
			want: []store.ToolCall{
				{ID: "call_1", Name: "read", Arguments: `{"path":"a"}`},
				{ID: "call_2", Name: "read", Arguments: `{"path":"b"}`},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := &Engine{Provider: &scriptProvider{chunks: tt.chunks}, Model: "mockt/t", System: "sys"}
			_, calls, _, err := e.complete(context.Background(),
				[]store.Message{{Role: "user", Content: "hi"}})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(calls, tt.want) {
				t.Errorf("calls = %+v, want %+v", calls, tt.want)
			}
		})
	}
}
