package llm

import (
	"fmt"
)

type Limits struct {
	Context  int `json:"context"`
	Output   int `json:"output"`
	MaxInput int `json:"input,omitempty"`
}

func (l Limits) reserve() int { return min(l.Output, l.Context/4) }

func (l Limits) Input() int {
	room := l.Context - l.reserve()
	if l.MaxInput > 0 {
		return min(room, l.MaxInput)
	}
	return room
}

func (l Limits) Answer(input int) int {
	return min(l.Output, max(l.reserve(), l.Context-input))
}

func (l Limits) Check() error {
	if l.Context <= 0 || l.Output <= 0 {
		return fmt.Errorf("context %d and output %d must be positive", l.Context, l.Output)
	}
	if l.MaxInput < 0 {
		return fmt.Errorf("input %d must not be negative", l.MaxInput)
	}
	return nil
}
