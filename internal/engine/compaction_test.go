package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"matcode/internal/providers"
	"matcode/internal/store"
)

// fakeProvider records requests and returns a fixed reply with no tool calls.
type fakeProvider struct {
	reqs []providers.Request
}

func (f *fakeProvider) Name() string { return "fake" }

func (f *fakeProvider) Stream(ctx context.Context, req providers.Request) <-chan providers.Chunk {
	f.reqs = append(f.reqs, req)
	out := make(chan providers.Chunk, 2)
	out <- providers.Chunk{Text: "ok"}
	out <- providers.Chunk{Usage: &providers.Usage{Input: 10, Output: 5}}
	close(out)
	return out
}

func newSession(t *testing.T) *store.Session {
	t.Helper()
	s, err := store.Create(t.TempDir(), "mockt/t")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func mustAppend(t *testing.T, s *store.Session, m *store.Message) {
	t.Helper()
	if err := s.Append(m); err != nil {
		t.Fatal(err)
	}
}

// addExchange appends one assistant tool call plus its result.
func addExchange(t *testing.T, s *store.Session, id, name, args, res string) {
	t.Helper()
	mustAppend(t, s, &store.Message{
		Role:      "assistant",
		ToolCalls: []store.ToolCall{{ID: id, Name: name, Arguments: args}},
	})
	mustAppend(t, s, &store.Message{Role: "tool", Content: res, ToolCallID: id})
}

func TestEstimateTokens(t *testing.T) {
	if got := EstimateTokens(""); got != 0 {
		t.Errorf("empty = %d, want 0", got)
	}
	if got := EstimateTokens(strings.Repeat("word ", 100)); got <= 0 {
		t.Errorf("non-empty = %d, want > 0", got)
	}
}

func TestCollapseDupsAndErrors(t *testing.T) {
	raw := []string{
		"write(a.go) → done",
		"write(a.go) → done",
		"write(a.go) → done",
		"read(b.go) → (ERROR) error: permission denied",
	}
	got := collapseDups(raw)
	if len(got) != 2 {
		t.Fatalf("got %d lines: %q", len(got), got)
	}
	if !strings.HasSuffix(got[0], "×3") {
		t.Errorf("dup line = %q, want ×3", got[0])
	}
	if !strings.Contains(got[1], "(ERROR)") {
		t.Errorf("error line = %q, want (ERROR)", got[1])
	}
	if !isErrorResult("denied by permissions.toml") || !isErrorResult("unknown tool: foo") {
		t.Error("denied/unknown tool should read as errors")
	}
	if isErrorResult("ok, 12 lines") {
		t.Error("normal result misread as error")
	}
}

func TestExtractCaps(t *testing.T) {
	dir := t.TempDir()
	var fold []store.Message
	fold = append(fold, store.Message{Role: "user", Content: "do the thing"})
	for i := 0; i < maxChanges+40; i++ {
		id := "c" + string(rune('a'+i%26)) + string(rune('a'+i/26))
		fold = append(fold,
			store.Message{Role: "assistant", ToolCalls: []store.ToolCall{{ID: id, Name: "write", Arguments: `{"path":"f` + id + `.go"}`}}},
			store.Message{Role: "tool", ToolCallID: id, Content: "wrote " + id},
		)
	}
	changes, decisions, _ := foldChanges(fold)
	if len(changes) != maxChanges {
		t.Errorf("changes = %d, want cap %d", len(changes), maxChanges)
	}
	if len(decisions) != 0 {
		t.Errorf("decisions = %d, want 0", len(decisions))
	}
	if round := extract(dir, fold, checkpoint{}); round != 1 {
		t.Errorf("round = %d, want 1", round)
	}
	arch, err := os.ReadFile(filepath.Join(dir, "compaction", "archive.md"))
	if err != nil {
		t.Fatal(err)
	}
	a := string(arch)
	for _, want := range []string{"## Overall", "- Round 1: do the thing", "### Round 1", "Objective: do the thing", "Changes:", "Decisions:"} {
		if !strings.Contains(a, want) {
			t.Errorf("archive missing %q", want)
		}
	}
	cp, err := os.ReadFile(filepath.Join(dir, "checkpoint.md"))
	if err != nil {
		t.Fatal(err)
	}
	c := string(cp)
	for _, want := range []string{"## Objective", "## Done", "## Next Move", "## Blockers/Facts", "## Archive", "compaction/archive.md (round 1)"} {
		if !strings.Contains(c, want) {
			t.Errorf("checkpoint missing %q", want)
		}
	}
}

func TestExtractCarriesPreviousDoneAndBlockers(t *testing.T) {
	dir := t.TempDir()
	prev := checkpoint{
		Objective: "old objective",
		Done:      []string{"did old work"},
		Blockers:  []string{"error: old blocker verbatim"},
	}
	fold := []store.Message{
		{Role: "assistant", ToolCalls: []store.ToolCall{{ID: "c1", Name: "write", Arguments: `{"path":"x.go"}`}}},
		{Role: "tool", ToolCallID: "c1", Content: "error: boom"},
	}
	extract(dir, fold, prev)
	md := readFile(filepath.Join(dir, "checkpoint.md"))
	if !strings.Contains(md, "- did old work") {
		t.Error("previous Done bullet not carried")
	}
	if !strings.Contains(md, "error: boom") {
		t.Error("new blocker missing")
	}
	if !strings.Contains(md, "old blocker verbatim") {
		t.Error("previous blocker not carried")
	}
	if !strings.Contains(md, "old objective") {
		t.Error("previous objective not carried")
	}
}

func TestFoldTodosAndDecisions(t *testing.T) {
	s := newSession(t)
	addExchange(t, s, "c1", "question", `{"question":"ship it?"}`, "yes, ship it")
	addExchange(t, s, "c2", "todowrite", `{"todos":[{"content":"write tests","status":"pending"},{"content":"done thing","status":"completed"}]}`, "saved 2 todos")
	msgs, _ := s.Messages()
	changes, decisions, blockers := foldChanges(msgs)
	if len(decisions) != 1 || decisions[0] != "yes, ship it" {
		t.Errorf("decisions = %q", decisions)
	}
	if len(changes) != 1 || !strings.Contains(changes[0], "todowrite") {
		t.Errorf("changes = %q", changes)
	}
	if len(blockers) != 0 {
		t.Errorf("blockers = %q", blockers)
	}
	if got := foldTodos(msgs); len(got) != 1 || got[0] != "write tests" {
		t.Errorf("todos = %q, want the pending one", got)
	}
}

func TestCompactFoldsTailAndKeepsSessionAppendOnly(t *testing.T) {
	s := newSession(t)
	mustAppend(t, s, &store.Message{Role: "user", Content: "build a parser"})
	for i := 0; i < 40; i++ {
		addExchange(t, s, "c"+string(rune('a'+i%26))+string(rune('a'+i/26)), "write", `{"path":"f.go"}`, "wrote f.go")
	}
	mustAppend(t, s, &store.Message{Role: "user", Content: "now add tests"})

	before, _ := s.Messages()
	e := &Engine{Session: s, System: "sys"}
	if err := e.Compact(); err != nil {
		t.Fatal(err)
	}
	after, _ := s.Messages()

	if len(after) != len(before)+1 {
		t.Fatalf("transcript grew by %d lines, want exactly 1 (append-only)", len(after)-len(before))
	}
	ev := after[len(after)-1]
	if ev.Role != store.RoleCompaction {
		t.Fatalf("last role = %q, want %q", ev.Role, store.RoleCompaction)
	}
	if ev.Through == "" || ev.Through == ev.ID {
		t.Fatal("compaction event must carry Through = last folded message id")
	}
	// Through must point at a message inside the folded prefix, and the
	// newest user message must stay live.
	var throughIdx, lastUserIdx = -1, -1
	for i, m := range after {
		if m.ID == ev.Through {
			throughIdx = i
		}
		if m.Role == "user" {
			lastUserIdx = i
		}
	}
	if throughIdx < 0 {
		t.Fatal("Through id not found in transcript")
	}
	if lastUserIdx <= throughIdx {
		t.Errorf("newest user message (idx %d) must stay after Through (idx %d)", lastUserIdx, throughIdx)
	}
	if !strings.Contains(ev.Content, "build a parser") {
		t.Error("checkpoint objective should hold the first ask")
	}
	if !strings.Contains(ev.Content, "## Done") {
		t.Error("checkpoint missing Done section")
	}
}

func TestPayloadAfterCompaction(t *testing.T) {
	s := newSession(t)
	mustAppend(t, s, &store.Message{Role: "user", Content: "old ask"})
	addExchange(t, s, "c1", "write", `{"path":"old.go"}`, "wrote old.go")
	// Turn appends the new ask before auto-compaction runs, so the newest
	// user message is always the one that stays live.
	mustAppend(t, s, &store.Message{Role: "user", Content: "fresh ask"})
	e := &Engine{Session: s, System: "sys"}
	if err := e.Compact(); err != nil {
		t.Fatal(err)
	}

	msgs, _ := s.Messages()
	sys, pm := payload("sys", msgs)
	if !strings.Contains(sys, "sys\n\n## Objective") {
		t.Errorf("system = %q, want instructions + checkpoint", sys)
	}
	if len(pm) != 1 || pm[0].Content != "fresh ask" {
		t.Errorf("provider messages = %+v, want only post-checkpoint ones", pm)
	}
	if !strings.Contains(sys, "wrote old.go") {
		t.Error("folded tool spam should live in the checkpoint, not messages")
	}
	// Threshold math: checkpoint moves into the system side of the estimate.
	if payloadEstimate("sys", msgs) <= EstimateTokens("sys") {
		t.Error("estimate must account for the checkpoint")
	}
}

func TestAutoCompactsBeforeTurn(t *testing.T) {
	s := newSession(t)
	mustAppend(t, s, &store.Message{Role: "user", Content: "seed"})
	addExchange(t, s, "c1", "write", `{"path":"old.go"}`, "wrote old.go")

	fp := &fakeProvider{}
	e := &Engine{
		Session:      s,
		Provider:     fp,
		Model:        "mockt/t",
		System:       "sys",
		AutoCompact:  true,
		ContextLimit: 200, // threshold 200-4000 < 0 → always trips
	}
	if err := e.Turn(context.Background(), "next step"); err != nil {
		t.Fatal(err)
	}
	if len(fp.reqs) != 1 {
		t.Fatalf("provider calls = %d, want 1", len(fp.reqs))
	}
	if !strings.Contains(fp.reqs[0].System, "## Done") {
		t.Error("auto-compaction did not put the checkpoint into the system prompt")
	}
	// Folded exchange must not be replayed as messages.
	for _, m := range fp.reqs[0].Messages {
		if m.Content == "wrote old.go" {
			t.Error("folded tool result leaked into provider messages")
		}
	}
	if _, err := os.Stat(filepath.Join(s.Dir, "compaction", "archive.md")); err != nil {
		t.Errorf("archive.md missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.Dir, "checkpoint.md")); err != nil {
		t.Errorf("checkpoint.md missing: %v", err)
	}
}

func TestAutoCompactDisabledByConfig(t *testing.T) {
	s := newSession(t)
	mustAppend(t, s, &store.Message{Role: "user", Content: "seed"})
	fp := &fakeProvider{}
	e := &Engine{
		Session:      s,
		Provider:     fp,
		Model:        "mockt/t",
		System:       "sys",
		AutoCompact:  false,
		ContextLimit: 1, // would trip if enabled
	}
	if err := e.Turn(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s.Dir, "checkpoint.md")); err == nil {
		t.Error("checkpoint written although auto_compact = false")
	}
}

// TestCompactionAgentFallback proves §11 step 5: when the extractor
// throws, the prepared prompt goes to the compaction agent and its reply
// becomes the checkpoint — the round still folds exactly one
// append-only event and the extractor's archive never runs.
func TestCompactionAgentFallback(t *testing.T) {
	s := newSession(t)
	mustAppend(t, s, &store.Message{Role: "user", Content: "build a parser"})
	for i := 0; i < 40; i++ {
		addExchange(t, s, "f"+string(rune('a'+i%26))+string(rune('a'+i/26)), "write", `{"path":"f.go"}`, "wrote f.go")
	}
	mustAppend(t, s, &store.Message{Role: "user", Content: "now add tests"})
	before, _ := s.Messages()

	extractStep = func(string, []store.Message, checkpoint) int { panic("extractor exploded") }
	defer func() { extractStep = extract }()

	var prompt, emitted string
	e := &Engine{
		Session: s, System: "sys",
		Emit: func(ev Event) { emitted = ev.Text },
		CompactionLLM: func(_ context.Context, p string) (string, error) {
			prompt = p
			return "## Objective\nfrom the agent\n\n## Done\n- built parser\n", nil
		},
	}
	if err := e.Compact(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, "Folded transcript:") || !strings.Contains(prompt, "build a parser") {
		t.Errorf("prompt misses the folded transcript: %q", prompt)
	}
	body, _ := os.ReadFile(filepath.Join(s.Dir, "checkpoint.md"))
	if !strings.Contains(string(body), "from the agent") ||
		!strings.Contains(string(body), "## Done") {
		t.Errorf("checkpoint.md = %q, want the agent's sections", body)
	}
	if _, err := os.Stat(filepath.Join(s.Dir, "compaction", "archive.md")); err == nil {
		t.Error("the extractor panicked: the archive round must not run")
	}
	after, _ := s.Messages()
	if len(after) != len(before)+1 {
		t.Fatalf("transcript grew by %d lines, want exactly 1", len(after)-len(before))
	}
	ev := after[len(after)-1]
	if ev.Role != store.RoleCompaction || ev.Through == "" ||
		!strings.Contains(ev.Content, "from the agent") {
		t.Errorf("event = %+v, want a compaction event carrying the agent's checkpoint", ev)
	}
	if !strings.Contains(emitted, "compaction agent") {
		t.Errorf("emit = %q, want the compaction-agent label", emitted)
	}
}

// TestCompactionFallbackFailingAgent keeps the round honest: a hook that
// errors surfaces the original extraction failure and appends nothing.
func TestCompactionFallbackFailingAgent(t *testing.T) {
	s := newSession(t)
	mustAppend(t, s, &store.Message{Role: "user", Content: "build a parser"})
	for i := 0; i < 40; i++ {
		addExchange(t, s, "g"+string(rune('a'+i%26))+string(rune('a'+i/26)), "write", `{"path":"f.go"}`, "wrote f.go")
	}
	mustAppend(t, s, &store.Message{Role: "user", Content: "now add tests"})
	before, _ := s.Messages()

	extractStep = func(string, []store.Message, checkpoint) int { panic("extractor exploded") }
	defer func() { extractStep = extract }()

	e := &Engine{Session: s, System: "sys", CompactionLLM: func(context.Context, string) (string, error) {
		return "", fmt.Errorf("model unavailable")
	}}
	err := e.Compact()
	if err == nil || !strings.Contains(err.Error(), "compaction extraction failed") {
		t.Fatalf("err = %v, want the original extraction failure", err)
	}
	after, _ := s.Messages()
	if len(after) != len(before) {
		t.Errorf("failed round appended %d line(s), want none", len(after)-len(before))
	}

	// No hook at all: same contract (extraction error surfaces).
	e2 := &Engine{Session: s, System: "sys"}
	if err := e2.Compact(); err == nil || !strings.Contains(err.Error(), "compaction extraction failed") {
		t.Fatalf("nil-hook err = %v, want the extraction failure", err)
	}
}
