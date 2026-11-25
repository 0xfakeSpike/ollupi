package sandbox

import "context"

func (s *Sandbox) Terminal(context.Context, TerminalSpec) (Terminal, error) {
	return nil, errNoTerminal
}
