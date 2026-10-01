package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kyleparisi/ganclaw/internal/api"
)

func TestRestartWhenIdle(t *testing.T) {
	ctx := context.Background()
	down := errors.New("no server")

	// healthSeq answers health checks in order, repeating the last answer.
	healthSeq := func(answers ...func() (api.HealthResponse, error)) func(context.Context) (api.HealthResponse, error) {
		i := 0
		return func(context.Context) (api.HealthResponse, error) {
			a := answers[min(i, len(answers)-1)]
			i++
			return a()
		}
	}
	up := func(active int) func() (api.HealthResponse, error) {
		return func() (api.HealthResponse, error) { return api.HealthResponse{Version: "v1", ActiveTurns: active}, nil }
	}
	gone := func() (api.HealthResponse, error) { return api.HealthResponse{}, down }

	t.Run("SuccessFlow", func(t *testing.T) {
		var events []string
		subject := restarter{
			Health:  healthSeq(up(2), up(1), up(0), gone, up(0)),
			Restart: func(context.Context) error { events = append(events, "restart"); return nil },
			Sleep:   func(time.Duration) { events = append(events, "sleep") },
			Log:     func(string) {},
		}

		err := subject.restartWhenIdle(ctx, time.Minute)

		require.NoError(t, err)
		assert.Equal(t, []string{"sleep", "sleep", "restart", "sleep", "sleep"}, events, "waits for idle, restarts, then waits for it to answer")
	})

	t.Run("A stopped gateway is not started", func(t *testing.T) {
		subject := restarter{
			Health:  healthSeq(gone),
			Restart: func(context.Context) error { t.Error("must not restart"); return nil },
			Sleep:   func(time.Duration) {},
			Log:     func(string) {},
		}

		assert.NoError(t, subject.restartWhenIdle(ctx, time.Minute))
	})

	t.Run("Restarts anyway once the wait is over", func(t *testing.T) {
		restarts := 0
		subject := restarter{
			Health:  healthSeq(up(1)),
			Restart: func(context.Context) error { restarts++; return nil },
			Sleep:   func(time.Duration) {},
			Log:     func(string) {},
		}

		require.NoError(t, subject.restartWhenIdle(ctx, 20*time.Second))
		assert.Equal(t, 1, restarts)
	})

	t.Run("A gateway that doesn't come back is an error", func(t *testing.T) {
		subject := restarter{
			Health:  healthSeq(up(0), gone),
			Restart: func(context.Context) error { return nil },
			Sleep:   func(time.Duration) {},
			Log:     func(string) {},
		}

		assert.ErrorContains(t, subject.restartWhenIdle(ctx, time.Minute), "not answering after restart")
	})
}

func TestPlatforms(t *testing.T) {
	subject := platforms

	t.Run("Maps Linux architectures to release names", func(t *testing.T) {
		claude, codex, err := subject("linux", "amd64")
		require.NoError(t, err)
		assert.Equal(t, "linux-x64", claude)
		assert.Equal(t, "x86_64-unknown-linux-musl", codex)
	})

	t.Run("Other systems are refused", func(t *testing.T) {
		_, _, err := subject("darwin", "arm64")
		assert.ErrorContains(t, err, "only Linux")
	})
}
