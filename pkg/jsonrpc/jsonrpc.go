package jsonrpc

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/sync/semaphore"
)

type Message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string {
	return fmt.Sprintf("jsonrpc error %d: %s", e.Code, e.Message)
}

const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
	CodeServerError    = -32000
)

func Errorf(code int, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

type Handler interface {
	// Call answers a request; ctx ends when the connection does.
	Call(ctx context.Context, method string, params json.RawMessage) (result any, after func(), err *Error)
	Notify(method string, params json.RawMessage)
}

type ResponseFunc func(result json.RawMessage, err *Error)

const (
	writeTimeout   = 10 * time.Second
	maxOutboxBytes = 16 << 20
	// A client may send this many messages, and this many bytes of them, ahead of the one being handled before
	// reading waits for room.
	maxInboxMessages = 64
	maxInboxBytes    = 16 << 20
)

type Conn struct {
	ws *websocket.Conn

	mu      sync.Mutex
	nextID  int
	pending map[string]ResponseFunc
	outbox  [][]byte
	// held keeps what is sent while a call holds it, until the call's reply has gone into outbox.
	held    [][]byte
	holding bool
	queued  int
	closed  bool
	ready   chan struct{}
}

func NewConn(ws *websocket.Conn) *Conn {
	return &Conn{ws: ws, pending: make(map[string]ResponseFunc), ready: make(chan struct{}, 1)}
}

// Serve handles the client's messages one at a time, in the order they came, and returns what ended reading them.
// Reading goes on while one is handled: the pong to a ping is read only while a read is in progress, so a slow call
// would otherwise look like a dead peer.
func (c *Conn) Serve(ctx context.Context, h Handler) (err error) {
	ctx, cancel := context.WithCancel(ctx)
	written := make(chan struct{})
	go func() {
		defer close(written)
		c.write()
	}()
	room := semaphore.NewWeighted(maxInboxBytes)
	inbox := make(chan []byte, maxInboxMessages)
	var readErr error
	go func() {
		defer close(inbox)
		readErr = c.read(ctx, room, inbox)
		cancel()
	}()
	defer func() {
		cancel()
		for range inbox {
		}
		err = readErr
		c.mu.Lock()
		c.closed = true
		c.mu.Unlock()
		c.wake()
		<-written
	}()
	for data := range inbox {
		if ctx.Err() != nil {
			break
		}
		c.handle(ctx, h, data)
		room.Release(inboxWeight(data))
	}
	return nil
}

// read passes the client's text messages to inbox until reading fails.
func (c *Conn) read(ctx context.Context, room *semaphore.Weighted, inbox chan<- []byte) error {
	for {
		kind, data, err := c.ws.Read(ctx)
		if err != nil {
			return err
		}
		if kind != websocket.MessageText {
			c.reply(nil, nil, Errorf(CodeInvalidRequest, "messages must be text"))
			continue
		}
		if err := room.Acquire(ctx, inboxWeight(data)); err != nil {
			return err
		}
		select {
		case inbox <- data:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// inboxWeight is what a message takes of the inbox's room; one larger than all of it takes all of it.
func inboxWeight(data []byte) int64 {
	return int64(min(len(data), maxInboxBytes))
}

func (c *Conn) handle(ctx context.Context, h Handler, data []byte) {
	var m Message
	if err := json.Unmarshal(data, &m); err != nil {
		c.reply(nil, nil, Errorf(CodeParseError, "%v", err))
		return
	}
	switch {
	case m.JSONRPC != "2.0":
		c.reply(m.ID, nil, Errorf(CodeInvalidRequest, `jsonrpc must be "2.0"`))
	case m.Method != "" && m.ID != nil:
		result, after, rerr := h.Call(ctx, m.Method, m.Params)
		c.reply(m.ID, result, rerr)
		c.release()
		if rerr == nil && after != nil {
			after()
		}
	case m.Method != "":
		h.Notify(m.Method, m.Params)
	case m.ID != nil:
		c.response(m)
	default:
		c.reply(nil, nil, Errorf(CodeInvalidRequest, "not a request, notification, or response"))
	}
}

// HoldUntilReplied keeps what is sent from now on behind the reply to the call being answered, so that a client reads
// a view before the changes made after it was read. Only Call may use it; the reply releases what it held.
func (c *Conn) HoldUntilReplied() {
	c.mu.Lock()
	c.holding = true
	c.mu.Unlock()
}

func (c *Conn) release() {
	c.mu.Lock()
	c.outbox, c.held, c.holding = append(c.outbox, c.held...), nil, false
	c.mu.Unlock()
	c.wake()
}

func (c *Conn) Notify(method string, params any) {
	c.send(Message{JSONRPC: "2.0", Method: method, Params: encode(params)})
}

func (c *Conn) Request(method string, params any, onResponse ResponseFunc) {
	c.mu.Lock()
	c.nextID++
	id := fmt.Sprint(c.nextID)
	c.pending[id] = onResponse
	c.mu.Unlock()
	c.send(Message{JSONRPC: "2.0", ID: json.RawMessage(id), Method: method, Params: encode(params)})
}

func (c *Conn) response(m Message) {
	c.mu.Lock()
	onResponse, ok := c.pending[string(m.ID)]
	delete(c.pending, string(m.ID))
	c.mu.Unlock()
	if !ok {
		slog.Warn("response to unknown request", "id", string(m.ID))
		return
	}
	onResponse(m.Result, m.Error)
}

func (c *Conn) reply(id json.RawMessage, result any, rerr *Error) {
	if id == nil {
		id = json.RawMessage("null")
	}
	m := Message{JSONRPC: "2.0", ID: id, Error: rerr}
	if rerr == nil {
		raw, err := json.Marshal(result)
		if err != nil {
			slog.Error("result not encodable", "err", err)
			raw, m.Error = nil, Errorf(CodeInternalError, "the result cannot be encoded: %v", err)
		}
		m.Result = raw
	}
	c.enqueue(encode(m), true)
}

func (c *Conn) send(m Message) {
	c.enqueue(encode(m), false)
}

func (c *Conn) enqueue(data []byte, reply bool) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	if c.queued+len(data) > maxOutboxBytes {
		c.closed, c.outbox, c.held, c.queued = true, nil, nil, 0
		c.mu.Unlock()
		slog.Warn("client fell behind; connection closed", "bytes", maxOutboxBytes)
		go c.ws.Close(websocket.StatusPolicyViolation, "client is not reading fast enough")
		c.wake()
		return
	}
	if c.holding && !reply {
		c.held = append(c.held, data)
	} else {
		c.outbox = append(c.outbox, data)
	}
	c.queued += len(data)
	c.mu.Unlock()
	c.wake()
}

func (c *Conn) wake() {
	select {
	case c.ready <- struct{}{}:
	default:
	}
}

func (c *Conn) write() {
	for range c.ready {
		c.mu.Lock()
		batch, closed := c.outbox, c.closed
		c.outbox = nil
		c.mu.Unlock()
		for _, data := range batch {
			ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
			err := c.ws.Write(ctx, websocket.MessageText, data)
			cancel()
			c.mu.Lock()
			c.queued = max(0, c.queued-len(data))
			c.mu.Unlock()
			if err != nil {
				slog.Warn("write to client failed", "err", err)
				c.ws.Close(websocket.StatusInternalError, "write failed")
				c.mu.Lock()
				c.closed, c.outbox, c.held, c.queued = true, nil, nil, 0
				c.mu.Unlock()
				closed = true
				break
			}
		}
		if closed {
			return
		}
	}
}

func DecodeParams(params json.RawMessage, out any) *Error {
	if len(params) == 0 {
		params = json.RawMessage("{}")
	}
	dec := json.NewDecoder(strings.NewReader(string(params)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return Errorf(CodeInvalidParams, "%v", err)
	}
	return nil
}

func encode(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		slog.Error("message not encodable", "type", fmt.Sprintf("%T", v), "err", err)
		return json.RawMessage("null")
	}
	return b
}
