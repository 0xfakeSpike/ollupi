package llm

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/openai/openai-go"
	"google.golang.org/genai"
)

const (
	DefaultCalls = 4
	// retries back off from half a second to eight, or as Retry-After asks: about 40 s in all, so a provider short of
	// quota for a few seconds slows a turn instead of failing it. The call keeps its slot while it waits.
	retries = 8
)

var httpClient = &http.Client{Transport: idleTransport{base: http.DefaultTransport, idle: idleTimeout}}

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

func (c connection) acquire(ctx context.Context) (func(), error) {
	select {
	case c.calls <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return func() { <-c.calls }, nil
}

// safeRequestError reports a failed model request without the provider's response body, which can echo the prompt.
func safeRequestError(err error) error {
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	if errors.Is(err, errIdle) {
		return errIdle
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
