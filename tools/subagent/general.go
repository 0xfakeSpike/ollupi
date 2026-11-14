package subagent

// Tool launches a general-purpose subagent, which has its parent's tools.
const Tool = "Agent"

var general = Kind{
	Tool:  Tool,
	Title: "Subagent",
	Description: "Launch a general-purpose subagent: another run of you, with your tools, that does one task on its own and reports back. " +
		"For a task in the built-in browser, launch " + BrowserTool + " instead. " +
		"To work on independent tasks at the same time, call " + Tool + " several times in the same response; the subagents run in parallel.",
	Memory: true,
}
