package llm

import "fmt"

type Limits struct {
	Context int `json:"context"`
	Output  int `json:"output"`
}

func (l Limits) Input() int { return l.Context - l.Output }

func (l Limits) check() error {
	if l.Context <= 0 || l.Output <= 0 || l.Output >= l.Context {
		return fmt.Errorf("context %d and output %d must be positive, with output below context", l.Context, l.Output)
	}
	return nil
}

func (c *OpenAI) Limits(model string) (Limits, error) {
	l, ok := c.limits[model]
	if !ok {
		return Limits{}, fmt.Errorf("llm: the context limits of model %q are not declared; declare them on its connection", model)
	}
	if err := l.check(); err != nil {
		return Limits{}, fmt.Errorf("llm: limits of model %q: %w", model, err)
	}
	return l, nil
}
