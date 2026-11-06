package llm

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/openai/openai-go"
	"google.golang.org/genai"
)

const (
	defaultChatTimeout = 180 * time.Second
	DefaultCalls       = 4
	// retries is how many times a request refused with 408, 429 or a 5xx, or cut off before its answer, is sent
	// again, waiting from half a second, doubling to at most eight (or, with OpenAI and Anthropic, as long as the
	// provider's Retry-After asks): about 40 s in all, within defaultChatTimeout. A provider short of quota for a few seconds then slows a turn
	// instead of failing it. The call keeps its slot while it waits, so the calls in flight stay within the limit.
	retries = 8
)

type Limits struct {
	Context int `json:"context"`
	Output  int `json:"output"`
	// MaxInput is the most input the model accepts, for models that cap it below the window. Zero means no cap of its own.
	MaxInput int `json:"input,omitempty"`
}

// Input is the room a request has: what the window leaves beside the answer, within the model's own cap.
func (l Limits) Input() int {
	room := l.Context - l.Output
	if l.MaxInput > 0 {
		return min(room, l.MaxInput)
	}
	return room
}

func (l Limits) check() error {
	if l.Context <= 0 || l.Output <= 0 || l.Output >= l.Context {
		return fmt.Errorf("context %d and output %d must be positive, with output below context", l.Context, l.Output)
	}
	if l.MaxInput < 0 {
		return fmt.Errorf("input %d must not be negative", l.MaxInput)
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
	cancel := context.CancelFunc(func() {})
	if _, ok := ctx.Deadline(); !ok {
		ctx, cancel = context.WithTimeout(ctx, defaultChatTimeout)
	}
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
