package codex

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kyleparisi/ganclaw/internal/provider"
)

func rateLimits(allowed bool, reached any, primary, secondary map[string]any, credits map[string]any) map[string]any {
	rl := map[string]any{"limitId": "codex", "planType": "prolite", "primary": primary, "secondary": secondary, "rateLimitReachedType": reached}
	if credits != nil {
		rl["credits"] = credits
	}
	return map[string]any{"ordinaryUsageAllowed": allowed, "rateLimits": rl}
}

func TestProviderStatus(t *testing.T) {
	ctx := context.Background()

	t.Run("Exhausted weekly limit is unavailable until it resets", func(t *testing.T) {
		server := &fakeAppServer{
			Handle: func(s *fakeAppServer, method string, params json.RawMessage) (any, *rpcError) {
				assert.Equal(t, "account/rateLimits/read", method)
				return rateLimits(false, "rate_limit_reached",
					map[string]any{"usedPercent": 100, "windowDurationMins": 10080, "resetsAt": 1791048011},
					map[string]any{"usedPercent": 40, "windowDurationMins": 300, "resetsAt": 1790000000},
					map[string]any{"hasCredits": false, "unlimited": false, "balance": "0"}), nil
			},
		}
		subject := newTestProvider(t, server)

		st, err := subject.Status(ctx)

		require.NoError(t, err)
		assert.True(t, st.Known)
		assert.False(t, st.Available)
		assert.Equal(t, "rate limit reached, prolite plan", st.Reason)
		assert.Equal(t, time.Unix(1791048011, 0), st.ResetsAt)
		assert.Equal(t, []provider.Window{
			{Name: "weekly", UsedPercent: 100, ResetsAt: time.Unix(1791048011, 0)},
			{Name: "5-hour", UsedPercent: 40, ResetsAt: time.Unix(1790000000, 0)},
		}, st.Windows)
	})

	t.Run("Usage allowed is available", func(t *testing.T) {
		server := &fakeAppServer{
			Handle: func(s *fakeAppServer, method string, params json.RawMessage) (any, *rpcError) {
				return rateLimits(true, nil, map[string]any{"usedPercent": 12, "windowDurationMins": 300}, nil, nil), nil
			},
		}
		subject := newTestProvider(t, server)

		st, err := subject.Status(ctx)

		require.NoError(t, err)
		assert.True(t, st.Available)
		assert.Equal(t, "prolite plan", st.Reason)
		assert.True(t, st.ResetsAt.IsZero())
	})

	t.Run("Purchased credits make an exhausted plan available", func(t *testing.T) {
		server := &fakeAppServer{
			Handle: func(s *fakeAppServer, method string, params json.RawMessage) (any, *rpcError) {
				return rateLimits(false, "rate_limit_reached", map[string]any{"usedPercent": 100, "windowDurationMins": 10080, "resetsAt": 1791048011}, nil,
					map[string]any{"hasCredits": true, "unlimited": false, "balance": "25"}), nil
			},
		}
		subject := newTestProvider(t, server)

		st, err := subject.Status(ctx)

		require.NoError(t, err)
		assert.True(t, st.Available)
	})

	t.Run("Exited app-server reports unavailable without calling it", func(t *testing.T) {
		server := &fakeAppServer{}
		subject := newTestProvider(t, server)
		server.Crash(errors.New("exit status 1"))
		<-subject.done

		st, err := subject.Status(ctx)

		require.NoError(t, err)
		assert.True(t, st.Known)
		assert.False(t, st.Available)
		assert.Contains(t, st.Reason, "exit status 1")
	})

	t.Run("Usage-limit failures carry the reset time for the chain", func(t *testing.T) {
		server := &fakeAppServer{
			Handle: func(s *fakeAppServer, method string, params json.RawMessage) (any, *rpcError) {
				switch method {
				case "thread/start":
					return threadResult("thread-1"), nil
				case "turn/start":
					s.NotifyAfterReply("turn/completed", completed("thread-1", "turn-1", "failed", "usageLimitExceeded"))
					return turnResult("turn-1"), nil
				case "account/rateLimits/read":
					return rateLimits(false, "rate_limit_reached", map[string]any{"usedPercent": 100, "windowDurationMins": 10080, "resetsAt": 1791048011}, nil, nil), nil
				}
				return nil, nil
			},
		}
		subject := newTestProvider(t, server)

		_, err := subject.Run(ctx, provider.Request{Prompt: "hi"})

		var ra provider.RetryAter
		require.ErrorAs(t, err, &ra)
		assert.Equal(t, time.Unix(1791048011, 0), ra.RetryAt())
		assert.True(t, errors.Is(err, provider.ErrUnavailable))
	})

	t.Run("Other failures don't query limits", func(t *testing.T) {
		server := &fakeAppServer{
			Handle: func(s *fakeAppServer, method string, params json.RawMessage) (any, *rpcError) {
				switch method {
				case "thread/start":
					return threadResult("thread-1"), nil
				case "turn/start":
					s.NotifyAfterReply("turn/completed", completed("thread-1", "turn-1", "failed", "serverOverloaded"))
					return turnResult("turn-1"), nil
				case "account/rateLimits/read":
					t.Error("limits must not be queried for overload")
				}
				return nil, nil
			},
		}
		subject := newTestProvider(t, server)

		_, err := subject.Run(ctx, provider.Request{Prompt: "hi"})

		var terr *TurnError
		require.ErrorAs(t, err, &terr)
		assert.True(t, terr.RetryAt().IsZero())
	})
}
