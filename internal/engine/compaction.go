// Compaction folds the transcript into a checkpoint without spending a model
// call: the engine reads messages.jsonl itself, keeps a tail of recent
// messages live, and rewrites only the context the model sees.
package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"matcode/internal/providers"
	"matcode/internal/store"
)

const (
	// KeepTail is the number of estimated tokens of newest messages kept live
	// after a compaction round (spec §11).
	KeepTail = 15000
	// CompactBuffer is the headroom reserved under the context limit for the
	// model's own reply; the auto trigger fires at ContextLimit-CompactBuffer.
	CompactBuffer = 4000

	maxChanges   = 80  // bullets per round folded into the checkpoint
	maxDecisions = 5   // question answers per round
	maxBlockers  = 10  // newest error facts kept in the checkpoint
	maxDone      = 60  // bullets under ## Done
	objectiveMax = 300 // runes for objective / next-move asks

	// compactionFallbackTimeout caps the §11 step-5 model call: a hung
	// provider must not stall the auto-compact path either.
	compactionFallbackTimeout = 60 * time.Second

	// foldMsgMax caps one folded message inside the fallback transcript.
	foldMsgMax = 2000
)

// extractStep is the extraction seam: tests swap it to exercise the
// compaction-agent fallback without provoking a real panic.
var extractStep = extract

// RenderFold flattens a folded round into the role-labelled transcript
// the compaction agent consumes (§11 step 5): previous checkpoint first
// when there is one, then user/assistant/tool lines with payloads capped
// so one round fits a single stateless completion.
func RenderFold(prevCheckpoint string, fold []store.Message) string {
	var b strings.Builder
	if prevCheckpoint != "" {
		b.WriteString("Previous checkpoint:\n" + strings.TrimSpace(prevCheckpoint) + "\n\n")
	}
	b.WriteString("Folded transcript:\n")
	for _, m := range fold {
		text := strings.TrimSpace(m.Content)
		if text == "" && len(m.ToolCalls) == 0 {
			continue
		}
		role := m.Role
		if role == "assistant" && len(m.ToolCalls) > 0 {
			role = "tool call " + m.ToolCalls[0].Name
		}
		if len(m.ToolCalls) > 0 && text == "" {
			text = "calling " + m.ToolCalls[0].Name + "(" + truncRunes(m.ToolCalls[0].Arguments, 200) + ")"
		}
		b.WriteString(role + ": " + truncRunes(text, foldMsgMax) + "\n")
	}
	return b.String()
}

// EstimateTokens approximates token count: ~4 bytes per token in English,
// rounded up, so the heuristic never under-reports badly.
func EstimateTokens(s string) int {
	if s == "" {
		return 0
	}
	return utf8.RuneCountInString(s)/4 + 1
}

// estimateMessage charges one stored message, including tool-call payloads.
func estimateMessage(m store.Message) int {
	n := EstimateTokens(m.Role) + EstimateTokens(m.Content) + EstimateTokens(m.ToolCallID)
	for _, c := range m.ToolCalls {
		n += EstimateTokens(c.Name) + EstimateTokens(c.Arguments)
	}
	return n
}

// splitAtCheckpoint returns the newest checkpoint body ("" when none) and the
// messages that follow it — the only transcript the model still sees. The
// split anchors on the event's Through id (last *folded* message), so the
// keep-tail that precedes the event in the append-only file stays live.
func splitAtCheckpoint(msgs []store.Message) (string, []store.Message) {
	cp := ""
	for _, m := range msgs {
		if m.Role == store.RoleCompaction {
			cp = m.Content
		}
	}
	if cp == "" {
		return "", msgs
	}
	start := 0
	for _, m := range msgs {
		if m.Role == store.RoleCompaction && m.Through != "" {
			for j, x := range msgs {
				if x.ID == m.Through {
					start = j + 1
				}
			}
		}
	}
	return cp, msgs[start:]
}

