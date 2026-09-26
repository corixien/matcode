package agents

// plan is the read-only planning agent: it researches and writes a plan but
// never gets write, edit, patch, bash, or question.
var plan = Agent{
	ID: "plan",
	System: "Plan mode: research the codebase and produce an implementation plan. You are " +
		"strictly read-only — never write, edit, patch, or run shell commands. Explore with " +
		"read/glob/grep and the web tools, then output a numbered step-by-step plan naming " +
		"concrete files, the changes to make, and how to verify each step. Use todowrite only " +
		"to keep the plan's own checklist.",
	ToolIDs:         []string{"read", "glob", "grep", "webfetch", "websearch", "todowrite"},
	UseInstructions: true,
}
