package agents

// summary condenses a transcript in one stateless call — no tools, no session.
var summary = Agent{
	ID: "summary",
	System: "Summarize the supplied text — usually a session transcript. Cover: the goal, " +
		"what was done, key decisions and constraints, current state, and open items. " +
		"Output only the summary in plain prose or a tight bullet list, with no preamble.",
	Stateless: true,
}
