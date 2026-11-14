// Package profile holds the kinds of main agent: how the system prompt reads and which tools go out with every
// request, for the model a turn runs on.
package profile

// A Profile is one kind of main agent.
type Profile struct {
	// Rules are the working rules of the system prompt, after the agent's own prompt.
	Rules string
	// Skills lists the skills in the system prompt.
	Skills bool
	// Core are the only tools sent with every request; the others are deferred, for the model to load. Nil sends every
	// tool, deferring external ones only when they do not fit.
	Core []string
}

// compactBelow is the input the full prompt and tool list need: about 8k tokens, which the context window holds to half
// of it.
const compactBelow = 16_384

// For is the profile of a model that takes inputTokens of input.
func For(inputTokens int) Profile {
	if inputTokens < compactBelow {
		return Compact
	}
	return Full
}
