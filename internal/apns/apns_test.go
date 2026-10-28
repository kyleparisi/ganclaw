package apns

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kyleparisi/ganclaw/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSend(t *testing.T) {
	subject := (*Client).Send
	makeClient := func() *Client {
		return &Client{Config: Config{Environment: "sandbox", Topic: "com.example.app", KeyID: "example", TeamID: "example"}, Now: func() time.Time { return time.Unix(1000, 0) }, Sign: func([]byte) ([]byte, error) { return make([]byte, 64), nil }}
	}
	t.Run("Sends a generic alert with routing and caches provider authentication", func(t *testing.T) {
		c := makeClient()
		signed := 0
		c.Sign = func([]byte) ([]byte, error) { signed++; return make([]byte, 64), nil }
		c.Do = func(r *http.Request) (*http.Response, error) {
			assert.Equal(t, "api.sandbox.push.apple.com", r.URL.Host)
			assert.Equal(t, "/3/device/example", r.URL.Path)
			assert.Equal(t, "alert", r.Header.Get("apns-push-type"))
			assert.Equal(t, "com.example.app", r.Header.Get("apns-topic"))
			assert.Equal(t, "reply-12", r.Header.Get("apns-collapse-id"))
			assert.NotEmpty(t, r.Header.Get("authorization"))
			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			assert.NotContains(t, string(body), "private reply")
			assert.Contains(t, string(body), `"thread":"support"`)
			assert.Contains(t, string(body), `"body":"support replied."`)
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(""))}, nil
		}
		for range 2 {
			invalid, err := subject(c, context.Background(), store.AppPush{Token: "example"}, store.AppMessage{ID: 12, Thread: "support", Text: "private reply"})
			require.NoError(t, err)
			assert.False(t, invalid)
		}
		assert.Equal(t, 1, signed)
	})
	t.Run("Includes the unread badge including zero", func(t *testing.T) {
		c := makeClient()
		c.Do = func(r *http.Request) (*http.Response, error) {
			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			assert.Contains(t, string(body), `"badge":0`)
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(""))}, nil
		}
		count := 0
		_, err := subject(c, context.Background(), store.AppPush{Token: "example"}, store.AppMessage{Badge: &count})
		require.NoError(t, err)
	})

	t.Run("Revokes inactive tokens without deleting newer registrations", func(t *testing.T) {
		c := makeClient()
		c.Do = func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 410, Body: io.NopCloser(strings.NewReader(`{"reason":"Unregistered","timestamp":2000}`))}, nil
		}
		invalid, err := subject(c, context.Background(), store.AppPush{Registered: 1000}, store.AppMessage{})
		require.Error(t, err)
		assert.True(t, invalid)
		invalid, err = subject(c, context.Background(), store.AppPush{Registered: 3000}, store.AppMessage{})
		require.Error(t, err)
		assert.False(t, invalid)
	})
	t.Run("Transport failures expose no credentials and are not retried", func(t *testing.T) {
		c := makeClient()
		calls := 0
		c.Do = func(*http.Request) (*http.Response, error) { calls++; return nil, errors.New("private token") }
		invalid, err := subject(c, context.Background(), store.AppPush{}, store.AppMessage{})
		assert.False(t, invalid)
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "private token")
		assert.Equal(t, 1, calls)
	})
	t.Run("Service failures retain registration", func(t *testing.T) {
		c := makeClient()
		c.Do = func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader(""))}, nil
		}
		invalid, err := subject(c, context.Background(), store.AppPush{}, store.AppMessage{})
		require.Error(t, err)
		assert.False(t, invalid)
	})
}
func TestAlertText(t *testing.T) {
	subject := AlertText
	t.Run("Names the agent and job, never the reply", func(t *testing.T) {
		assert.Equal(t, "infra replied.", subject(store.AppMessage{Thread: "infra", Text: "secret"}))
		assert.Equal(t, "infra ran into a problem.", subject(store.AppMessage{Thread: "infra", Status: store.StatusError}))
		assert.Equal(t, "infra: daily-check finished.", subject(store.AppMessage{Thread: "infra", Job: "daily-check"}))
		assert.Equal(t, "infra: daily-check failed.", subject(store.AppMessage{Thread: "infra", Job: "daily-check", Status: store.StatusError}))
	})
}

func TestNew(t *testing.T) {
	subject := New
	t.Run("Disabled by default", func(t *testing.T) { c, err := subject(Config{}); require.NoError(t, err); assert.Nil(t, c) })
	t.Run("Rejects partial configuration", func(t *testing.T) { _, err := subject(Config{Topic: "com.example.app"}); require.Error(t, err) })
}
