package provider

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/kyleparisi/ganclaw/internal/logx"
)

// Sessions maps provider name to that provider's session ID for one chat.
// Each backend keeps its own history, so a chat can have one of each.
type Sessions map[string]string

// Chain tries providers in order, moving to the next only when a provider
// returns an error matching ErrUnavailable. A provider that fails that way
// is skipped until its limit resets (from RetryAter) or, failing that, for
// DefaultCooldown — unless its live Status says it has recovered.
type Chain struct {
	Providers       []Provider
	Logger          *slog.Logger
	DefaultCooldown time.Duration    // default 30s
	Now             func() time.Time // default time.Now

	mu       sync.Mutex
	cooldown map[string]time.Time
}

// Attempt records one provider's failure within a Run.
type Attempt struct {
	Provider string
	Err      error
}

// ChainError is returned when every provider failed or a provider failed
// with a non-fallback error.
type ChainError struct {
	Attempts []Attempt
}

func (e *ChainError) Error() string {
	if len(e.Attempts) == 1 {
		return fmt.Sprintf("%s: %v", e.Attempts[0].Provider, e.Attempts[0].Err)
	}
	s := "all providers failed:"
	for _, a := range e.Attempts {
		s += fmt.Sprintf(" [%s: %v]", a.Provider, a.Err)
	}
	return s
}

func (e *ChainError) Unwrap() []error {
	errs := make([]error, len(e.Attempts))
	for i, a := range e.Attempts {
		errs[i] = a.Err
	}
	return errs
}

// Run sends req to the first provider that succeeds. req.Session is ignored;
// each provider's session comes from sessions, which is updated in place
// with any session a provider returns (even on failure). It returns the name
// of the provider that answered.
func (c *Chain) Run(ctx context.Context, sessions Sessions, req Request) (string, Result, error) {
	log := logx.OrDiscard(c.Logger)
	if len(c.Providers) == 0 {
		return "", Result{}, errors.New("no providers configured")
	}

	// Providers still cooling down go to the back of the line: they are
	// only tried if everything else fails too.
	var ready, cooling []Provider
	for _, p := range c.Providers {
		if c.coolingDown(ctx, p) {
			cooling = append(cooling, p)
		} else {
			ready = append(ready, p)
		}
	}
	if len(cooling) > 0 && len(ready) > 0 {
		log.Debug("skipping providers in cooldown", "skipped", names(cooling))
	}
	order := append(ready, cooling...)

	var attempts []Attempt
	for i, p := range order {
		if i > 0 && req.OnReset != nil {
			req.OnReset()
		}
		r := req
		r.Session = sessions[p.Name()]
		res, err := p.Run(ctx, r)
		if res.Session != "" {
			sessions[p.Name()] = res.Session
		}
		if err == nil {
			c.clearCooldown(p.Name())
			if len(attempts) > 0 {
				log.Info("answered by fallback provider", "provider", p.Name(), "failed", attemptNames(attempts))
			}
			return p.Name(), res, nil
		}
		attempts = append(attempts, Attempt{Provider: p.Name(), Err: err})
		if ctx.Err() != nil || !errors.Is(err, ErrUnavailable) {
			break
		}
		until := c.setCooldown(p.Name(), err)
		if i+1 < len(order) {
			log.Warn("provider unavailable, falling back", "provider", p.Name(), "next", order[i+1].Name(), "skip_until", until, "err", err)
		} else {
			log.Warn("provider unavailable", "provider", p.Name(), "skip_until", until, "err", err)
		}
		// Only fall through to cooling providers if nothing ready is left.
		if i+1 == len(ready) && len(ready) < len(order) {
			log.Info("all ready providers failed, trying providers in cooldown", "providers", names(cooling))
		}
	}
	return "", Result{}, &ChainError{Attempts: attempts}
}

// Status reports every provider's status, including chain cooldowns.
// Providers that can't report get a Known=false entry.
func (c *Chain) Status(ctx context.Context) []Status {
	out := make([]Status, 0, len(c.Providers))
	for _, p := range c.Providers {
		s := Status{Provider: p.Name()}
		if sp, ok := p.(Statuser); ok {
			if st, err := sp.Status(ctx); err != nil {
				s.Known = true
				s.Reason = "status check failed: " + err.Error()
			} else {
				s = st
				s.Provider = p.Name()
			}
		}
		c.mu.Lock()
		s.CoolingDownUntil = c.cooldown[p.Name()]
		c.mu.Unlock()
		out = append(out, s)
	}
	return out
}

func (c *Chain) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// coolingDown reports whether p should be skipped. A provider whose live
// status says it's available again leaves cooldown immediately.
func (c *Chain) coolingDown(ctx context.Context, p Provider) bool {
	c.mu.Lock()
	until, ok := c.cooldown[p.Name()]
	c.mu.Unlock()
	if !ok || !c.now().Before(until) {
		return false
	}
	if sp, isStatuser := p.(Statuser); isStatuser {
		sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		st, err := sp.Status(sctx)
		cancel()
		if err == nil && st.Known && st.Available {
			logx.OrDiscard(c.Logger).Info("provider recovered before cooldown ended", "provider", p.Name())
			c.clearCooldown(p.Name())
			return false
		}
	}
	return true
}

func (c *Chain) setCooldown(name string, err error) time.Time {
	d := c.DefaultCooldown
	if d == 0 {
		d = 30 * time.Second
	}
	until := c.now().Add(d)
	var ra RetryAter
	if errors.As(err, &ra) {
		if t := ra.RetryAt(); t.After(c.now()) {
			until = t
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cooldown == nil {
		c.cooldown = map[string]time.Time{}
	}
	c.cooldown[name] = until
	return until
}

func (c *Chain) clearCooldown(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.cooldown, name)
}

func names(ps []Provider) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Name()
	}
	return out
}

func attemptNames(as []Attempt) []string {
	names := make([]string, len(as))
	for i, a := range as {
		names[i] = a.Provider
	}
	return names
}

// Funcs is a Provider built from function fields. Nil RunFunc panics;
// nil CloseFunc is a no-op. See StatusFuncs for one that reports status.
type Funcs struct {
	ProviderName string
	RunFunc      func(ctx context.Context, req Request) (Result, error)
	CloseFunc    func() error
}

func (f *Funcs) Name() string { return f.ProviderName }

func (f *Funcs) Run(ctx context.Context, req Request) (Result, error) { return f.RunFunc(ctx, req) }

func (f *Funcs) Close() error {
	if f.CloseFunc == nil {
		return nil
	}
	return f.CloseFunc()
}

// StatusFuncs is a Funcs that also reports status.
type StatusFuncs struct {
	Funcs
	StatusFunc func(ctx context.Context) (Status, error)
}

func (f *StatusFuncs) Status(ctx context.Context) (Status, error) { return f.StatusFunc(ctx) }