// payload is the context assembly: system instructions plus the last
// checkpoint, and only the transcript messages after it (spec §5).
func payload(sys string, msgs []store.Message) (string, []providers.Message) {
	cp, rest := splitAtCheckpoint(msgs)
	system := sys
	if cp != "" {
		system = sys + "\n\n" + strings.TrimSpace(cp)
	}
	return system, toProvider(rest)
}

// ContextPreview exposes the assembled payload for API consumers (GET
// /api/session/{id}/context): the system text with checkpoint folded in,
// only the live messages after it, and the estimated token count.
func ContextPreview(sys string, msgs []store.Message) (system string, live []store.Message, tokens int) {
	cp, rest := splitAtCheckpoint(msgs)
	system = sys
	if cp != "" {
		system = sys + "\n\n" + strings.TrimSpace(cp)
	}
	return system, rest, payloadEstimate(sys, msgs)
}

// payloadEstimate sizes exactly what payload() would send, for the auto
// compaction threshold.
func payloadEstimate(sys string, msgs []store.Message) int {
	cp, rest := splitAtCheckpoint(msgs)
	n := EstimateTokens(sys) + EstimateTokens(cp)
	for _, m := range rest {
		n += estimateMessage(m)
	}
	return n
}

// Compact folds everything up to the keep-tail boundary into a checkpoint and
// appends a compaction event; the transcript itself stays append-only. It is
// the deterministic path — no model call (spec §11).
func (e *Engine) Compact() error {
	if e.Session == nil {
		return fmt.Errorf("engine: compaction needs a session")
	}
	msgs, err := e.Session.Messages()
	if err != nil {
		return err
	}
	start := 0
	for i, m := range msgs {
		if m.Role == store.RoleCompaction {
			start = i + 1
		}
	}
	// Live messages resume after the folded-through id, not after the event:
	// the keep-tail precedes the event in an append-only transcript.
	if start > 0 {
		if ev := msgs[start-1]; ev.Through != "" {
			for j, x := range msgs {
				if x.ID == ev.Through {
					start = j + 1
				}
			}
		}
	}
	if start >= len(msgs) {
		return nil
	}

	// Walk from the end until the keep-tail budget is spent.
	boundary, acc := len(msgs), 0
	for i := len(msgs) - 1; i >= start; i-- {
		acc += estimateMessage(msgs[i])
		boundary = i
		if acc >= KeepTail {
			break
		}
	}
	lastUser := -1
	for i := len(msgs) - 1; i >= start; i-- {
		if msgs[i].Role == "user" {
			lastUser = i
			break
		}
	}
	if boundary <= start && lastUser >= 0 {
		// Under the keep-tail budget: a manual round (or a tiny configured
		// limit) still folds everything up to the newest ask.
		boundary = lastUser
	}
	if lastUser >= 0 && boundary > lastUser {
		// Never fold the newest user message.
		boundary = lastUser
	}
	if boundary <= start {
		return nil // nothing new to fold
	}
	var fold []store.Message
	for _, m := range msgs[start:boundary] {
		if m.Role != store.RoleCompaction {
			fold = append(fold, m)
		}
	}
	if len(fold) == 0 {
		return nil
	}

	// session.compaction (§8): a plugin that owns compaction writes the
	// checkpoint itself and the built-in extractor never runs. A failing
	// hook falls through to the deterministic extractor.
	if e.Plugins != nil {
		if cp, ok := e.Plugins.SessionCompaction(context.Background(), e.Session.Meta.ID, fold); ok {
			if err := os.WriteFile(filepath.Join(e.Session.Dir, "checkpoint.md"), []byte(cp), 0o644); err != nil {
				return err
			}
			return e.appendCompaction(msgs, boundary, len(fold), "plugin checkpoint")
		}
	}

	// If extraction ever panics, the session still runs on its previous
	// checkpoint: the compaction agent takes over (§11 step 5), and only
	// when it also fails does the round error out.
	round, err := safeExtract(e.Session.Dir, fold)
	if err != nil {
		if e.CompactionLLM == nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), compactionFallbackTimeout)
		prev := readFile(filepath.Join(e.Session.Dir, "checkpoint.md"))
		reply, ferr := e.CompactionLLM(ctx, RenderFold(prev, fold))
		cancel()
		if ferr != nil {
			return err // best effort: the extractor's error wins
		}
		cp := parseCheckpoint(reply)
		if cp.Objective == "" && len(cp.Done) == 0 && len(cp.NextMove) == 0 &&
			len(cp.Blockers) == 0 && cp.Archive == "" {
			return err // unusable reply — report what actually broke
		}
		if werr := os.WriteFile(filepath.Join(e.Session.Dir, "checkpoint.md"),
			[]byte(cp.render()), 0o644); werr != nil {
			return err
		}
		return e.appendCompaction(msgs, boundary, len(fold), "compaction agent")
	}
	_ = round

	return e.appendCompaction(msgs, boundary, len(fold), "extractor")
}

