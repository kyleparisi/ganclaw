package provider

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChainRun(t *testing.T) {
	t.Run("First provider answers", func(t *testing.T) {
		subject := &Chain{Providers: []Provider{
			&Funcs{ProviderName: "codex", RunFunc: func(ctx context.Context, req Request) (Result, error) {
				assert.Equal(t, "c-1", req.Session)
				assert.Equal(t, "hello", req.Prompt)
				return Result{Session: "c-1", Text: "hi from codex"}, nil
			}},
			&Funcs{ProviderName: "claude", RunFunc: func(ctx context.Context, req Request) (Result, error) {
				t.Fatal("claude must not run when codex succeeds")
				return Result{}, nil
			}},
		}}
		sessions := Sessions{"codex": "c-1"}

		used, res, err := subject.Run(context.Background(), sessions, Request{Session: "ignored", Prompt: "hello"})

		require.NoError(t, err)
		assert.Equal(t, "codex", used)
		assert.Equal(t, "hi from codex", res.Text)
		assert.Equal(t, Sessions{"codex": "c-1"}, sessions)
	})

	t.Run("Falls back on unavailable and records both sessions", func(t *testing.T) {
		var logs bytes.Buffer
		subject := &Chain{
			Logger: slog.New(slog.NewTextHandler(&logs, nil)),
			Providers: []Provider{
				&Funcs{ProviderName: "codex", RunFunc: func(ctx context.Context, req Request) (Result, error) {
					assert.Empty(t, req.Session)
					return Result{Session: "c-new"}, errors.Join(ErrUnavailable, errors.New("usage limit"))
				}},
				&Funcs{ProviderName: "claude", RunFunc: func(ctx context.Context, req Request) (Result, error) {
					assert.Equal(t, "k-1", req.Session, "each provider gets its own session")
					assert.Equal(t, "hello", req.Prompt)
					return Result{Session: "k-1", Text: "hi from claude"}, nil
				}},
			},
		}
		sessions := Sessions{"claude": "k-1"}

		used, res, err := subject.Run(context.Background(), sessions, Request{Prompt: "hello"})

		require.NoError(t, err)
		assert.Equal(t, "claude", used)
		assert.Equal(t, "hi from claude", res.Text)
		assert.Equal(t, Sessions{"codex": "c-new", "claude": "k-1"}, sessions)
		assert.Contains(t, logs.String(), "provider unavailable, falling back")
		assert.Contains(t, logs.String(), "usage limit")
		assert.Contains(t, logs.String(), "answered by fallback provider")
	})

	t.Run("OnReset is called only before falling back", func(t *testing.T) {
		var calls []string
		subject := &Chain{Providers: []Provider{
			&Funcs{ProviderName: "codex", RunFunc: func(ctx context.Context, req Request) (Result, error) {
				calls = append(calls, "codex")
				return Result{}, errors.Join(ErrUnavailable, errors.New("dropped"))
			}},
			&Funcs{ProviderName: "claude", RunFunc: func(ctx context.Context, req Request) (Result, error) {
				calls = append(calls, "claude")
				return Result{Text: "ok"}, nil
			}},
		}}

		_, _, err := subject.Run(context.Background(), Sessions{}, Request{OnReset: func() { calls = append(calls, "reset") }})

		require.NoError(t, err)
		assert.Equal(t, []string{"codex", "reset", "claude"}, calls)
	})

	t.Run("Stops on a non-fallback error", func(t *testing.T) {
		badRequest := errors.New("bad request")
		subject := &Chain{Providers: []Provider{
			&Funcs{ProviderName: "codex", RunFunc: func(ctx context.Context, req Request) (Result, error) {
				return Result{}, badRequest
			}},
			&Funcs{ProviderName: "claude", RunFunc: func(ctx context.Context, req Request) (Result, error) {
				t.Fatal("claude must not run after a non-fallback error")
				return Result{}, nil
			}},
		}}

		used, _, err := subject.Run(context.Background(), Sessions{}, Request{Prompt: "hello"})

		assert.Empty(t, used)
		assert.ErrorIs(t, err, badRequest)
		var ce *ChainError
		require.ErrorAs(t, err, &ce)
		assert.Len(t, ce.Attempts, 1)
		assert.Equal(t, "codex: bad request", err.Error())
	})

	t.Run("All providers unavailable", func(t *testing.T) {
		subject := &Chain{Providers: []Provider{
			&Funcs{ProviderName: "codex", RunFunc: func(ctx context.Context, req Request) (Result, error) {
				return Result{}, errors.Join(ErrUnavailable, errors.New("usage limit"))
			}},
			&Funcs{ProviderName: "claude", RunFunc: func(ctx context.Context, req Request) (Result, error) {
				return Result{}, errors.Join(ErrUnavailable, errors.New("rate limited"))
			}},
		}}

		_, _, err := subject.Run(context.Background(), Sessions{}, Request{Prompt: "hello"})

		assert.ErrorIs(t, err, ErrUnavailable)
		var ce *ChainError
		require.ErrorAs(t, err, &ce)
		assert.Equal(t, []string{"codex", "claude"}, attemptNames(ce.Attempts))
		assert.Contains(t, err.Error(), "all providers failed")
	})

	t.Run("Cancelled context does not fall back", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		subject := &Chain{Providers: []Provider{
			&Funcs{ProviderName: "codex", RunFunc: func(ctx context.Context, req Request) (Result, error) {
				cancel()
				return Result{}, errors.Join(ErrUnavailable, ctx.Err())
			}},
			&Funcs{ProviderName: "claude", RunFunc: func(ctx context.Context, req Request) (Result, error) {
				t.Fatal("claude must not run after cancellation")
				return Result{}, nil
			}},
		}}

		_, _, err := subject.Run(ctx, Sessions{}, Request{Prompt: "hello"})

		assert.ErrorIs(t, err, context.Canceled)
	})

	t.Run("No providers configured", func(t *testing.T) {
		subject := &Chain{}

		_, _, err := subject.Run(context.Background(), Sessions{}, Request{Prompt: "hello"})

		assert.EqualError(t, err, "no providers configured")
	})
}

