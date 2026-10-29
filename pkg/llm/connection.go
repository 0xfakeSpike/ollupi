package llm

import (
	"context"
	"errors"
	"fmt"
	"maps"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/openai/openai-go"
	"google.golang.org/genai"
)

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

// connection holds what every provider client shares: the declared model limits and the concurrent call slots.
type connection struct {
	limits map[string]Limits
	calls  chan struct{}
}

func newConnection(calls int, limits map[string]Limits) connection {
	if calls <= 0 {
		calls = DefaultCalls
	}
	return connection{limits: maps.Clone(limits), calls: make(chan struct{}, calls)}
}

func (c connection) Limits(model string) (Limits, error) {
	l, ok := c.limits[model]
	if !ok {
		return Limits{}, fmt.Errorf("llm: the context limits of model %q are not declared; declare them on its connection", model)
	}
	if err := l.check(); err != nil {
		return Limits{}, fmt.Errorf("llm: limits of model %q: %w", model, err)
	}
	return l, nil
}

// acquire takes a call slot and bounds the call with the default timeout unless ctx has a deadline;
// the returned function frees both.
func (c connection) acquire(ctx context.Context) (context.Context, func(), error) {
	select {
	case c.calls <- struct{}{}:
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
	ctx, cancel := withDefaultTimeout(ctx, defaultChatTimeout)
	return ctx, func() { cancel(); <-c.calls }, nil
}

// safeRequestError reports a failed model request without the provider's response body, which can echo the prompt.
func safeRequestError(err error) error {
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	var openaiErr *openai.Error
	var anthropicErr *anthropic.Error
	var geminiErr genai.APIError
	status := 0
	switch {
	case errors.As(err, &openaiErr):
		status = openaiErr.StatusCode
	case errors.As(err, &anthropicErr):
		status = anthropicErr.StatusCode
	case errors.As(err, &geminiErr):
		status = geminiErr.Code
	}
	if status != 0 {
		return fmt.Errorf("model request failed (HTTP %d); check the selected connection and model", status)
	}
	return fmt.Errorf("model request failed; check the selected connection and try again")
}
