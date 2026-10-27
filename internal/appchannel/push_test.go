package appchannel

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kyleparisi/ganclaw/internal/agent"
	"github.com/kyleparisi/ganclaw/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPushRegistration(t *testing.T) {
	subject := (*Channel).Handler
	t.Run("Authenticated requests change only their own pairing", func(t *testing.T) {
		s := store.NewTestStore(t)
		c := &Channel{Store: s}
		c.setup()
		ctx := context.Background()
		require.NoError(t, s.AddAppDevice(ctx, "a", "phone", "a-token"))
		require.NoError(t, s.AddAppDevice(ctx, "b", "tablet", "b-token"))
		handler := subject(c, "test")
		request := func(method, token, body string) int {
			r := httptest.NewRequest(method, "/app/v1/me/push", strings.NewReader(body))
			if token != "" {
				r.Header.Set("Authorization", "Bearer "+token)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			return w.Code
		}
		body := `{"token":"` + strings.Repeat("ab", 32) + `"}`
		assert.Equal(t, http.StatusUnauthorized, request("PUT", "", body))
		assert.Equal(t, http.StatusOK, request("PUT", "a-token", body))
		assert.Equal(t, http.StatusOK, request("DELETE", "b-token", ""))
		devices, err := s.AppPushes(ctx)
		require.NoError(t, err)
		require.Len(t, devices, 1)
		assert.Equal(t, "a", devices[0].DeviceID)
		assert.Equal(t, http.StatusBadRequest, request("PUT", "a-token", `{"token":"bad"}`))
		_, err = s.RemoveAppDevice(ctx, "a")
		require.NoError(t, err)
		assert.Equal(t, http.StatusUnauthorized, request("PUT", "a-token", body))
	})
}

func TestNotificationSelection(t *testing.T) {
	subject := (*Channel).Message
	t.Run("Final reply notifies once while chunks users and hidden task mirrors do not", func(t *testing.T) {
		ctx := context.Background()
		s := store.NewTestStore(t)
		require.NoError(t, s.AddAppDevice(ctx, "a", "phone", "token"))
		require.NoError(t, s.SetAppPush(ctx, "a", "apns", "1"))
		delivered := make(chan store.AppMessage, 10)
		c := &Channel{Store: s, Agents: map[string]agent.Agent{"support": {}}, Push: func(ctx context.Context, d store.AppPush, m store.AppMessage) (bool, error) {
			delivered <- m
			return false, nil
		}}
		msg := subject(c, "support", "hello", "")
		stream := msg.Stream(ctx)
		stream.Append("first")
		stream.Append("second")
		_, err := c.Post(ctx, "support", store.RoleUser, "hello", "")
		require.NoError(t, err)
		require.NoError(t, stream.Finish(ctx, "complete"))
		require.NoError(t, stream.Finish(ctx, "complete"))
		hidden := &turn{c: c, agent: "task:example", hidden: true}
		require.NoError(t, hidden.reply(ctx, "complete"))
		select {
		case got := <-delivered:
			assert.Equal(t, "complete", got.Text)
		case <-time.After(time.Second):
			t.Fatal("no push")
		}
		select {
		case <-delivered:
			t.Fatal("duplicate push")
		case <-time.After(30 * time.Millisecond):
		}
	})
}

func TestNotificationRevocation(t *testing.T) {
	subject := (*Channel).notify
	t.Run("Permanent rejection removes registration while delivery does not fail the reply", func(t *testing.T) {
		ctx := context.Background()
		s := store.NewTestStore(t)
		require.NoError(t, s.AddAppDevice(ctx, "a", "phone", "token"))
		require.NoError(t, s.SetAppPush(ctx, "a", "apns", "1"))
		c := &Channel{Store: s, Push: func(context.Context, store.AppPush, store.AppMessage) (bool, error) {
			return true, errors.New("rejected")
		}}
		c.setup()
		subject(c, store.AppMessage{Thread: "support", Role: store.RoleAgent, Text: "done"})
		require.Eventually(t, func() bool { devices, err := s.AppPushes(ctx); return err == nil && len(devices) == 0 }, time.Second, time.Millisecond)
	})
	t.Run("File only completion notifies once", func(t *testing.T) {
		ctx := context.Background()
		s := store.NewTestStore(t)
		require.NoError(t, s.AddAppDevice(ctx, "a", "phone", "token"))
		require.NoError(t, s.SetAppPush(ctx, "a", "apns", "1"))
		delivered := make(chan struct{}, 2)
		c := &Channel{Store: s, Push: func(context.Context, store.AppPush, store.AppMessage) (bool, error) {
			delivered <- struct{}{}
			return false, nil
		}}
		c.setup()
		turn := &turn{c: c, msg: &store.AppMessage{Status: store.StatusDone, Files: []store.AppFile{{ID: "example"}}}}
		turn.notifyFinal()
		turn.notifyFinal()
		select {
		case <-delivered:
		case <-time.After(time.Second):
			t.Fatal("no push")
		}
		assert.Empty(t, delivered)
	})
}
