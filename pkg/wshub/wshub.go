// Package wshub keeps a process's WebSocket connections, whatever they carry: it admits new ones at a bounded rate,
// holds a few for each key and gives the oldest up for a newer one, closes those whose peer stops answering pings or
// that reach their age, and closes all of them when the process stops.
package wshub

import (
	"context"
	"errors"
	"log/slog"
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
	// PerKey is how many connections one key may hold; a newer one closes the oldest.
	PerKey int
	// Rate and Burst bound how fast connections are admitted, so that clients reconnecting together after a restart
	// reach the work each new connection sets off a few at a time.
	Rate  rate.Limit
	Burst int
	// RetryWithin is the longest Retry-After a refused client is told; each is told a random one up to it, so that
	// those refused together do not come back together.
	RetryWithin time.Duration
	// PingEvery should be shorter than the shortest idle timeout on the way to the client, such as a load balancer's.
	PingEvery time.Duration
	// PongWithin is how long a peer may take to answer a ping before its connection is closed.
	PongWithin time.Duration
	// MaxAge should be shorter than the longest a load balancer on the way keeps a WebSocket open. Each connection is
	// closed with StatusGoingAway at a random point in the last quarter of it, so that connections opened together, as
	// after a restart, are not all cut together by that load balancer later.
	MaxAge time.Duration
}

// StatusReplaced is the close code of a connection given up for a newer one of its key. It is in the range RFC 6455
// leaves to applications, so that a client can tell it from the codes that ask it to reconnect.
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

// Admit says whether a new connection may start now. When it may not, w has been answered 503 with a Retry-After.
func (h *Hub[T]) Admit(w http.ResponseWriter) bool {
	if h.admit.Allow() {
		return true
	}
	h.Refuse(w, "too many connections are starting; try again")
	return false
}

// Refuse answers w 503 with a Retry-After, for a connection that may start later but not now.
func (h *Hub[T]) Refuse(w http.ResponseWriter, why string) {
	seconds := 1 + rand.IntN(int(h.opts.RetryWithin/time.Second))
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
	http.Error(w, why, http.StatusServiceUnavailable)
}

// Serve holds ws as one of key's connections, carrying value, while serve runs on it, and closes it when serve
// returns. serve's context ends when ctx does; serve should return once reading ws fails, as it does when the hub
// closes ws: given up for a newer connection, its peer stopped answering pings, or it reached its age.
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
	alive.Go(func() { h.keepAlive(ctx, ws) })

	err := serve(ctx)
	stopping := ctx.Err() != nil
	stop()
	cancel()
	alive.Wait()
	switch status := websocket.CloseStatus(err); {
	case status == websocket.StatusNormalClosure || status == websocket.StatusGoingAway || status == StatusReplaced || stopping:
		slog.Info("client disconnected")
		ws.Close(websocket.StatusNormalClosure, "")
	default:
		slog.Warn("client connection failed", "err", err)
		ws.Close(websocket.StatusInternalError, "connection error")
	}
}

// Count is how many connections the hub holds.
func (h *Hub[T]) Count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, held := range h.held {
		n += len(held)
	}
	return n
}

// Each calls fn with each connection key holds, oldest first.
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
		slog.Info("client replaced by a newer connection")
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

// keepAlive pings the peer until ctx ends, and drops the connection without a closing handshake, which a peer that
// does not answer would not finish, once it misses a pong.
func (h *Hub[T]) keepAlive(ctx context.Context, ws *websocket.Conn) {
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
				slog.Info("client stopped answering pings; connection dropped", "err", err)
				ws.CloseNow()
			}
			return
		}
	}
}
