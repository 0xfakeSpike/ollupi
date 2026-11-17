package wshub

import (
	"context"
	"errors"
	"math/rand/v2"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/time/rate"
)

type Options struct {
	PerKey      int
	Rate        rate.Limit
	Burst       int
	RetryWithin time.Duration
	PingEvery   time.Duration
	PongWithin  time.Duration
	MaxAge      time.Duration
}

const StatusReplaced websocket.StatusCode = 4000

type Hub[T any] struct {
	opts  Options
	admit *rate.Limiter

	mu   sync.Mutex
	held map[string][]*entry[T]
}

type entry[T any] struct {
	value T
	ws    *websocket.Conn
}

func New[T any](opts Options) (*Hub[T], error) {
	if opts.PerKey <= 0 || opts.Rate <= 0 || opts.Burst <= 0 || opts.RetryWithin < time.Second || opts.PingEvery <= 0 || opts.PongWithin <= 0 || opts.MaxAge <= 0 {
		return nil, errors.New("wshub: PerKey, Rate, Burst, PingEvery, PongWithin and MaxAge must be positive and RetryWithin at least a second")
	}
	return &Hub[T]{opts: opts, admit: rate.NewLimiter(opts.Rate, opts.Burst), held: map[string][]*entry[T]{}}, nil
}

func (h *Hub[T]) Admit(w http.ResponseWriter) bool {
	if h.admit.Allow() {
		return true
	}
	h.Refuse(w, "too many connections are starting; try again")
	return false
}

func (h *Hub[T]) Refuse(w http.ResponseWriter, why string) {
	seconds := 1 + rand.IntN(int(h.opts.RetryWithin/time.Second))
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
	http.Error(w, why, http.StatusServiceUnavailable)
}

func (h *Hub[T]) Serve(ctx context.Context, key string, ws *websocket.Conn, value T, serve func(context.Context) error) {
	e := &entry[T]{value: value, ws: ws}
	h.add(key, e)
	defer h.remove(key, e)

	age := h.opts.MaxAge
	if quarter := age / 4; quarter > 0 {
		age -= rand.N(quarter)
	}
	aged := time.AfterFunc(age, func() { ws.Close(websocket.StatusGoingAway, "connection reached its age; reconnect") })
	defer aged.Stop()
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(ctx, func() { ws.Close(websocket.StatusGoingAway, "server shutting down") })
	var alive sync.WaitGroup
	alive.Go(func() { h.keepAlive(ctx, key, ws) })

	err := serve(ctx)
	stopping := ctx.Err() != nil
	stop()
	cancel()
	alive.Wait()
	switch status := websocket.CloseStatus(err); {
	case status == websocket.StatusNormalClosure || status == websocket.StatusGoingAway || status == StatusReplaced || stopping:
		ws.Close(websocket.StatusNormalClosure, "")
	default:
		ws.Close(websocket.StatusInternalError, "connection error")
	}
}

func (h *Hub[T]) Count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, held := range h.held {
		n += len(held)
	}
	return n
}

func (h *Hub[T]) add(key string, e *entry[T]) {
	h.mu.Lock()
	held := append(slices.Clone(h.held[key]), e)
	var oldest *entry[T]
	if len(held) > h.opts.PerKey {
		oldest, held = held[0], held[1:]
	}
	h.held[key] = held
	h.mu.Unlock()
	if oldest != nil {
		go oldest.ws.Close(StatusReplaced, "replaced by a newer connection")
	}
}

func (h *Hub[T]) remove(key string, e *entry[T]) {
	h.mu.Lock()
	defer h.mu.Unlock()
	held := slices.DeleteFunc(slices.Clone(h.held[key]), func(x *entry[T]) bool { return x == e })
	if len(held) == 0 {
		delete(h.held, key)
		return
	}
	h.held[key] = held
}

func (h *Hub[T]) keepAlive(ctx context.Context, key string, ws *websocket.Conn) {
	tick := time.NewTicker(h.opts.PingEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		pingCtx, cancel := context.WithTimeout(ctx, h.opts.PongWithin)
		err := ws.Ping(pingCtx)
		cancel()
		if err != nil {
			if ctx.Err() == nil {
				ws.CloseNow()
			}
			return
		}
	}
}
