package api

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kyleparisi/ganclaw/internal/config"
	"github.com/kyleparisi/ganclaw/internal/store"
)

func TestFollowupsAPI(t *testing.T) {
	subject := serve

	t.Run("Schedules with default quiet, lists, and cancels follow-ups", func(t *testing.T) {
		db := store.NewTestStore(t)
		resolver := Resolver{
			Contacts: []config.Contact{{Name: "alex", TelegramChat: 111, DefaultBot: "alerts"}},
			Bots:     []string{"alerts", "support"},
		}
		client := subject(t, &Server{
			Store:    db,
			Resolve:  resolver.Resolve,
			AgentBot: func(agent string) string { return "support" },
			ScheduleFollowup: func(ctx context.Context, req FollowupRequest, target Target) (store.Followup, error) {
				require.NotNil(t, req.Quiet)
				assert.True(t, *req.Quiet)
				assert.Equal(t, "telegram:support:111", target.String())
				return db.CreateFollowup(ctx, store.Followup{
					Agent:   req.Agent,
					To:      target.String(),
					Session: req.Session,
					Prompt:  req.Prompt,
					Quiet:   *req.Quiet,
					DueAt:   time.Now().UTC().Add(time.Duration(req.DelaySeconds) * time.Second),
				})
			},
			Followups:      db.ListFollowups,
			CancelFollowup: db.CancelFollowup,
		})
		ctx := context.Background()

		created, err := client.ScheduleFollowup(ctx, FollowupRequest{
			Agent:        "support",
			To:           "alex",
			Prompt:       "Check CI run 42",
			DelaySeconds: 120,
		})
		require.NoError(t, err)
		assert.Equal(t, store.FollowupPending, created.State)
		assert.Equal(t, "telegram:support:111", created.To)
		assert.True(t, created.Quiet)

		pending, err := client.Followups(ctx, "support", false)
		require.NoError(t, err)
		require.Len(t, pending, 1)
		assert.Equal(t, created.ID, pending[0].ID)

		cancelled, err := client.CancelFollowup(ctx, created.ID)
		require.NoError(t, err)
		assert.Equal(t, store.FollowupCancelled, cancelled.State)

		pending, err = client.Followups(ctx, "support", false)
		require.NoError(t, err)
		assert.Empty(t, pending)

		all, err := client.Followups(ctx, "support", true)
		require.NoError(t, err)
		require.Len(t, all, 1)
		assert.Equal(t, store.FollowupCancelled, all[0].State)
	})

	t.Run("Rejects invalid delay, missing fields, and maps conflict and not-found errors", func(t *testing.T) {
		db := store.NewTestStore(t)
		client := subject(t, &Server{
			Store:   db,
			Resolve: Resolver{Bots: []string{"support"}}.Resolve,
			ScheduleFollowup: func(ctx context.Context, req FollowupRequest, target Target) (store.Followup, error) {
				return db.CreateFollowup(ctx, store.Followup{
					Agent:  req.Agent,
					To:     target.String(),
					Prompt: req.Prompt,
					Quiet:  *req.Quiet,
					DueAt:  time.Now().UTC().Add(time.Duration(req.DelaySeconds) * time.Second),
				})
			},
			Followups:      db.ListFollowups,
			CancelFollowup: db.CancelFollowup,
		})
		ctx := context.Background()

		_, err := client.ScheduleFollowup(ctx, FollowupRequest{Agent: "support", To: "app:support", Prompt: "too soon", DelaySeconds: 5})
		var ae *Error
		require.ErrorAs(t, err, &ae)
		assert.Equal(t, CodeBadRequest, ae.Code)

		_, err = client.ScheduleFollowup(ctx, FollowupRequest{Agent: "support", To: "app:support", Prompt: "overflow", DelaySeconds: 1 << 50})
		require.ErrorAs(t, err, &ae)
		assert.Equal(t, CodeBadRequest, ae.Code)

		_, err = client.CancelFollowup(ctx, "does-not-exist")
		require.ErrorAs(t, err, &ae)
		assert.Equal(t, CodeNotFound, ae.Code)

		f, err := client.ScheduleFollowup(ctx, FollowupRequest{Agent: "support", To: "app:support", Prompt: "check", DelaySeconds: 60, IdempotencyKey: "ci-42"})
		require.NoError(t, err)
		replayed, err := client.ScheduleFollowup(ctx, FollowupRequest{Agent: "support", To: "app:support", Prompt: "check", DelaySeconds: 60, IdempotencyKey: "ci-42"})
		require.NoError(t, err)
		assert.Equal(t, f.ID, replayed.ID, "idempotency key prevents duplicate scheduling")

		_, err = client.CancelFollowup(ctx, f.ID)
		require.NoError(t, err)

		_, err = client.CancelFollowup(ctx, f.ID)
		require.ErrorAs(t, err, &ae)
		assert.Equal(t, CodeConflict, ae.Code)
	})

	t.Run("Maps rate limit to 429 busy and nil callbacks to unavailable", func(t *testing.T) {
		client := subject(t, &Server{
			Resolve: Resolver{Bots: []string{"support"}}.Resolve,
			ScheduleFollowup: func(ctx context.Context, req FollowupRequest, target Target) (store.Followup, error) {
				return store.Followup{}, store.ErrFollowupRateLimit
			},
		})
		ctx := context.Background()

		var ae *Error
		_, err := client.ScheduleFollowup(ctx, FollowupRequest{Agent: "support", To: "app:support", Prompt: "check", DelaySeconds: 60})
		require.ErrorAs(t, err, &ae)
		assert.Equal(t, CodeBusy, ae.Code)

		_, err = client.Followups(ctx, "", false)
		require.ErrorAs(t, err, &ae)
		assert.Equal(t, CodeUnavailable, ae.Code)

		_, err = client.CancelFollowup(ctx, "f-1")
		require.ErrorAs(t, err, &ae)
		assert.Equal(t, CodeUnavailable, ae.Code)
	})
}