// appendCompaction records the compaction event and the emit, shared by the
// plugin-checkpoint path and the built-in extractor path.
func (e *Engine) appendCompaction(msgs []store.Message, boundary, n int, how string) error {
	ev := &store.Message{
		Role:    store.RoleCompaction,
		Content: readFile(filepath.Join(e.Session.Dir, "checkpoint.md")),
		Through: msgs[boundary-1].ID,
	}
	if err := e.Session.Append(ev); err != nil {
		return err
	}
	if e.Emit != nil {
		e.Emit(Event{Type: EventCompact, Text: fmt.Sprintf(
			"folded %d message(s) through %s (%s)", n, ev.Through, how)})
	}
	return nil
}

// checkpoint is the parsed/rendered shape of checkpoint.md (spec §11 step 4).
type checkpoint struct {
	Objective string
	Done      []string
	NextMove  []string
	Blockers  []string
	Archive   string
}

func parseCheckpoint(md string) checkpoint {
	return checkpoint{
		Objective: strings.TrimSpace(section(md, "Objective")),
		Done:      bullets(section(md, "Done")),
		NextMove:  bullets(section(md, "Next Move")),
		Blockers:  bullets(section(md, "Blockers/Facts")),
		Archive:   strings.TrimSpace(section(md, "Archive")),
	}
}

func (c checkpoint) render() string {
	var b strings.Builder
	b.WriteString("## Objective\n")
	b.WriteString(c.Objective + "\n\n")
	b.WriteString("## Done\n")
	for _, d := range c.Done {
		b.WriteString("- " + d + "\n")
	}
	b.WriteString("\n## Next Move\n")
	for _, n := range c.NextMove {
		b.WriteString("- " + n + "\n")
	}
	b.WriteString("\n## Blockers/Facts\n")
	if len(c.Blockers) == 0 {
		b.WriteString("(none)\n")
	}
	for _, x := range c.Blockers {
		b.WriteString(bullet(x) + "\n")
	}
	b.WriteString("\n## Archive\n")
	b.WriteString(c.Archive + "\n")
	return b.String()
}

// safeExtract runs extract with a panic guard: step 2 never takes the session
// down with it (the compaction.md agent file is the fallback path, spec §11
// step 5 — not reached while extraction succeeds).
func safeExtract(dir string, fold []store.Message) (round int, err error) {
	prev := parseCheckpoint(readFile(filepath.Join(dir, "checkpoint.md")))
	defer func() {
		if r := recover(); r != nil {
			round, err = 0, fmt.Errorf("engine: compaction extraction failed: %v", r)
		}
	}()
	return extractStep(dir, fold, prev), nil
}