// limitErr is an unavailable error that knows when it clears.
type limitErr struct{ at time.Time }

func (e limitErr) Error() string        { return "usage limit" }
func (e limitErr) Is(target error) bool { return target == ErrUnavailable }
func (e limitErr) RetryAt() time.Time   { return e.at }

func TestChainCooldown(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 9, 30, 4, 0, 0, 0, time.UTC)

	t.Run("Provider is skipped until its limit resets", func(t *testing.T) {
		now := start
		codexCalls, claudeCalls := 0, 0
		subject := &Chain{
			Now: func() time.Time { return now },
			Providers: []Provider{
				&Funcs{ProviderName: "codex", RunFunc: func(ctx context.Context, req Request) (Result, error) {
					codexCalls++
					return Result{}, limitErr{at: start.Add(2 * time.Hour)}
				}},
				&Funcs{ProviderName: "claude", RunFunc: func(ctx context.Context, req Request) (Result, error) {
					claudeCalls++
					return Result{Text: "ok"}, nil
				}},
			},
		}

		for i := 0; i < 3; i++ {
			used, _, err := subject.Run(ctx, Sessions{}, Request{Prompt: "hi"})
			require.NoError(t, err)
			assert.Equal(t, "claude", used)
		}
		assert.Equal(t, 1, codexCalls, "codex is only tried once while its limit is exhausted")

		now = start.Add(2*time.Hour + time.Second)
		_, _, _ = subject.Run(ctx, Sessions{}, Request{Prompt: "hi"})
		assert.Equal(t, 2, codexCalls, "codex is retried after the reset time")
		assert.Equal(t, 4, claudeCalls)
	})

	t.Run("Errors without a reset time use the default cooldown", func(t *testing.T) {
		now := start
		calls := 0
		subject := &Chain{
			Now:             func() time.Time { return now },
			DefaultCooldown: time.Minute,
			Providers: []Provider{
				&Funcs{ProviderName: "codex", RunFunc: func(ctx context.Context, req Request) (Result, error) {
					calls++
					return Result{}, errors.Join(ErrUnavailable, errors.New("connection reset"))
				}},
				&Funcs{ProviderName: "claude", RunFunc: func(ctx context.Context, req Request) (Result, error) {
					return Result{Text: "ok"}, nil
				}},
			},
		}

		subject.Run(ctx, Sessions{}, Request{})
		now = start.Add(59 * time.Second)
		subject.Run(ctx, Sessions{}, Request{})
		assert.Equal(t, 1, calls)
		now = start.Add(61 * time.Second)
		subject.Run(ctx, Sessions{}, Request{})
		assert.Equal(t, 2, calls)
	})

	t.Run("Live status lifts the cooldown early", func(t *testing.T) {
		available := false
		calls := 0
		subject := &Chain{
			Now: func() time.Time { return start },
			Providers: []Provider{
				&StatusFuncs{
					Funcs: Funcs{ProviderName: "codex", RunFunc: func(ctx context.Context, req Request) (Result, error) {
						calls++
						if !available {
							return Result{}, limitErr{at: start.Add(72 * time.Hour)}
						}
						return Result{Text: "codex again"}, nil
					}},
					StatusFunc: func(ctx context.Context) (Status, error) {
						return Status{Known: true, Available: available}, nil
					},
				},
				&Funcs{ProviderName: "claude", RunFunc: func(ctx context.Context, req Request) (Result, error) {
					return Result{Text: "claude"}, nil
				}},
			},
		}

		subject.Run(ctx, Sessions{}, Request{})
		subject.Run(ctx, Sessions{}, Request{})
		assert.Equal(t, 1, calls, "still limited, still skipped")

		available = true // e.g. credits were bought
		used, res, err := subject.Run(ctx, Sessions{}, Request{})

		require.NoError(t, err)
		assert.Equal(t, "codex", used)
		assert.Equal(t, "codex again", res.Text)
		assert.Equal(t, 2, calls)
	})

	t.Run("Providers in cooldown are still tried when nothing else works", func(t *testing.T) {
		codexCalls := 0
		subject := &Chain{
			Now: func() time.Time { return start },
			Providers: []Provider{
				&Funcs{ProviderName: "codex", RunFunc: func(ctx context.Context, req Request) (Result, error) {
					codexCalls++
					if codexCalls == 1 {
						return Result{}, limitErr{at: start.Add(time.Hour)}
					}
					return Result{Text: "codex"}, nil
				}},
				&Funcs{ProviderName: "claude", RunFunc: func(ctx context.Context, req Request) (Result, error) {
					return Result{}, errors.Join(ErrUnavailable, errors.New("rate limited"))
				}},
			},
		}

		_, _, err := subject.Run(ctx, Sessions{}, Request{})
		assert.ErrorIs(t, err, ErrUnavailable)

		// Both are cooling down now; the chain still tries them in order
		// rather than failing without an attempt.
		used, _, err := subject.Run(ctx, Sessions{}, Request{})
		require.NoError(t, err)
		assert.Equal(t, "codex", used)
		assert.Equal(t, 2, codexCalls)
	})

	t.Run("Status reports cooldowns and provider status", func(t *testing.T) {
		subject := &Chain{
			Now: func() time.Time { return start },
			Providers: []Provider{
				&StatusFuncs{
					Funcs: Funcs{ProviderName: "codex", RunFunc: func(ctx context.Context, req Request) (Result, error) {
						return Result{}, limitErr{at: start.Add(time.Hour)}
					}},
					StatusFunc: func(ctx context.Context) (Status, error) {
						return Status{Provider: "ignored", Known: true, Reason: "usage limit reached"}, nil
					},
				},
				&StatusFuncs{
					Funcs:      Funcs{ProviderName: "broken", RunFunc: func(ctx context.Context, req Request) (Result, error) { return Result{Text: "x"}, nil }},
					StatusFunc: func(ctx context.Context) (Status, error) { return Status{}, errors.New("rpc closed") },
				},
				&Funcs{ProviderName: "plain", RunFunc: func(ctx context.Context, req Request) (Result, error) { return Result{Text: "x"}, nil }},
			},
		}
		subject.Run(ctx, Sessions{}, Request{})

		got := subject.Status(ctx)

		require.Len(t, got, 3)
		assert.Equal(t, "codex", got[0].Provider)
		assert.Equal(t, "usage limit reached", got[0].Reason)
		assert.Equal(t, start.Add(time.Hour), got[0].CoolingDownUntil)
		assert.Equal(t, "broken", got[1].Provider)
		assert.True(t, got[1].Known)
		assert.Equal(t, "status check failed: rpc closed", got[1].Reason)
		assert.Equal(t, Status{Provider: "plain"}, got[2])
	})
}
