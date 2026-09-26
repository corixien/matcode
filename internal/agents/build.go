package agents

// build is the default agent: full tools, no permission friction. It never
// asks for or denies tool access — permissions.toml is bypassed entirely.
var build = Agent{
	ID: "build",
	System: "Build mode: carry out the request directly — write, edit, patch, and run " +
		"commands without asking for approval; permissions are bypassed in this mode. " +
		"Keep changes minimal and focused on the task, verify your work, and end with a " +
		"short summary of what changed.",
	ToolIDs:           []string{"*"},
	UseInstructions:   true,
	BypassPermissions: true,
}