// extract turns one folded round into archive.md + checkpoint.md and returns
// the round number (spec §11 steps 2-4).
func extract(dir string, fold []store.Message, prev checkpoint) int {
	objective := ""
	for _, m := range fold {
		if m.Role == "user" && strings.TrimSpace(m.Content) != "" {
			objective = truncRunes(oneLine(m.Content), objectiveMax)
			break
		}
	}
	if objective == "" {
		objective = prev.Objective
	}

	changes, decisions, blockers := foldChanges(fold)
	nextMove := foldTodos(fold)
	if len(nextMove) == 0 {
		nextMove = prev.NextMove
	}
	if len(nextMove) == 0 {
		for i := len(fold) - 1; i >= 0; i-- {
			if fold[i].Role == "user" && strings.TrimSpace(fold[i].Content) != "" {
				nextMove = []string{truncRunes(oneLine(fold[i].Content), objectiveMax)}
				break
			}
		}
	}

	// Blockers: previous facts verbatim plus this round's errors, newest kept.
	blockers = dedupe(append(prev.Blockers, blockers...))
	if len(blockers) > maxBlockers {
		blockers = blockers[len(blockers)-maxBlockers:]
	}

	done := append(append([]string{}, prev.Done...), changes...)
	if len(done) > maxDone {
		done = done[len(done)-maxDone:]
	}

	// archive.md grows forever: index line under ## Overall + this round.
	archPath := filepath.Join(dir, "compaction", "archive.md")
	old := readFile(archPath)
	round := strings.Count(old, "### Round ") + 1
	body := fmt.Sprintf("### Round %d\n\nObjective: %s\n\nChanges:\n%s\n\nDecisions:\n%s\n",
		round, objective, bulletList(changes), bulletList(decisions))
	next := insertArchive(old, fmt.Sprintf("- Round %d: %s", round, objective), body)
	if err := os.MkdirAll(filepath.Dir(archPath), 0o755); err == nil {
		_ = os.WriteFile(archPath, []byte(next), 0o644)
	}

	cp := checkpoint{
		Objective: objective,
		Done:      done,
		NextMove:  nextMove,
		Blockers:  blockers,
		Archive:   fmt.Sprintf("compaction/archive.md (round %d)", round),
	}
	_ = os.WriteFile(filepath.Join(dir, "checkpoint.md"), []byte(cp.render()), 0o644)
	return round
}

// foldChanges walks a round extracting every tool call paired with its result.
func foldChanges(fold []store.Message) (changes, decisions, blockers []string) {
	results := map[string]string{}
	for _, m := range fold {
		if m.Role == "tool" {
			results[m.ToolCallID] = m.Content
		}
	}
	var raw []string
	for _, m := range fold {
		if m.Role != "assistant" {
			continue
		}
		for _, c := range m.ToolCalls {
			res, ok := results[c.ID]
			if !ok {
				res = "(result not recorded)"
			}
			if c.Name == "question" {
				decisions = append(decisions, oneLine(res))
				continue
			}
			if isErrorResult(res) {
				blockers = append(blockers, strings.TrimSpace(res))
			}
			raw = append(raw, formatChange(c, res))
		}
	}
	return collapseDups(raw), capN(decisions, maxDecisions), blockers
}

// formatChange renders one call/result pair: name(hint) → (ERROR?) summary.
func formatChange(c store.ToolCall, res string) string {
	label := c.Name
	if hint := argHint(c.Arguments); hint != "" {
		label += "(" + hint + ")"
	}
	marker := ""
	if isErrorResult(res) {
		marker = "(ERROR) "
	}
	return label + " → " + marker + oneLine(res)
}

// foldTodos reads the newest todowrite call's unfinished items.
func foldTodos(fold []store.Message) []string {
	var out []string
	for i := len(fold) - 1; i >= 0 && len(out) == 0; i-- {
		if fold[i].Role != "assistant" {
			continue
		}
		for _, c := range fold[i].ToolCalls {
			if c.Name == "todowrite" {
				out = parseTodos(c.Arguments)
			}
		}
	}
	return out
}

