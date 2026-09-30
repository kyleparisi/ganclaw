package codex

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/kyleparisi/ganclaw/internal/provider"
)

// Status asks the app-server for the account's rate limits. It is cheap
// and doesn't consume usage.
func (p *Provider) Status(ctx context.Context) (provider.Status, error) {
	st := provider.Status{Provider: "codex", Known: true}
	select {
	case <-p.done:
		st.Reason = fmt.Sprintf("app-server exited: %v", p.exitErr())
		return st, nil
	default:
	}

	var r rateLimitsResponse
	if err := p.rpc.call(ctx, "account/rateLimits/read", struct{}{}, &r); err != nil {
		return st, p.wrapCallErr("account/rateLimits/read", err)
	}
	rl := r.RateLimits

	var exhausted []time.Time
	for _, w := range []*rateLimitWindow{rl.Primary, rl.Secondary} {
		if w == nil {
			continue
		}
		win := provider.Window{
			Name:        provider.WindowName(time.Duration(w.WindowDurationMins) * time.Minute),
			UsedPercent: w.UsedPercent,
		}
		if w.ResetsAt > 0 {
			win.ResetsAt = time.Unix(w.ResetsAt, 0)
		}
		st.Windows = append(st.Windows, win)
		if w.UsedPercent >= 100 && !win.ResetsAt.IsZero() {
			exhausted = append(exhausted, win.ResetsAt)
		}
	}

	hasCredits := rl.Credits != nil && (rl.Credits.HasCredits || rl.Credits.Unlimited)
	st.Available = (r.OrdinaryUsageAllowed == nil || *r.OrdinaryUsageAllowed) || hasCredits
	var detail []string
	if rl.PlanType != "" {
		detail = append(detail, rl.PlanType+" plan")
	}
	if !st.Available {
		reason := "usage limit reached"
		if rl.RateLimitReachedType != nil && *rl.RateLimitReachedType != "" {
			reason = strings.ReplaceAll(*rl.RateLimitReachedType, "_", " ")
		}
		detail = append([]string{reason}, detail...)
		// Usable again once every exhausted window has reset.
		for _, t := range exhausted {
			if t.After(st.ResetsAt) {
				st.ResetsAt = t
			}
		}
	}
	st.Reason = strings.Join(detail, ", ")
	return st, nil
}

// limitCodes are failures worth asking the server when they'll clear.
var limitCodes = map[string]bool{"usageLimitExceeded": true, "rateLimitExceeded": true}

// retryAt looks up when a limit failure clears. Zero if unknown.
func (p *Provider) retryAt(ctx context.Context) time.Time {
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	st, err := p.Status(sctx)
	if err != nil || st.Available {
		return time.Time{}
	}
	return st.ResetsAt
}
