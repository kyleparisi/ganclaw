package codex

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pipeConn wires a conn to in-memory pipes. The test plays the server by
// reading requests from `requests` and writing lines to `server`.
func pipeConn(t *testing.T, onNotify NotifyFunc, onRequest RequestFunc) (c *conn, requests *json.Decoder, server *io.PipeWriter, loopErr chan error) {
	t.Helper()
	toServerR, toServerW := io.Pipe()
	fromServerR, fromServerW := io.Pipe()
	c = newConn(toServerW, onNotify, onRequest)
	loopErr = make(chan error, 1)
	go func() { loopErr <- c.readLoop(fromServerR) }()
	t.Cleanup(func() {
		toServerR.Close()
		fromServerW.Close()
	})
	return c, json.NewDecoder(toServerR), fromServerW, loopErr
}

func writeLine(t *testing.T, w io.Writer, s string) {
	t.Helper()
	_, err := io.WriteString(w, s+"\n")
	require.NoError(t, err)
}

func TestConnCall(t *testing.T) {
	t.Run("Out-of-order responses are matched by id", func(t *testing.T) {
		c, requests, server, _ := pipeConn(t, nil, nil)

		type out struct {
			res map[string]string
			err error
		}
		first, second := make(chan out, 1), make(chan out, 1)
		go func() {
			var r map[string]string
			err := c.call(context.Background(), "a", nil, &r)
			first <- out{r, err}
		}()
		var m1 message
		require.NoError(t, requests.Decode(&m1))
		go func() {
			var r map[string]string
			err := c.call(context.Background(), "b", nil, &r)
			second <- out{r, err}
		}()
		var m2 message
		require.NoError(t, requests.Decode(&m2))

		writeLine(t, server, `{"id":`+string(m2.ID)+`,"result":{"from":"b"}}`)
		writeLine(t, server, `{"id":`+string(m1.ID)+`,"result":{"from":"a"}}`)

		assert.Equal(t, map[string]string{"from": "a"}, (<-first).res)
		assert.Equal(t, map[string]string{"from": "b"}, (<-second).res)
		assert.Equal(t, "a", m1.Method)
		assert.Equal(t, "b", m2.Method)
	})

	t.Run("RPC error is returned", func(t *testing.T) {
		c, requests, server, _ := pipeConn(t, nil, nil)
		errc := make(chan error, 1)
		go func() { errc <- c.call(context.Background(), "x", map[string]int{"n": 1}, nil) }()

		var m message
		require.NoError(t, requests.Decode(&m))
		assert.JSONEq(t, `{"n":1}`, string(m.Params))
		writeLine(t, server, `{"id":`+string(m.ID)+`,"error":{"code":-32602,"message":"bad params"}}`)

		err := <-errc
		var re *rpcError
		require.ErrorAs(t, err, &re)
		assert.Equal(t, -32602, re.Code)
		assert.Equal(t, "bad params", re.Message)
	})

	t.Run("Pending calls fail when the connection closes", func(t *testing.T) {
		c, requests, server, loopErr := pipeConn(t, nil, nil)
		errc := make(chan error, 1)
		go func() { errc <- c.call(context.Background(), "x", nil, nil) }()
		var m message
		require.NoError(t, requests.Decode(&m))

		server.Close()

		assert.ErrorContains(t, <-errc, "connection closed")
		assert.ErrorIs(t, <-loopErr, io.ErrUnexpectedEOF)
		assert.ErrorContains(t, c.call(context.Background(), "y", nil, nil), "connection closed", "calls after close fail fast")
	})

	t.Run("Context cancellation abandons the call", func(t *testing.T) {
		c, requests, _, _ := pipeConn(t, nil, nil)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		go func() { var m message; _ = requests.Decode(&m) }()

		err := c.call(ctx, "slow", nil, nil)

		assert.ErrorIs(t, err, context.DeadlineExceeded)
	})
}

func TestConnDispatch(t *testing.T) {
	t.Run("Notifications go to the notify handler", func(t *testing.T) {
		got := make(chan string, 1)
		_, _, server, _ := pipeConn(t, func(method string, params json.RawMessage) {
			got <- method + " " + string(params)
		}, nil)

		writeLine(t, server, `{"method":"turn/completed","params":{"threadId":"t"}}`)

		assert.Equal(t, `turn/completed {"threadId":"t"}`, <-got)
	})

	t.Run("Server requests are answered with the handler result", func(t *testing.T) {
		_, requests, server, _ := pipeConn(t, nil, func(method string, params json.RawMessage) (any, error) {
			assert.Equal(t, "item/tool/call", method)
			return map[string]bool{"success": true}, nil
		})

		writeLine(t, server, `{"id":"srv-1","method":"item/tool/call","params":{}}`)

		var reply message
		require.NoError(t, requests.Decode(&reply))
		assert.JSONEq(t, `"srv-1"`, string(reply.ID), "string ids are echoed verbatim")
		assert.JSONEq(t, `{"success":true}`, string(reply.Result))
		assert.Nil(t, reply.Error)
	})

	t.Run("Server requests without a handler get method-not-found", func(t *testing.T) {
		_, requests, server, _ := pipeConn(t, nil, nil)

		writeLine(t, server, `{"id":5,"method":"attestation/generate","params":{}}`)

		var reply message
		require.NoError(t, requests.Decode(&reply))
		require.NotNil(t, reply.Error)
		assert.Equal(t, -32601, reply.Error.Code)
	})
}
