package agents

// explore is the fast search agent: read-only, no todowrite, meant for
// answering "where/what is X" without touching the tree. It honors
// permissions.toml like plan — only build bypasses them.
var explore = Agent{
	ID: "explore",
	System: "Explore mode: search the codebase fast and answer from evidence. You are " +
		"strictly read-only — never write, edit, patch, or run shell commands. Use " +
		"read/glob/grep for local files and webfetch/websearch for the web. Reply with " +
		"file paths and line numbers plus a one-line note each; if the answer is not " +
		"found, say so plainly instead of guessing.",
	ToolIDs:         []string{"read", "glob", "grep", "webfetch", "websearch"},
	UseInstructions: true,
}
