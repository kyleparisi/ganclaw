package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
)

// message is any JSON-RPC message on the wire. The app-server omits the
// "jsonrpc" field, so we don't send or require it.
type message struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message) }

// NotifyFunc handles server notifications.
type NotifyFunc func(method string, params json.RawMessage)

// RequestFunc handles server-initiated requests and returns the result
// (marshalled to JSON) or an error.
type RequestFunc func(method string, params json.RawMessage) (any, error)

// conn is a bidirectional JSON-RPC connection over a subprocess's stdio.
type conn struct {
	w      io.Writer
	wmu    sync.Mutex
	nextID atomic.Int64

	mu      sync.Mutex
	pending map[int64]chan message
	closed  error

	onNotify  NotifyFunc
	onRequest RequestFunc
}

func newConn(w io.Writer, onNotify NotifyFunc, onRequest RequestFunc) *conn {
	return &conn{w: w, pending: map[int64]chan message{}, onNotify: onNotify, onRequest: onRequest}
}

func (c *conn) write(m message) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, err = c.w.Write(append(b, '\n'))
	return err
}

// call sends a request and waits for its response.
func (c *conn) call(ctx context.Context, method string, params, result any) error {
	id := c.nextID.Add(1)
	ch := make(chan message, 1)

	c.mu.Lock()
	if c.closed != nil {
		c.mu.Unlock()
		return c.closed
	}
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	p, err := json.Marshal(params)
	if err != nil {
		return err
	}
	if err := c.write(message{ID: json.RawMessage(fmt.Sprint(id)), Method: method, Params: p}); err != nil {
		return err
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case m, ok := <-ch:
		if !ok {
			return c.closedErr()
		}
		if m.Error != nil {
			return m.Error
		}
		if result != nil && len(m.Result) > 0 {
			return json.Unmarshal(m.Result, result)
		}
		return nil
	}
}

// notify sends a notification (no response expected).
func (c *conn) notify(method string, params any) error {
	m := message{Method: method}
	if params != nil {
		p, err := json.Marshal(params)
		if err != nil {
			return err
		}
		m.Params = p
	}
	return c.write(m)
}

// readLoop dispatches incoming messages until r is exhausted, then fails
// all pending calls with the returned error.
func (c *conn) readLoop(r io.Reader) error {
	dec := json.NewDecoder(r)
	var err error
	for {
		var m message
		if err = dec.Decode(&m); err != nil {
			break
		}
		switch {
		case m.Method != "" && len(m.ID) > 0:
			go c.serve(m)
		case m.Method != "":
			if c.onNotify != nil {
				c.onNotify(m.Method, m.Params)
			}
		case len(m.ID) > 0:
			var id int64
			if json.Unmarshal(m.ID, &id) != nil {
				continue // not one of ours
			}
			c.mu.Lock()
			ch := c.pending[id]
			c.mu.Unlock()
			if ch != nil {
				ch <- m
			}
		}
	}
	if errors.Is(err, io.EOF) {
		err = io.ErrUnexpectedEOF
	}
	c.mu.Lock()
	c.closed = fmt.Errorf("codex app-server connection closed: %w", err)
	for id, ch := range c.pending {
		close(ch)
		delete(c.pending, id)
	}
	c.mu.Unlock()
	return err
}

func (c *conn) closedErr() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed != nil {
		return c.closed
	}
	return errors.New("codex app-server connection closed")
}

// serve answers a server-initiated request.
func (c *conn) serve(m message) {
	reply := message{ID: m.ID}
	if c.onRequest == nil {
		reply.Error = &rpcError{Code: -32601, Message: "method not supported: " + m.Method}
	} else if res, err := c.onRequest(m.Method, m.Params); err != nil {
		reply.Error = &rpcError{Code: -32000, Message: err.Error()}
	} else if b, err := json.Marshal(res); err != nil {
		reply.Error = &rpcError{Code: -32603, Message: err.Error()}
	} else {
		reply.Result = b
	}
	_ = c.write(reply)
}
