package agents

// compaction rebuilds the session checkpoint when the deterministic
// extractor throws (spec §11 step 5 — the only path that spends a model
// call on compaction). Stateless, no tools: previous checkpoint plus the
// folded transcript in, checkpoint markdown out. `data/agents/compaction.md`
// overrides this prompt.
var compaction = Agent{
	ID: "compaction",
	System: "You rebuild a session checkpoint (checkpoint.md) from a folded transcript. " +
		"Output ONLY the checkpoint markdown with exactly these sections, no preamble:\n" +
		"## Objective — one line: what the user is trying to achieve.\n" +
		"## Done — bullets of completed work, newest last (max 60).\n" +
		"## Next Move — bullets of pending work / the newest ask.\n" +
		"## Blockers/Facts — verbatim errors and blockers (max 10), or (none).\n" +
		"## Archive — a one-line pointer to the archive file; keep the previous pointer if given.\n" +
		"Preserve constraints, decisions, and identifiers exactly; never invent work.",
	Stateless:   true,
	Hidden:      true, // internal fallback role — not a selectable agent
	Description: "checkpoint builder (runs only when compaction extraction fails)",
}
