// Package wshub keeps a process's WebSocket connections, whatever they carry: it admits new ones at a bounded rate,
// holds a few for each key and gives the oldest up for a newer one, closes those whose peer stops answering pings or
// that reach their age, and closes all of them when the process stops.
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
	PerKey int
	// Rate and Burst bound how fast connections are admitted, so that clients reconnecting together after a restart
	// reach the work each new connection sets off a few at a time.
	Rate  rate.Limit
	Burst int
	// RetryWithin is the longest Retry-After a refused client is told; each is told a random one up to it, so that
	// those refused together do not come back together.
	RetryWithin time.Duration
	// PingEvery should be shorter than the shortest idle timeout on the way to the client, such as a load balancer's.
	PingEvery  time.Duration
	PongWithin time.Duration
	// MaxAge should be shorter than the longest a load balancer on the way keeps a WebSocket open. Each connection closes
	// at a random point in its last quarter, so that connections opened together are not all cut together later.
	MaxAge time.Duration
}

// StatusReplaced is in the range RFC 6455 leaves to applications, so that a client can tell it from the codes that
// ask it to reconnect.
const StatusReplaced websocket.StatusCode = 4000

type Hub[T any] struct {
	opts  Options
	admit *rate.Limiter

	mu sync.Mutex
	// held is never changed in place, so a slice read from it can be used after the lock is let go.
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

// When Admit returns false, w has already been answered 503 with a Retry-After.
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

// serve should return once reading ws fails, as it does when the hub closes ws: given up for a newer connection, its
// peer stopped answering pings, or it reached its age.
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

// Each visits key's connections oldest first.
func (h *Hub[T]) Each(key string, fn func(T)) {
	h.mu.Lock()
	held := h.held[key]
	h.mu.Unlock()
	for _, e := range held {
		fn(e.value)
	}
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

// keepAlive drops a connection that misses a pong without a closing handshake, which a peer that does not answer
// would not finish.
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
