// Package followup runs persisted one-shot delayed checkbacks so agents can
// wake up after external work (such as CI runs or builds) finishes.
package followup

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kyleparisi/ganclaw/internal/logx"
	"github.com/kyleparisi/ganclaw/internal/store"
)

const (
	// MinDelay prevents tight busy-polling loops.
	MinDelay = 30 * time.Second
	// MaxDelay bounds how far ahead a one-shot checkback can be scheduled.
	MaxDelay = 7 * 24 * time.Hour

	MinDelaySeconds = int(MinDelay / time.Second)
	MaxDelaySeconds = int(MaxDelay / time.Second)

	defaultPollInterval  = 5 * time.Second
	defaultMaxConcurrent = 4
)

var ErrInvalidDelay = errors.New("delay must be between 30s and 168h (7d)")

// notifiedError marks a RunTurn error that has already been reported to the
// recipient chat (e.g. by the router), so the scheduler won't double-notify.
type notifiedError struct {
	err error
}

func (e notifiedError) Error() string { return e.err.Error() }
func (e notifiedError) Unwrap() error { return e.err }

// AlreadyNotified wraps err to indicate the user was already notified in chat.
func AlreadyNotified(err error) error {
	if err == nil {
		return nil
	}
	return notifiedError{err: err}
}

// ParseDelay parses a positive duration string like "30s", "2m", "1h30m", or "2d".
func ParseDelay(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, ErrInvalidDelay
	}
	var total time.Duration
	if idx := strings.IndexByte(s, 'd'); idx != -1 {
		days, err := strconv.Atoi(s[:idx])
		if err != nil || days < 0 || days > 7 {
			return 0, ErrInvalidDelay
		}
		total = time.Duration(days) * 24 * time.Hour
		s = s[idx+1:]
	}
	if s != "" {
		d, err := time.ParseDuration(s)
		if err != nil || d < 0 {
			return 0, ErrInvalidDelay
		}
		total += d
	}
	if err := ValidateDelay(total); err != nil {
		return 0, err
	}
	return total, nil
}

// ValidateDelay checks that d is within the supported follow-up bounds.
func ValidateDelay(d time.Duration) error {
	if d < MinDelay || d > MaxDelay {
		return ErrInvalidDelay
	}
	return nil
}

// ValidateDelaySeconds validates an integer second count without duration overflow.
func ValidateDelaySeconds(seconds int) (time.Duration, error) {
	if seconds < MinDelaySeconds || seconds > MaxDelaySeconds {
		return 0, ErrInvalidDelay
	}
	return time.Duration(seconds) * time.Second, nil
}

// BuildPrompt wraps a scheduled follow-up's prompt with neutral checkback context.
func BuildPrompt(f store.Followup) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[Scheduled follow-up %s]\n%s\n\n", f.ID, f.Prompt)
	if f.Quiet {
		b.WriteString("A scheduled checkback triggered with the note above. Check the current status and report the outcome to the user. If the work is still in progress, you may schedule another follow-up (stop polling and inform the user if it appears stuck or has waited too long) and reply NO_REPLY to stay silent.")
	} else {
		b.WriteString("A scheduled checkback triggered with the note above. Check the current status and report the outcome to the user.")
	}
	return b.String()
}

// Config configures a follow-up Scheduler.
type Config struct {
	Store *store.Store
	// Validate checks that a follow-up's agent and resolved destination are valid
	// before persisting. Optional.
	Validate func(agent, to string) error
	// RunTurn executes a claimed follow-up. Returning an error marks the
	// follow-up as failed; wrap with AlreadyNotified if the chat was already informed.
	RunTurn func(ctx context.Context, f store.Followup) error
	// Notify delivers a short failure notice to the follow-up's recipient when
	// a follow-up fails before the router can reply or is interrupted by restart.
	// Optional.
	Notify func(ctx context.Context, f store.Followup, text string) error
	// Claim overrides Store.ClaimDueFollowups for tests. Optional.
	Claim func(ctx context.Context, now time.Time, limit int) ([]store.Followup, error)
	// MaxConcurrent bounds simultaneous follow-up turns; default 4.
	MaxConcurrent int
	// PollInterval is the maximum sleep between store checks; default 5s.
	PollInterval time.Duration
	// Now defaults to time.Now.
	Now    func() time.Time
	Logger *slog.Logger
}

// Scheduler dispatches persisted follow-ups as they come due.
type Scheduler struct {
	cfg       Config
	log       *slog.Logger
	wake      chan struct{}
	slotFreed chan struct{}
	sem       chan struct{}
	wg        sync.WaitGroup
}

// New returns a Scheduler ready to be started with Run.
func New(cfg Config) *Scheduler {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = defaultPollInterval
	}
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = defaultMaxConcurrent
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Claim == nil && cfg.Store != nil {
		cfg.Claim = cfg.Store.ClaimDueFollowups
	}
	return &Scheduler{
		cfg:       cfg,
		log:       logx.OrDiscard(cfg.Logger),
		wake:      make(chan struct{}, 1),
		slotFreed: make(chan struct{}, 1),
		sem:       make(chan struct{}, cfg.MaxConcurrent),
	}
}

