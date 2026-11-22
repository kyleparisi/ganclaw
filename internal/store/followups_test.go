package store

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFollowups(t *testing.T) {
	subject := (*Store).CreateFollowup
	ctx := context.Background()

	t.Run("Creates, lists in due order, and retrieves a follow-up", func(t *testing.T) {
		db := NewTestStore(t)
		base := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)

		later, err := subject(db, ctx, Followup{
			Agent: "support", To: "app:support", Prompt: "Check deploy", Quiet: true, DueAt: base.Add(10 * time.Minute),
		})
		require.NoError(t, err)
		sooner, err := subject(db, ctx, Followup{
			Agent: "support", To: "telegram:support:111", Prompt: "Check CI run 42", Quiet: true, DueAt: base.Add(2 * time.Minute),
		})
		require.NoError(t, err)
		other, err := subject(db, ctx, Followup{
			Agent: "sales", To: "app:sales", Prompt: "Check lead", Quiet: false, DueAt: base.Add(1 * time.Minute),
		})
		require.NoError(t, err)

		got, err := db.Followup(ctx, sooner.ID)
		require.NoError(t, err)
		assert.Equal(t, sooner.ID, got.ID)
		assert.Equal(t, FollowupPending, got.State)

		supportList, err := db.ListFollowups(ctx, "support", false)
		require.NoError(t, err)
		require.Len(t, supportList, 2)
		assert.Equal(t, sooner.ID, supportList[0].ID)
		assert.Equal(t, later.ID, supportList[1].ID)

		nextDue, ok, err := db.NextFollowupDue(ctx)
		require.NoError(t, err)
		assert.True(t, ok)
		assert.Equal(t, other.DueAt, nextDue)
	})

	t.Run("Claims due follow-ups once and transitions through finish with valid UTF-8 error truncation", func(t *testing.T) {
		db := NewTestStore(t)
		base := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)

		due, err := subject(db, ctx, Followup{
			Agent: "support", To: "app:support", Prompt: "Check CI", Quiet: true, DueAt: base,
		})
		require.NoError(t, err)
		_, err = subject(db, ctx, Followup{
			Agent: "support", To: "app:support", Prompt: "Later", Quiet: true, DueAt: base.Add(5 * time.Minute),
		})
		require.NoError(t, err)

		claimed, err := db.ClaimDueFollowups(ctx, base.Add(time.Second), 0)
		require.NoError(t, err)
		require.Len(t, claimed, 1)
		assert.Equal(t, due.ID, claimed[0].ID)
		assert.Equal(t, FollowupRunning, claimed[0].State)
		assert.NotNil(t, claimed[0].FiredAt)

		again, err := db.ClaimDueFollowups(ctx, base.Add(2*time.Second), 0)
		require.NoError(t, err)
		assert.Empty(t, again, "already claimed follow-ups are not claimed twice")

		longErr := strings.Repeat("é", 300)
		require.NoError(t, db.FinishFollowup(ctx, due.ID, FollowupFailed, longErr))
		finished, err := db.Followup(ctx, due.ID)
		require.NoError(t, err)
		assert.Equal(t, FollowupFailed, finished.State)
		assert.NotNil(t, finished.FinishedAt)
		assert.True(t, utf8.ValidString(finished.Error))
		assert.LessOrEqual(t, len(finished.Error), 500)

		// Terminal states are not overwritten.
		require.NoError(t, db.FinishFollowup(ctx, due.ID, FollowupCompleted, ""))
		stillFailed, err := db.Followup(ctx, due.ID)
		require.NoError(t, err)
		assert.Equal(t, FollowupFailed, stillFailed.State)
	})

	t.Run("Corrupt due row is marked failed without blocking valid due rows", func(t *testing.T) {
		db := NewTestStore(t)
		base := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)

		_, err := db.db.ExecContext(ctx,
			`INSERT INTO followups (id, agent, state, due_unix_ms, created_unix_ms, updated_unix_ms, data) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			"corrupt-1", "support", FollowupPending, base.UnixMilli(), base.UnixMilli(), base.UnixMilli(), []byte("{not-json"))
		require.NoError(t, err)

		valid, err := subject(db, ctx, Followup{
			Agent: "support", To: "app:support", Prompt: "Still runs", Quiet: true, DueAt: base.Add(time.Second),
		})
		require.NoError(t, err)

		claimed, err := db.ClaimDueFollowups(ctx, base.Add(2*time.Second), 0)
		require.NoError(t, err)
		require.Len(t, claimed, 1)
		assert.Equal(t, valid.ID, claimed[0].ID)

		corrupt, err := db.Followup(ctx, "corrupt-1")
		require.NoError(t, err)
		assert.Equal(t, FollowupFailed, corrupt.State)
		assert.Contains(t, corrupt.Error, "corrupt")

		_, hasPending, err := db.NextFollowupDue(ctx)
		require.NoError(t, err)
		assert.False(t, hasPending, "corrupt row does not remain pending")
	})

	t.Run("Cancelling pending follow-up works and rejects non-pending or missing", func(t *testing.T) {
		db := NewTestStore(t)
		base := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)

		f, err := subject(db, ctx, Followup{
			Agent: "support", To: "app:support", Prompt: "Check CI", Quiet: true, DueAt: base.Add(time.Minute),
		})
		require.NoError(t, err)

		cancelled, err := db.CancelFollowup(ctx, f.ID)
		require.NoError(t, err)
		assert.Equal(t, FollowupCancelled, cancelled.State)
		assert.NotNil(t, cancelled.FinishedAt)

		_, err = db.CancelFollowup(ctx, f.ID)
		assert.ErrorIs(t, err, ErrFollowupNotPending)

		_, err = db.CancelFollowup(ctx, "missing")
		assert.ErrorIs(t, err, sql.ErrNoRows)

		claimed, err := db.ClaimDueFollowups(ctx, base.Add(2*time.Minute), 0)
		require.NoError(t, err)
		assert.Empty(t, claimed, "cancelled follow-ups never fire")
	})

	t.Run("InterruptFollowups fails running items, returns them, and keeps pending ones", func(t *testing.T) {
		db := NewTestStore(t)
		base := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)

		running, err := subject(db, ctx, Followup{
			Agent: "support", To: "app:support", Prompt: "Running", Quiet: true, DueAt: base,
		})
		require.NoError(t, err)
		pending, err := subject(db, ctx, Followup{
			Agent: "support", To: "app:support", Prompt: "Pending", Quiet: true, DueAt: base.Add(time.Hour),
		})
		require.NoError(t, err)

		_, err = db.ClaimDueFollowups(ctx, base.Add(time.Second), 0)
		require.NoError(t, err)

		interrupted, err := db.InterruptFollowups(ctx)
		require.NoError(t, err)
		require.Len(t, interrupted, 1)
		assert.Equal(t, running.ID, interrupted[0].ID)
		assert.Equal(t, "app:support", interrupted[0].To)

		gotRunning, err := db.Followup(ctx, running.ID)
		require.NoError(t, err)
		assert.Equal(t, FollowupFailed, gotRunning.State)
		assert.Contains(t, gotRunning.Error, "Gateway restarted")

		gotPending, err := db.Followup(ctx, pending.ID)
		require.NoError(t, err)
		assert.Equal(t, FollowupPending, gotPending.State)
	})

	t.Run("Enforces validation, per-agent pending cap, and hourly rate limit", func(t *testing.T) {
		db := NewTestStore(t)
		due := time.Now().UTC().Add(time.Minute)

		_, err := subject(db, ctx, Followup{Agent: "", To: "app:support", Prompt: "hi", DueAt: due})
		assert.ErrorIs(t, err, ErrInvalidFollowup)

		_, err = subject(db, ctx, Followup{Agent: "support", To: "app:support", Prompt: "hi"})
		assert.ErrorIs(t, err, ErrInvalidFollowup)

		for range MaxPendingFollowupsPerAgent {
			_, err := subject(db, ctx, Followup{Agent: "support", To: "app:support", Prompt: "check", DueAt: due})
			require.NoError(t, err)
		}
		_, err = subject(db, ctx, Followup{Agent: "support", To: "app:support", Prompt: "one too many", DueAt: due})
		assert.ErrorIs(t, err, ErrTooManyFollowups)

		// Even after cancelling one pending item, the hourly rate limit stops infinite self-rescheduling.
		list, err := db.ListFollowups(ctx, "support", false)
		require.NoError(t, err)
		_, err = db.CancelFollowup(ctx, list[0].ID)
		require.NoError(t, err)
		_, err = subject(db, ctx, Followup{Agent: "support", To: "app:support", Prompt: "chain hop", DueAt: due})
		assert.ErrorIs(t, err, ErrFollowupRateLimit)

		// Another agent still has its own budget.
		_, err = subject(db, ctx, Followup{Agent: "sales", To: "app:sales", Prompt: "ok", DueAt: due})
		require.NoError(t, err)
	})

	t.Run("PruneFollowups removes old terminal items and keeps pending ones", func(t *testing.T) {
		db := NewTestStore(t)
		due := time.Now().UTC().Add(time.Minute)

		done, err := subject(db, ctx, Followup{Agent: "support", To: "app:support", Prompt: "done", DueAt: due})
		require.NoError(t, err)
		_, err = db.CancelFollowup(ctx, done.ID)
		require.NoError(t, err)

		pending, err := subject(db, ctx, Followup{Agent: "support", To: "app:support", Prompt: "pending", DueAt: due})
		require.NoError(t, err)

		// Prune with negative duration treats all terminal items as older than cutoff.
		n, err := db.PruneFollowups(ctx, -time.Minute)
		require.NoError(t, err)
		assert.Equal(t, int64(1), n)

		_, err = db.Followup(ctx, done.ID)
		assert.ErrorIs(t, err, sql.ErrNoRows)
		got, err := db.Followup(ctx, pending.ID)
		require.NoError(t, err)
		assert.Equal(t, FollowupPending, got.State)
	})
}
