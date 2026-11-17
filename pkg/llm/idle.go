package llm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Calls stream their answers, so a long answer keeps sending; one that stalls is cut off here rather than held in
// its slot for as long as the turn may run.
const idleTimeout = 3 * time.Minute

var errIdle = fmt.Errorf("llm: the provider sent nothing for %s", idleTimeout)

type idleTransport struct {
	base http.RoundTripper
	idle time.Duration
}

func (t idleTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	ctx, cancel := context.WithCancelCause(r.Context())
	timer := time.AfterFunc(t.idle, func() { cancel(errIdle) })
	res, err := t.base.RoundTrip(r.WithContext(ctx))
	if err != nil {
		timer.Stop()
		if context.Cause(ctx) == errIdle {
			err = errIdle
		}
		cancel(nil)
		return nil, err
	}
	timer.Reset(t.idle)
	res.Body = &idleBody{ReadCloser: res.Body, ctx: ctx, cancel: cancel, timer: timer, idle: t.idle}
	return res, nil
}

type idleBody struct {
	io.ReadCloser
	ctx    context.Context
	cancel context.CancelCauseFunc
	timer  *time.Timer
	idle   time.Duration
}

func (b *idleBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.timer.Reset(b.idle)
	}
	if err != nil && !errors.Is(err, io.EOF) && context.Cause(b.ctx) == errIdle {
		err = errIdle
	}
	return n, err
}

func (b *idleBody) Close() error {
	b.timer.Stop()
	b.cancel(nil)
	return b.ReadCloser.Close()
}
