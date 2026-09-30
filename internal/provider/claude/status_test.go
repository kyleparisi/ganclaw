package claude

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kyleparisi/ganclaw/internal/procexec"
	"github.com/kyleparisi/ganclaw/internal/provider"
)

const (
	lineRateWarn = `{"type":"rate_limit_event","session_id":"sess-1","rate_limit_info":{"status":"allowed_warning","rateLimitType":"five_hour","resetsAt":1790740200,"unifiedWindows":{"five_hour":{"utilization":0.82,"resetsAt":1790740200},"seven_day":{"utilization":0.17,"resetsAt":1790920800}}}}`
	lineRateOut  = `{"type":"rate_limit_event","session_id":"sess-1","rate_limit_info":{"status":"rejected","rateLimitType":"seven_day","resetsAt":1790920800,"unifiedWindows":{"seven_day":{"utilization":1.0,"resetsAt":1790920800}}}}`
	lineRateErr  = `{"type":"result","subtype":"error_during_execution","is_error":true,"session_id":"sess-1","result":"","api_error_status":429}`
)

func TestProviderStatus(t *testing.T) {
	ctx := context.Background()

	t.Run("Unknown until a turn has run", func(t *testing.T) {
		subject := New(Config{Environ: func() []string { return nil }})

		st, err := subject.Status(ctx)

		require.NoError(t, err)
		assert.False(t, st.Known)
		assert.True(t, st.Available, "unknown must not block the provider")
		assert.Equal(t, "no limit data until a turn runs", st.Reason)
	})

	t.Run("Reports windows from the last turn", func(t *testing.T) {
		var cmd procexec.Cmd
		subject := New(Config{
			Environ: func() []string { return nil },
			Exec:    fakeExec(&cmd, nil, jsonl(lineInit, lineRateWarn, lineSuccess), "", nil),
		})
		_, err := subject.Run(ctx, provider.Request{Prompt: "x"})
		require.NoError(t, err)

		st, err := subject.Status(ctx)

		require.NoError(t, err)
		assert.True(t, st.Known)
		assert.True(t, st.Available)
		assert.Equal(t, "approaching 5-hour limit", st.Reason)
		require.Len(t, st.Windows, 2)
		assert.Equal(t, "5-hour", st.Windows[0].Name)
		assert.InDelta(t, 82, st.Windows[0].UsedPercent, 0.001)
		assert.Equal(t, time.Unix(1790740200, 0), st.Windows[0].ResetsAt)
		assert.Equal(t, "weekly", st.Windows[1].Name)
	})

	t.Run("Rejected limit is unavailable until reset and the error carries it", func(t *testing.T) {
		now := time.Unix(1790900000, 0)
		var cmd procexec.Cmd
		subject := New(Config{
			Environ: func() []string { return nil },
			Exec:    fakeExec(&cmd, nil, jsonl(lineInit, lineRateOut, lineRateErr), "", nil),
		})
		subject.now = func() time.Time { return now }

		_, runErr := subject.Run(ctx, provider.Request{Prompt: "x"})
		st, err := subject.Status(ctx)

		var ra provider.RetryAter
		require.ErrorAs(t, runErr, &ra)
		assert.Equal(t, time.Unix(1790920800, 0), ra.RetryAt())
		require.NoError(t, err)
		assert.False(t, st.Available)
		assert.Equal(t, "rate limited (weekly)", st.Reason)
		assert.Equal(t, time.Unix(1790920800, 0), st.ResetsAt)

		now = time.Unix(1790920801, 0)
		st, err = subject.Status(ctx)
		require.NoError(t, err)
		assert.True(t, st.Available, "rejection expires at its reset time")
	})
}
