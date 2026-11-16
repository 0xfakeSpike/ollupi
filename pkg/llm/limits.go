package llm

import (
	"fmt"
)

type Limits struct {
	Context int `json:"context"`
	Output  int `json:"output"`
	// MaxInput is the most input the model accepts, for models that cap it below the window. Zero means no cap of its own.
	MaxInput int `json:"input,omitempty"`
}

// reserve is the part of the window kept for the answer. Output is the most an answer may take, which few reach, so a
// model that allows a long one gives up no more than a quarter of its window for it.
func (l Limits) reserve() int { return min(l.Output, l.Context/4) }

// Input is the room a request has: what the window leaves beside the answer's reserve, within the model's own cap.
func (l Limits) Input() int {
	room := l.Context - l.reserve()
	if l.MaxInput > 0 {
		return min(room, l.MaxInput)
	}
	return room
}

// Answer is the most an answer may take beside input tokens of input. The reserve stands however large input is, as
// input is an estimate.
func (l Limits) Answer(input int) int {
	return min(l.Output, max(l.reserve(), l.Context-input))
}

func (l Limits) check() error {
	if l.Context <= 0 || l.Output <= 0 {
		return fmt.Errorf("context %d and output %d must be positive", l.Context, l.Output)
	}
	if l.MaxInput < 0 {
		return fmt.Errorf("input %d must not be negative", l.MaxInput)
	}
	return nil
}
