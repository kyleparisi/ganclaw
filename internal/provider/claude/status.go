package claude

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/kyleparisi/ganclaw/internal/provider"
)

// windowNames maps the CLI's unified window keys to display names.
var windowNames = map[string]string{"five_hour": "5-hour", "seven_day": "weekly"}

// Status reports the limits seen on the most recent turn. The CLI has no
// standalone status call, so this is Known=false until a turn has run.
func (p *Provider) Status(ctx context.Context) (provider.Status, error) {
	p.mu.Lock()
	rl := p.rateLimit
	p.mu.Unlock()

	st := provider.Status{Provider: "claude"}
	if rl == nil {
		st.Available = true
		st.Reason = "no limit data until a turn runs"
		return st, nil
	}
	st.Known = true

	keys := make([]string, 0, len(rl.UnifiedWindows))
	for k := range rl.UnifiedWindows {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		w := rl.UnifiedWindows[k]
		name := windowNames[k]
		if name == "" {
			name = strings.ReplaceAll(k, "_", "-")
		}
		win := provider.Window{Name: name, UsedPercent: w.Utilization * 100}
		if w.ResetsAt > 0 {
			win.ResetsAt = time.Unix(w.ResetsAt, 0)
		}
		st.Windows = append(st.Windows, win)
	}

	resets := time.Time{}
	if rl.ResetsAt > 0 {
		resets = time.Unix(rl.ResetsAt, 0)
	}
	// A rejection stops applying once its window has reset.
	st.Available = rl.Status != "rejected" || (!resets.IsZero() && !p.now().Before(resets))
	if !st.Available {
		st.Reason = "rate limited (" + windowNameOr(rl.RateLimitType) + ")"
		st.ResetsAt = resets
	} else if rl.Status == "allowed_warning" {
		st.Reason = "approaching " + windowNameOr(rl.RateLimitType) + " limit"
	}
	return st, nil
}

func windowNameOr(k string) string {
	if n := windowNames[k]; n != "" {
		return n
	}
	return k
}