func parseTodos(args string) []string {
	var p struct {
		Todos []struct {
			Content string `json:"content"`
			Status  string `json:"status"`
		} `json:"todos"`
	}
	if json.Unmarshal([]byte(store.RawJSON(args)), &p) != nil {
		return nil
	}
	var out []string
	for _, t := range p.Todos {
		if t.Status == "pending" || t.Status == "in_progress" || t.Status == "" {
			if c := strings.TrimSpace(t.Content); c != "" {
				out = append(out, c)
			}
		}
	}
	return out
}

// insertArchive adds the index line right under ## Overall and appends body.
func insertArchive(old, indexLine, body string) string {
	if old == "" {
		return "## Overall\n" + indexLine + "\n\n" + body
	}
	if i := strings.Index(old, "## Overall\n"); i >= 0 {
		at := i + len("## Overall\n")
		out := old[:at] + indexLine + "\n" + old[at:]
		if !strings.HasSuffix(out, "\n") {
			out += "\n"
		}
		return out + "\n" + body
	}
	if !strings.HasSuffix(old, "\n") {
		old += "\n"
	}
	return old + "\n" + body
}

// argHint picks one readable argument for the change line.
func argHint(args string) string {
	var m map[string]any
	if json.Unmarshal([]byte(store.RawJSON(args)), &m) == nil {
		for _, k := range []string{"path", "file_path", "command", "pattern", "query", "url", "file"} {
			if v, ok := m[k].(string); ok && v != "" {
				return truncRunes(v, 48)
			}
		}
	}
	return truncRunes(strings.TrimSpace(args), 48)
}

// isErrorResult marks tool results the session should treat as failures.
func isErrorResult(res string) bool {
	t := strings.ToLower(strings.TrimSpace(res))
	for _, p := range []string{"error", "denied", "unknown tool", "needs approval", "tool ", "failed", "cannot", "no such"} {
		if strings.HasPrefix(t, p) {
			return true
		}
	}
	return false
}

// collapseDups marks consecutive identical change lines with ×N.
func collapseDups(raw []string) []string {
	var lines []string
	var counts []int
	for _, l := range raw {
		if n := len(lines); n > 0 && lines[n-1] == l {
			counts[n-1]++
			continue
		}
		lines = append(lines, l)
		counts = append(counts, 1)
	}
	out := make([]string, 0, len(lines))
	for i, l := range lines {
		if counts[i] > 1 {
			l = fmt.Sprintf("%s ×%d", l, counts[i])
		}
		out = append(out, l)
		if len(out) >= maxChanges {
			break
		}
	}
	return out
}

// --- small text helpers -----------------------------------------------------

func readFile(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

// section returns the lines of "## title" until the next "## " heading.
func section(md, title string) string {
	var out []string
	in := false
	for _, l := range strings.Split(md, "\n") {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "## ") {
			in = strings.EqualFold(strings.TrimSpace(strings.TrimPrefix(t, "## ")), title)
			continue
		}
		if in {
			out = append(out, l)
		}
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func bullets(md string) []string {
	var out []string
	for _, l := range strings.Split(md, "\n") {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "- ") {
			if v := strings.TrimSpace(strings.TrimPrefix(t, "- ")); v != "" && v != "(none)" {
				out = append(out, v)
			}
		}
	}
	return out
}

func bullet(s string) string {
	lines := strings.Split(s, "\n")
	out := "- " + strings.TrimSpace(lines[0])
	for _, l := range lines[1:] {
		out += "\n  " + strings.TrimRight(l, " \t")
	}
	return out
}

func bulletList(items []string) string {
	if len(items) == 0 {
		return "- (none)"
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, "- "+it)
	}
	return strings.Join(out, "\n")
}

func oneLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\n\r"); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	return truncRunes(s, 120)
}

func truncRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return "…"
	}
	return string(r[:n-1]) + "…"
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func capN(in []string, n int) []string {
	if len(in) <= n {
		return in
	}
	return in[:n]
}