// Schedule validates, persists, and wakes the scheduler for a new follow-up.
func (s *Scheduler) Schedule(ctx context.Context, f store.Followup, delay time.Duration) (store.Followup, error) {
	if err := ValidateDelay(delay); err != nil {
		return store.Followup{}, err
	}
	if s.cfg.Validate != nil {
		if err := s.cfg.Validate(f.Agent, f.To); err != nil {
			return store.Followup{}, err
		}
	}
	f.DueAt = s.cfg.Now().UTC().Add(delay)
	created, err := s.cfg.Store.CreateFollowup(ctx, f)
	if err != nil {
		return store.Followup{}, err
	}
	s.Wake()
	return created, nil
}

// Interrupt marks follow-ups left running by a previous process as failed and
// notifies their recipients (best effort). Call once at startup before Run.
func (s *Scheduler) Interrupt(ctx context.Context) error {
	interrupted, err := s.cfg.Store.InterruptFollowups(ctx)
	if err != nil {
		return err
	}
	for _, f := range interrupted {
		s.notify(ctx, f, fmt.Sprintf("Scheduled checkback for %s was interrupted by a gateway restart and was not retried.", f.Agent))
	}
	return nil
}

// Wake nudges the scheduler to re-check the next due follow-up immediately.
func (s *Scheduler) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Run polls for due follow-ups until ctx is cancelled, then waits for any
// in-flight follow-up dispatches to finish.
func (s *Scheduler) Run(ctx context.Context) {
	defer s.wg.Wait()
	for {
		if ctx.Err() != nil {
			return
		}
		available := cap(s.sem) - len(s.sem)
		if available <= 0 {
			select {
			case <-ctx.Done():
				return
			case <-s.slotFreed:
				continue
			}
		}
		claimedOK := s.dispatchDue(ctx, available)
		wait := s.cfg.PollInterval
		if claimedOK {
			wait = s.nextWait(ctx)
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-s.wake:
			timer.Stop()
		case <-s.slotFreed:
			timer.Stop()
		case <-timer.C:
		}
	}
}

func (s *Scheduler) nextWait(ctx context.Context) time.Duration {
	nextDue, ok, err := s.cfg.Store.NextFollowupDue(ctx)
	if err != nil {
		if ctx.Err() == nil {
			s.log.Warn("check next follow-up due", "err", err)
		}
		return s.cfg.PollInterval
	}
	if !ok {
		return s.cfg.PollInterval
	}
	wait := nextDue.Sub(s.cfg.Now().UTC())
	if wait <= 0 {
		return 0
	}
	if wait > s.cfg.PollInterval {
		return s.cfg.PollInterval
	}
	return wait
}

func (s *Scheduler) dispatchDue(ctx context.Context, limit int) bool {
	if ctx.Err() != nil {
		return false
	}
	due, err := s.cfg.Claim(ctx, s.cfg.Now().UTC(), limit)
	if err != nil {
		if ctx.Err() == nil {
			s.log.Error("claim due follow-ups", "err", err)
		}
		return false
	}
	for _, f := range due {
		s.sem <- struct{}{}
		s.wg.Add(1)
		go func(f store.Followup) {
			defer func() {
				<-s.sem
				select {
				case s.slotFreed <- struct{}{}:
				default:
				}
				s.wg.Done()
			}()
			log := s.log.With("followup", f.ID, "agent", f.Agent, "to", f.To)
			log.Info("running follow-up")
			err := s.cfg.RunTurn(ctx, f)
			state, errMsg := store.FollowupCompleted, ""
			if err != nil {
				state = store.FollowupFailed
				var already notifiedError
				if ctx.Err() != nil {
					errMsg = "Gateway shut down while follow-up was running; not retried"
					log.Warn("follow-up interrupted by shutdown")
					s.notify(ctx, f, fmt.Sprintf("Scheduled checkback for %s was interrupted by a gateway shutdown and was not retried.", f.Agent))
				} else {
					errMsg = err.Error()
					log.Warn("follow-up failed", "err", err)
					if !errors.As(err, &already) {
						s.notify(ctx, f, fmt.Sprintf("Scheduled checkback for %s failed: %s", f.Agent, errMsg))
					}
				}
			} else {
				log.Info("follow-up completed")
			}
			fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			if ferr := s.cfg.Store.FinishFollowup(fctx, f.ID, state, errMsg); ferr != nil {
				log.Error("record follow-up outcome", "err", ferr)
			}
		}(f)
	}
	return true
}

func (s *Scheduler) notify(ctx context.Context, f store.Followup, text string) {
	if s.cfg.Notify == nil || f.To == "" {
		return
	}
	nctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := s.cfg.Notify(nctx, f, text); err != nil {
		s.log.Warn("notify follow-up failure", "followup", f.ID, "to", f.To, "err", err)
	}
}
