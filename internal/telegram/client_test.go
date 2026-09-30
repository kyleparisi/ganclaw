package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testToken = "123456:TEST-token-value"

func jsonBody(v any) io.ReadCloser {
	b, _ := json.Marshal(v)
	return io.NopCloser(bytes.NewReader(b))
}

func okResponse(result any) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Body: jsonBody(map[string]any{"ok": true, "result": result})}
}

func TestClientCall(t *testing.T) {
	ctx := context.Background()

	t.Run("SendMessage posts JSON to the method URL", func(t *testing.T) {
		var calls int
		subject := &Client{
			Token:   testToken,
			BaseURL: "https://tg.test",
			HTTP: HTTPClient{Do: func(req *http.Request) (*http.Response, error) {
				calls++
				assert.Equal(t, http.MethodPost, req.Method)
				assert.Equal(t, "https://tg.test/bot"+testToken+"/sendMessage", req.URL.String())
				assert.Equal(t, "application/json", req.Header.Get("Content-Type"))
				var body map[string]any
				assert.NoError(t, json.NewDecoder(req.Body).Decode(&body))
				assert.Equal(t, float64(42), body["chat_id"])
				assert.Equal(t, "hello", body["text"])
				return okResponse(map[string]any{"message_id": 1}), nil
			}},
		}

		err := subject.SendMessage(ctx, 42, "hello")

		require.NoError(t, err)
		assert.Equal(t, 1, calls)
	})

	t.Run("Long messages are split across sends", func(t *testing.T) {
		var sent []string
		subject := &Client{Token: testToken, HTTP: HTTPClient{Do: func(req *http.Request) (*http.Response, error) {
			var body struct{ Text string }
			assert.NoError(t, json.NewDecoder(req.Body).Decode(&body))
			sent = append(sent, body.Text)
			return okResponse(map[string]any{"message_id": len(sent)}), nil
		}}}

		err := subject.SendMessage(ctx, 1, strings.Repeat("a", MaxMessageLen)+"\n"+"tail")

		require.NoError(t, err)
		require.Len(t, sent, 2)
		assert.Equal(t, "tail", sent[1])
	})

	t.Run("GetUpdates decodes messages and sends poll params", func(t *testing.T) {
		subject := &Client{Token: testToken, HTTP: HTTPClient{Do: func(req *http.Request) (*http.Response, error) {
			assert.True(t, strings.HasSuffix(req.URL.Path, "/getUpdates"))
			var body map[string]any
			assert.NoError(t, json.NewDecoder(req.Body).Decode(&body))
			assert.Equal(t, float64(7), body["offset"])
			assert.Equal(t, float64(50), body["timeout"])
			assert.Equal(t, []any{"message"}, body["allowed_updates"])
			return okResponse([]map[string]any{{
				"update_id": 7,
				"message": map[string]any{
					"message_id": 3, "text": "hi",
					"from": map[string]any{"id": 111, "username": "alice"},
					"chat": map[string]any{"id": 111, "type": "private"},
				},
			}}), nil
		}}}

		ups, err := subject.GetUpdates(ctx, 7, 50)

		require.NoError(t, err)
		require.Len(t, ups, 1)
		assert.Equal(t, int64(7), ups[0].UpdateID)
		assert.Equal(t, "hi", ups[0].Message.Text)
		assert.Equal(t, int64(111), ups[0].Message.From.ID)
		assert.Equal(t, "private", ups[0].Message.Chat.Type)
	})

	t.Run("API errors carry code, description and retry_after", func(t *testing.T) {
		subject := &Client{Token: testToken, HTTP: HTTPClient{Do: func(req *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusTooManyRequests, Body: jsonBody(map[string]any{
				"ok": false, "error_code": 429, "description": "Too Many Requests: retry after 3",
				"parameters": map[string]any{"retry_after": 3},
			})}, nil
		}}}

		err := subject.SendChatAction(ctx, 1, "typing")

		var apiErr *APIError
		require.ErrorAs(t, err, &apiErr)
		assert.Equal(t, "sendChatAction", apiErr.Method)
		assert.Equal(t, 429, apiErr.Code)
		assert.Equal(t, 3, apiErr.RetryAfter)
	})

	t.Run("Transport errors never contain the token", func(t *testing.T) {
		subject := &Client{Token: testToken, HTTP: HTTPClient{Do: func(req *http.Request) (*http.Response, error) {
			return nil, errors.New(`Post "` + req.URL.String() + `": dial tcp: connection refused`)
		}}}

		_, err := subject.GetMe(ctx)

		require.Error(t, err)
		assert.NotContains(t, err.Error(), testToken)
		assert.Contains(t, err.Error(), "/bot<token>/getMe")
	})

	t.Run("Redacted errors still match context cancellation", func(t *testing.T) {
		subject := &Client{Token: testToken, HTTP: HTTPClient{Do: func(req *http.Request) (*http.Response, error) {
			return nil, errors.Join(errors.New(req.URL.String()), context.Canceled)
		}}}

		_, err := subject.GetUpdates(ctx, 0, 1)

		assert.ErrorIs(t, err, context.Canceled)
		assert.NotContains(t, err.Error(), testToken)
	})

	t.Run("SetMyCommands sends the command list", func(t *testing.T) {
		subject := &Client{Token: testToken, HTTP: HTTPClient{Do: func(req *http.Request) (*http.Response, error) {
			assert.True(t, strings.HasSuffix(req.URL.Path, "/setMyCommands"))
			var body map[string]any
			assert.NoError(t, json.NewDecoder(req.Body).Decode(&body))
			assert.Equal(t, []any{map[string]any{"command": "new", "description": "Start over"}}, body["commands"])
			return okResponse(true), nil
		}}}

		err := subject.SetMyCommands(ctx, []BotCommand{{Command: "new", Description: "Start over"}})

		require.NoError(t, err)
	})

	t.Run("Editing to identical text is not an error", func(t *testing.T) {
		subject := &Client{Token: testToken, HTTP: HTTPClient{Do: func(req *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 400, Body: jsonBody(map[string]any{
				"ok": false, "error_code": 400,
				"description": "Bad Request: message is not modified: specified new message content and reply markup are exactly the same",
			})}, nil
		}}}

		assert.NoError(t, subject.EditMessageText(ctx, 1, 2, "same"))
	})

	t.Run("Other edit errors are returned", func(t *testing.T) {
		subject := &Client{Token: testToken, HTTP: HTTPClient{Do: func(req *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 400, Body: jsonBody(map[string]any{
				"ok": false, "error_code": 400, "description": "Bad Request: message to edit not found",
			})}, nil
		}}}

		assert.ErrorContains(t, subject.EditMessageText(ctx, 1, 2, "x"), "not found")
	})

	t.Run("Non-JSON response is an error", func(t *testing.T) {
		subject := &Client{Token: testToken, HTTP: HTTPClient{Do: func(req *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusBadGateway, Body: io.NopCloser(strings.NewReader("<html>bad gateway</html>"))}, nil
		}}}

		_, err := subject.GetMe(ctx)

		assert.ErrorContains(t, err, "HTTP 502")
	})
}

func TestSplitText(t *testing.T) {
	subject := SplitText

	t.Run("Short text is one part", func(t *testing.T) {
		assert.Equal(t, []string{"hello"}, subject("hello", 10))
	})

	t.Run("Empty text is one empty part", func(t *testing.T) {
		assert.Equal(t, []string{""}, subject("", 10))
	})

	t.Run("Prefers newlines, then spaces, then hard cuts", func(t *testing.T) {
		assert.Equal(t, []string{"aaaa\n", "bbbbbb"}, subject("aaaa\nbbbbbb", 8))
		assert.Equal(t, []string{"aaaaa ", "bbbbbb"}, subject("aaaaa bbbbbb", 8))
		assert.Equal(t, []string{"aaaaaaaa", "aa"}, subject("aaaaaaaaaa", 8))
	})

	t.Run("Counts characters, not bytes, and never splits a rune", func(t *testing.T) {
		parts := subject(strings.Repeat("é", 10), 4)

		assert.Equal(t, []string{"éééé", "éééé", "éé"}, parts)
	})
}
