package agents

// title names a transcript in one stateless call — no tools, no session.
var title = Agent{
	ID: "title",
	System: "Output a title for the supplied text: 3-6 words, no quotes, no trailing " +
		"period, no explanation — only the title itself.",
	Stateless: true,
}
