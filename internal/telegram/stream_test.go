package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// chatOps records Bot API message operations as "send:ID:text",
// "edit:ID:text" and "delete:ID". Fail, if set, can fail a call.
type chatOps struct {
	mu   sync.Mutex
	ops  []string
	next int64
	Fail func(method string) *http.Response
}

func (c *chatOps) client() *Client {
	return &Client{Token: testToken, HTTP: HTTPClient{Do: func(req *http.Request) (*http.Response, error) {
		var body struct {
			Text      string `json:"text"`
			MessageID int64  `json:"message_id"`
		}
		_ = json.NewDecoder(req.Body).Decode(&body)
		method := req.URL.Path[strings.LastIndex(req.URL.Path, "/")+1:]
		if c.Fail != nil {
			if resp := c.Fail(method); resp != nil {
				return resp, nil
			}
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		switch method {
		case "sendMessage":
			c.next++
			c.ops = append(c.ops, "send:"+itoa(c.next)+":"+body.Text)
			return okResponse(map[string]any{"message_id": c.next}), nil
		case "editMessageText":
			c.ops = append(c.ops, "edit:"+itoa(body.MessageID)+":"+body.Text)
		case "deleteMessage":
			c.ops = append(c.ops, "delete:"+itoa(body.MessageID))
		}
		return okResponse(true), nil
	}}}
}

func (c *chatOps) Ops() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.ops...)
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }

// manualStream never flushes on its own; tests drive flush directly.
func manualStream(t *testing.T, c *chatOps) *stream {
	t.Helper()
	s := newStream(context.Background(), c.client(), 42, time.Hour, nil)
	t.Cleanup(func() { _ = s.finish(context.Background(), "cleanup") })
	return s
}

func TestStream(t *testing.T) {
	ctx := context.Background()

	t.Run("SuccessFlow", func(t *testing.T) {
		chat := &chatOps{}
		subject := manualStream(t, chat)

		subject.append("Hel")
		require.NoError(t, subject.flush(ctx, false))
		subject.append("lo")
		require.NoError(t, subject.flush(ctx, false))
		require.NoError(t, subject.flush(ctx, false)) // unchanged: no call
		require.NoError(t, subject.finish(ctx, "Hello!"))

		assert.Equal(t, []string{
			"send:1:Hel …",
			"edit:1:Hello …",
			"edit:1:Hello!",
		}, chat.Ops()[:3])
	})

	t.Run("Finish without streamed text sends one message", func(t *testing.T) {
		chat := &chatOps{}
		subject := newStream(ctx, chat.client(), 42, time.Hour, nil)

		require.NoError(t, subject.finish(ctx, "All done."))

		assert.Equal(t, []string{"send:1:All done."}, chat.Ops())
	})

	t.Run("Whitespace is not shown before real text arrives", func(t *testing.T) {
		chat := &chatOps{}
		subject := newStream(ctx, chat.client(), 42, time.Hour, nil)

		subject.append("\n  ")
		require.NoError(t, subject.flush(ctx, false))

		assert.Empty(t, chat.Ops())
		require.NoError(t, subject.finish(ctx, ""))
		assert.Equal(t, []string{"send:1:(no reply)"}, chat.Ops())
	})

	t.Run("Long replies spill into further messages", func(t *testing.T) {
		chat := &chatOps{}
		subject := newStream(ctx, chat.client(), 42, time.Hour, nil)
		long := strings.Repeat("a", MaxMessageLen) + strings.Repeat("b", 10)

		subject.append(long)
		require.NoError(t, subject.flush(ctx, false))
		require.NoError(t, subject.finish(ctx, long))

		ops := chat.Ops()
		require.Len(t, ops, 4)
		assert.True(t, strings.HasPrefix(ops[0], "send:1:aaa"))
		assert.True(t, strings.HasSuffix(ops[1], "b …"), "cursor on the last message only")
		assert.Equal(t, "edit:1:"+strings.Repeat("a", MaxMessageLen), ops[2])
		assert.Equal(t, "edit:2:"+strings.Repeat("b", 10), ops[3])
	})

	t.Run("Reset then a shorter reply deletes surplus messages", func(t *testing.T) {
		chat := &chatOps{}
		subject := newStream(ctx, chat.client(), 42, time.Hour, nil)
		subject.append(strings.Repeat("x", MaxMessageLen+100))
		require.NoError(t, subject.flush(ctx, false))

		subject.reset()
		subject.append("fresh start")
		require.NoError(t, subject.finish(ctx, "fresh start"))

		ops := chat.Ops()
		assert.Equal(t, []string{"edit:1:fresh start", "delete:2"}, ops[2:])
	})

	t.Run("Rate limits pause interim edits; the final edit waits once", func(t *testing.T) {
		var mu sync.Mutex
		limited := true
		chat := &chatOps{Fail: func(method string) *http.Response {
			mu.Lock()
			defer mu.Unlock()
			if method == "editMessageText" && limited {
				limited = false
				return &http.Response{StatusCode: 429, Body: jsonBody(map[string]any{
					"ok": false, "error_code": 429, "description": "Too Many Requests", "parameters": map[string]any{"retry_after": 1},
				})}
			}
			return nil
		}}
		subject := newStream(ctx, chat.client(), 42, time.Hour, nil)
		subject.append("one")
		require.NoError(t, subject.flush(ctx, false))
		subject.append(" two")
		assert.Error(t, subject.flush(ctx, false), "edit hits the rate limit")
		subject.append(" three")
		require.NoError(t, subject.flush(ctx, false), "held back, not attempted")

		start := time.Now()
		require.NoError(t, subject.finish(ctx, "one two three"))

		assert.Equal(t, []string{"send:1:one …", "edit:1:one two three"}, chat.Ops())
		assert.Less(t, time.Since(start), 3*time.Second)
	})

	t.Run("Flushes periodically on its own", func(t *testing.T) {
		chat := &chatOps{}
		subject := newStream(ctx, chat.client(), 42, 5*time.Millisecond, nil)

		subject.append("tick")

		assert.Eventually(t, func() bool { return len(chat.Ops()) == 1 }, 2*time.Second, 5*time.Millisecond)
		assert.Equal(t, "send:1:tick …", chat.Ops()[0])
		require.NoError(t, subject.finish(ctx, "tick"))
		assert.Equal(t, "edit:1:tick", chat.Ops()[1])
	})
}
