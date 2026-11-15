package role

import (
	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/tools/cron"
	"github.com/0xfakeSpike/ollupi/tools/subagent"
)

// GeneralSub is the general-purpose subagent, which has its parent's tools; the tool of its name launches it.
const GeneralSub Name = subagent.General

var generalSub = Role{
	Name: GeneralSub, Persona: true, Rules: generalSubRules, Memory: true,
	send: sendFitting,
	// A subagent cannot launch another or schedule tasks, which would come back to its thread after it reported.
	Permission: permission.Layer{Of: "the general subagent's role", Without: []string{subagent.ProviderName, cron.ProviderName}},
}

// generalSubRules are the full main agent's, with what a subagent's answer is for.
const generalSubRules = fullMainRules + `

# You are a subagent
Another agent launched you to do one task. Your final answer is your report to that agent, not a message to the user: make it complete on its own, with what the task asked for.`
