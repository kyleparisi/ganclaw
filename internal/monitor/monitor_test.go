package monitor

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kyleparisi/ganclaw/internal/api"
	"github.com/kyleparisi/ganclaw/internal/provider"
)

var (
	t0       = time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	settings = Settings{BotStaleAfter: 5 * time.Minute, ProbeGrace: map[string]time.Duration{"tunnel": 30 * time.Minute}, RemindEvery: 6 * time.Hour}
)

func healthy() api.HealthResponse {
	return api.HealthResponse{
		Providers: []provider.Status{{Provider: "codex", Known: true, Reason: "usage limit reached", ResetsAt: t0.Add(72 * time.Hour)}, {Provider: "claude", Known: true, Available: true}},
		Bots:      []api.BotHealth{{Name: "support", Running: true, LastPoll: t0.Add(-time.Minute)}},
		Probes:    []api.ProbeResult{{Name: "tunnel", OK: true}},
	}
}

func TestObserve(t *testing.T) {
	subject := Observe

	t.Run("Healthy gateway has no problems, even with one provider limited", func(t *testing.T) {
		assert.Empty(t, subject(healthy(), nil, t0, settings))
	})

	t.Run("Unreachable API is the only problem reported", func(t *testing.T) {
		got := subject(healthy(), errors.New("connection refused"), t0, settings)

		assert.Equal(t, []Observation{{Key: "api", Message: "ganclaw isn't responding: connection refused"}}, got)
	})

	t.Run("All providers down, stale and stopped bots, failing probe", func(t *testing.T) {
		h := healthy()
		h.Providers[1] = provider.Status{Provider: "claude", Known: true, Reason: "rate limited (weekly)"}
		h.Bots = []api.BotHealth{
			{Name: "stale", Running: true, LastPoll: t0.Add(-10 * time.Minute), LastErr: "telegram getUpdates: 502 Bad Gateway"},
			{Name: "stopped", Running: false, LastErr: "telegram getMe: 401 Unauthorized"},
			{Name: "fresh", Running: true, LastPoll: t0.Add(-time.Minute)},
		}
		h.Probes = []api.ProbeResult{{Name: "tunnel", Error: "connection refused"}}

		got := subject(h, nil, t0, settings)

		require.Len(t, got, 4)
		assert.Equal(t, "providers", got[0].Key)
		assert.Contains(t, got[0].Message, "codex (usage limit reached, resets Oct 3 12:00 UTC); claude (rate limited (weekly))")
		assert.Equal(t, Observation{Key: "bot:stale", Message: "Bot stale hasn't reached Telegram since 11:50 UTC: telegram getUpdates: 502 Bad Gateway"}, got[1])
		assert.Equal(t, Observation{Key: "bot:stopped", Message: "Bot stopped is not running: telegram getMe: 401 Unauthorized"}, got[2])
		assert.Equal(t, Observation{Key: "probe:tunnel", Message: "tunnel is unreachable: connection refused", Grace: 30 * time.Minute}, got[3])
	})
}

func TestStep(t *testing.T) {
	subject := Step
	api := Observation{Key: "api", Message: "ganclaw isn't responding"}

	t.Run("New problem alerts once, then stays quiet", func(t *testing.T) {
		st, lines := subject(State{Problems: map[string]*Tracked{}}, []Observation{api}, t0, settings)
		assert.Equal(t, []string{"⚠️ ganclaw isn't responding"}, lines)

		st, lines = subject(st, []Observation{api}, t0.Add(5*time.Minute), settings)
		assert.Empty(t, lines)
		assert.Equal(t, t0, st.Problems["api"].FirstSeen)
	})

	t.Run("Ongoing problems are reminded about", func(t *testing.T) {
		st, _ := subject(State{Problems: map[string]*Tracked{}}, []Observation{api}, t0, settings)

		_, lines := subject(st, []Observation{api}, t0.Add(6*time.Hour), settings)

		assert.Equal(t, []string{"⚠️ Still: ganclaw isn't responding (since Sep 30 12:00 UTC)"}, lines)
	})

	t.Run("Grace delays the first alert", func(t *testing.T) {
		tunnel := Observation{Key: "probe:tunnel", Message: "tunnel is unreachable", Grace: 30 * time.Minute}

		st, lines := subject(State{Problems: map[string]*Tracked{}}, []Observation{tunnel}, t0, settings)
		assert.Empty(t, lines)
		st, lines = subject(st, []Observation{tunnel}, t0.Add(29*time.Minute), settings)
		assert.Empty(t, lines)
		_, lines = subject(st, []Observation{tunnel}, t0.Add(30*time.Minute), settings)
		assert.Equal(t, []string{"⚠️ tunnel is unreachable"}, lines)
	})

	t.Run("Cleared problems say so only if they were reported", func(t *testing.T) {
		brief := Observation{Key: "probe:tunnel", Message: "tunnel is unreachable", Grace: 30 * time.Minute}
		st, _ := subject(State{Problems: map[string]*Tracked{}}, []Observation{api, brief}, t0, settings)

		st, lines := subject(st, nil, t0.Add(5*time.Minute), settings)

		assert.Equal(t, []string{"✅ Resolved: ganclaw isn't responding"}, lines, "the unreported blip stays silent")
		assert.Empty(t, st.Problems)
	})
}

func TestLoadSave(t *testing.T) {
	t.Run("Round trip, and missing or corrupt files start empty", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "sub", "state.json")
		st, err := Load(path)
		require.NoError(t, err)
		assert.Empty(t, st.Problems)

		st.Problems["api"] = &Tracked{Message: "down", FirstSeen: t0, NotifiedAt: t0}
		require.NoError(t, Save(path, st))
		got, err := Load(path)
		require.NoError(t, err)
		assert.Equal(t, "down", got.Problems["api"].Message)
		assert.True(t, got.Problems["api"].NotifiedAt.Equal(t0))
	})
}
