package followup

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kyleparisi/ganclaw/internal/store"
)

func TestParseDelay(t *testing.T) {
	subject := ParseDelay

	t.Run("Parses standard durations and day prefixes", func(t *testing.T) {
		d1, err := subject("30s")
		require.NoError(t, err)
		assert.Equal(t, 30*time.Second, d1)

		d2, err := subject("2m30s")
		require.NoError(t, err)
		assert.Equal(t, 150*time.Second, d2)

		d3, err := subject("2d")
		require.NoError(t, err)
		assert.Equal(t, 48*time.Hour, d3)

		d4, err := subject("1d12h")
		require.NoError(t, err)
		assert.Equal(t, 36*time.Hour, d4)

		d5, err := subject("0d45s")
		require.NoError(t, err)
		assert.Equal(t, 45*time.Second, d5)
	})

	t.Run("Rejects invalid or out-of-range delays", func(t *testing.T) {
		_, err := subject("10s")
		assert.ErrorIs(t, err, ErrInvalidDelay)

		_, err = subject("8d")
		assert.ErrorIs(t, err, ErrInvalidDelay)

		_, err = subject("999999999999d")
		assert.ErrorIs(t, err, ErrInvalidDelay)

		_, err = subject("-1m")
		assert.ErrorIs(t, err, ErrInvalidDelay)

		_, err = subject("0d")
		assert.ErrorIs(t, err, ErrInvalidDelay)

		_, err = subject("soon")
		assert.ErrorIs(t, err, ErrInvalidDelay)

		_, err = subject("")
		assert.ErrorIs(t, err, ErrInvalidDelay)
	})
}

func TestValidateDelaySeconds(t *testing.T) {
	subject := ValidateDelaySeconds

	t.Run("Accepts bounds and rejects underflow and huge overflow values", func(t *testing.T) {
		d, err := subject(30)
		require.NoError(t, err)
		assert.Equal(t, 30*time.Second, d)

		d, err = subject(MaxDelaySeconds)
		require.NoError(t, err)
		assert.Equal(t, MaxDelay, d)

		_, err = subject(29)
		assert.ErrorIs(t, err, ErrInvalidDelay)

		_, err = subject(MaxDelaySeconds + 1)
		assert.ErrorIs(t, err, ErrInvalidDelay)

		_, err = subject(1 << 50)
		assert.ErrorIs(t, err, ErrInvalidDelay)
	})
}

func TestBuildPrompt(t *testing.T) {
	subject := BuildPrompt

	t.Run("Uses neutral framing and includes NO_REPLY guidance when quiet is true", func(t *testing.T) {
		got := subject(store.Followup{ID: "f-1", Prompt: "Check CI run 99", Quiet: true})
		assert.Contains(t, got, "[Scheduled follow-up f-1]")
		assert.Contains(t, got, "Check CI run 99")
		assert.Contains(t, got, "NO_REPLY")
		assert.NotContains(t, got, "you scheduled earlier")
	})

	t.Run("Omits NO_REPLY hint when quiet is false", func(t *testing.T) {
		got := subject(store.Followup{ID: "f-2", Prompt: "Remind user", Quiet: false})
		assert.Contains(t, got, "[Scheduled follow-up f-2]")
		assert.Contains(t, got, "Remind user")
		assert.NotContains(t, got, "NO_REPLY")
	})
}

