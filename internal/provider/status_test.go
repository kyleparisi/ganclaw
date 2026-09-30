package provider

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestFormatStatus(t *testing.T) {
	subject := FormatStatus
	now := time.Date(2026, 9, 30, 4, 0, 0, 0, time.UTC)

	t.Run("Unavailable provider shows reason, reset and windows", func(t *testing.T) {
		got := subject([]Status{{
			Provider: "codex", Known: true, Available: false,
			Reason:           "usage limit reached, plus plan",
			ResetsAt:         now.Add(3*24*time.Hour + 13*time.Hour + 20*time.Minute),
			Windows:          []Window{{Name: "weekly", UsedPercent: 100, ResetsAt: now.Add(85 * time.Hour)}},
			CoolingDownUntil: now.Add(90 * time.Minute),
		}}, now)

		assert.Equal(t, "codex: unavailable — usage limit reached, plus plan (resets Oct 3 17:20 UTC, in 3d 13h)\n"+
			"  weekly: 100% used, resets Oct 3 17:00 UTC, in 3d 13h\n"+
			"  skipped until Sep 30 05:30 UTC, in 1h 30m", got)
	})

	t.Run("Available and unknown providers", func(t *testing.T) {
		got := subject([]Status{
			{Provider: "claude", Known: true, Available: true, Windows: []Window{
				{Name: "5-hour", UsedPercent: 18.4, ResetsAt: now.Add(30 * time.Minute)},
				{Name: "weekly", UsedPercent: 17},
			}},
			{Provider: "other", Reason: "no limit data until a turn runs"},
		}, now)

		assert.Equal(t, "claude: available\n"+
			"  5-hour: 18% used, resets Sep 30 04:30 UTC, in 30m\n"+
			"  weekly: 17% used\n"+
			"\n"+
			"other: unknown — no limit data until a turn runs", got)
	})

	t.Run("Past times and expired cooldowns", func(t *testing.T) {
		got := subject([]Status{{
			Provider: "codex", Known: true, Available: false,
			ResetsAt:         now.Add(-time.Minute),
			CoolingDownUntil: now.Add(-time.Minute),
		}}, now)

		assert.Equal(t, "codex: unavailable (resets Sep 30 03:59 UTC)", got)
	})
}

func TestWindowName(t *testing.T) {
	subject := WindowName

	t.Run("Names common window lengths", func(t *testing.T) {
		assert.Equal(t, "5-hour", subject(5*time.Hour))
		assert.Equal(t, "daily", subject(24*time.Hour))
		assert.Equal(t, "weekly", subject(7*24*time.Hour))
		assert.Equal(t, "3-hour", subject(3*time.Hour))
		assert.Equal(t, "90-min", subject(90*time.Minute))
		assert.Equal(t, "window", subject(0))
	})
}
