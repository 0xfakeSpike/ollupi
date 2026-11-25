package workspace

import "github.com/0xfakeSpike/ollupi/pkg/sandbox"

// Describe tells the model where a conversation's commands and files are:
// the environment named name, whose box is box. It is the ENVIRONMENT of
// the system prompt, which the tools' descriptions point to, since an
// environment is the conversation's and the tools are every one's.
func Describe(name string, box sandbox.Box) string {
	text := "Commands and file edits run in " + name + ". Outside a project, work_dir is " + box.Roots()[0] + ". " + box.Summary() + "."
	if box.Enforced() {
		text += " Commands run within the sandbox's limits without asking the user."
	}
	if !box.CanEscalate() {
		return text + " Leave sandbox_permissions out: this sandbox cannot give a command more access."
	}
	return text + " A result's sandbox_denials lists what the sandbox's limits stopped. " +
		"When a command fails because of the sandbox's limits (a write outside the command directories or to a protected file, " +
		"a host outside the allowed domains, the network, a local socket), " +
		"retry it with sandbox_permissions \"require_escalated\" and a one-sentence justification: " +
		"the command then runs with full access, without those limits, and the user is asked first unless the approval policy never asks. " +
		"Do not escalate to get around a denied path on purpose, and do not escalate commands that work within the limits."
}