func TestScheduler(t *testing.T) {
	subject := New

	t.Run("Schedule validates target, persists due_at, and wakes the scheduler", func(t *testing.T) {
		db := store.NewTestStore(t)
		now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
		var validated bool

		s := subject(Config{
			Store: db,
			Now:   func() time.Time { return now },
			Validate: func(agent, to string) error {
				validated = true
				assert.Equal(t, "support", agent)
				assert.Equal(t, "app:support", to)
				return nil
			},
		})

		created, err := s.Schedule(context.Background(), store.Followup{
			Agent: "support", To: "app:support", Prompt: "Check CI", Quiet: true,
		}, 2*time.Minute)
		require.NoError(t, err)
		assert.True(t, validated)
		assert.Equal(t, now.Add(2*time.Minute), created.DueAt)

		// Validation error prevents persistence.
		sBad := subject(Config{
			Store: db,
			Validate: func(agent, to string) error {
				return errors.New("unknown agent")
			},
		})
		_, err = sBad.Schedule(context.Background(), store.Followup{
			Agent: "ghost", To: "app:ghost", Prompt: "Check CI", Quiet: true,
		}, time.Minute)
		require.ErrorContains(t, err, "unknown agent")
	})

	t.Run("Dispatches overdue follow-ups on startup and marks completed", func(t *testing.T) {
		db := store.NewTestStore(t)
		now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
		var mu sync.Mutex
		var ran []store.Followup

		f, err := db.CreateFollowup(context.Background(), store.Followup{
			Agent: "support", To: "app:support", Prompt: "Check CI", Quiet: true, DueAt: now.Add(-time.Minute),
		})
		require.NoError(t, err)

		s := subject(Config{
			Store:        db,
			PollInterval: time.Hour,
			Now:          func() time.Time { return now },
			RunTurn: func(ctx context.Context, got store.Followup) error {
				mu.Lock()
				ran = append(ran, got)
				mu.Unlock()
				return nil
			},
		})

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			s.Run(ctx)
			close(done)
		}()

		require.Eventually(t, func() bool {
			got, err := db.Followup(context.Background(), f.ID)
			return err == nil && got.State == store.FollowupCompleted
		}, 2*time.Second, 5*time.Millisecond)

		cancel()
		<-done

		mu.Lock()
		require.Len(t, ran, 1)
		assert.Equal(t, f.ID, ran[0].ID)
		mu.Unlock()
	})

	t.Run("Notifies recipient on unhandled failure but skips AlreadyNotified errors", func(t *testing.T) {
		db := store.NewTestStore(t)
		now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)

		f1, err := db.CreateFollowup(context.Background(), store.Followup{
			Agent: "support", To: "app:support", Prompt: "Unhandled fail", Quiet: true, DueAt: now,
		})
		require.NoError(t, err)
		f2, err := db.CreateFollowup(context.Background(), store.Followup{
			Agent: "sales", To: "app:sales", Prompt: "Already notified", Quiet: true, DueAt: now,
		})
		require.NoError(t, err)

		var mu sync.Mutex
		var notices []string
		s := subject(Config{
			Store:        db,
			PollInterval: 50 * time.Millisecond,
			Now:          func() time.Time { return now },
			RunTurn: func(ctx context.Context, got store.Followup) error {
				if got.ID == f1.ID {
					return errors.New("busy queue")
				}
				return AlreadyNotified(errors.New("provider unavailable"))
			},
			Notify: func(ctx context.Context, got store.Followup, text string) error {
				mu.Lock()
				notices = append(notices, got.To+":"+text)
				mu.Unlock()
				return nil
			},
		})

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			s.Run(ctx)
			close(done)
		}()

		require.Eventually(t, func() bool {
			g1, err1 := db.Followup(context.Background(), f1.ID)
			g2, err2 := db.Followup(context.Background(), f2.ID)
			return err1 == nil && err2 == nil && g1.State == store.FollowupFailed && g2.State == store.FollowupFailed
		}, 2*time.Second, 5*time.Millisecond)

		cancel()
		<-done

		mu.Lock()
		require.Len(t, notices, 1)
		assert.Contains(t, notices[0], "app:support:")
		assert.Contains(t, notices[0], "busy queue")
		mu.Unlock()
	})

	t.Run("Interrupt notifies recipients of follow-ups left running before restart", func(t *testing.T) {
		db := store.NewTestStore(t)
		now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)

		f, err := db.CreateFollowup(context.Background(), store.Followup{
			Agent: "support", To: "app:support", Prompt: "Check CI", Quiet: true, DueAt: now,
		})
		require.NoError(t, err)
		_, err = db.ClaimDueFollowups(context.Background(), now, 1)
		require.NoError(t, err)

		var notified []string
		s := subject(Config{
			Store: db,
			Notify: func(ctx context.Context, got store.Followup, text string) error {
				assert.Equal(t, f.ID, got.ID)
				notified = append(notified, text)
				return nil
			},
		})

		require.NoError(t, s.Interrupt(context.Background()))
		require.Len(t, notified, 1)
		assert.Contains(t, notified[0], "interrupted by a gateway restart")
	})

	t.Run("Backs off for PollInterval when Claim fails instead of hot-spinning", func(t *testing.T) {
		db := store.NewTestStore(t)
		now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
		// Past-due row exists so nextWait would return 0 if backoff were missing.
		_, err := db.CreateFollowup(context.Background(), store.Followup{
			Agent: "support", To: "app:support", Prompt: "Check CI", Quiet: true, DueAt: now.Add(-time.Minute),
		})
		require.NoError(t, err)

		var calls atomic.Int32
		firstCall := make(chan struct{})
		s := subject(Config{
			Store:        db,
			PollInterval: 500 * time.Millisecond,
			Now:          func() time.Time { return now },
			Claim: func(ctx context.Context, now time.Time, limit int) ([]store.Followup, error) {
				if calls.Add(1) == 1 {
					close(firstCall)
				}
				return nil, errors.New("temporary db error")
			},
		})

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			s.Run(ctx)
			close(done)
		}()

		<-firstCall
		time.Sleep(60 * time.Millisecond)
		cancel()
		<-done

		assert.Equal(t, int32(1), calls.Load(), "scheduler must wait PollInterval after a claim failure instead of spinning")
	})

	t.Run("Caps concurrent follow-up runs and cancels in-flight turns on shutdown", func(t *testing.T) {
		db := store.NewTestStore(t)
		now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)

		f1, err := db.CreateFollowup(context.Background(), store.Followup{
			Agent: "support", To: "app:support", Prompt: "One", Quiet: true, DueAt: now,
		})
		require.NoError(t, err)
		f2, err := db.CreateFollowup(context.Background(), store.Followup{
			Agent: "sales", To: "app:sales", Prompt: "Two", Quiet: true, DueAt: now,
		})
		require.NoError(t, err)

		started := make(chan string, 2)
		var sawCancel atomic.Bool
		var maxActive atomic.Int32
		var active atomic.Int32

		s := subject(Config{
			Store:         db,
			MaxConcurrent: 1,
			PollInterval:  time.Hour,
			Now:           func() time.Time { return now },
			RunTurn: func(ctx context.Context, got store.Followup) error {
				cur := active.Add(1)
				if cur > maxActive.Load() {
					maxActive.Store(cur)
				}
				defer active.Add(-1)
				started <- got.ID
				<-ctx.Done()
				sawCancel.Store(true)
				return ctx.Err()
			},
		})

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			s.Run(ctx)
			close(done)
		}()

		firstID := <-started
		assert.Equal(t, int32(1), maxActive.Load(), "second due follow-up waits while MaxConcurrent=1 is full")
		cancel()
		<-done

		assert.True(t, sawCancel.Load())
		gotFirst, err := db.Followup(context.Background(), firstID)
		require.NoError(t, err)
		assert.Equal(t, store.FollowupFailed, gotFirst.State)
		assert.Contains(t, gotFirst.Error, "Gateway shut down")

		otherID := f2.ID
		if firstID == f2.ID {
			otherID = f1.ID
		}
		gotOther, err := db.Followup(context.Background(), otherID)
		require.NoError(t, err)
		assert.Equal(t, store.FollowupPending, gotOther.State, "unclaimed follow-up stays pending for next startup")
	})
}
