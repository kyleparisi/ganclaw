package provider

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Status is a provider's current availability as far as ganclaw knows.
type Status struct {
	Provider  string
	Available bool
	// Known is false when the provider has nothing to report yet (e.g. the
	// claude CLI only reports limits after a turn).
	Known  bool
	Reason string // why it's unavailable, or extra detail
	// ResetsAt is when an exhausted limit resets. Zero if unknown.
	ResetsAt time.Time
	Windows  []Window
	// CoolingDownUntil is set by Chain when it is skipping this provider.
	CoolingDownUntil time.Time
}

// Window is one usage-limit window.
type Window struct {
	Name        string // e.g. "5-hour", "weekly"
	UsedPercent float64
	ResetsAt    time.Time
}

// Statuser is implemented by providers that can report their limits.
type Statuser interface {
	Status(ctx context.Context) (Status, error)
}

// RetryAter is implemented by errors that know when retrying can succeed.
type RetryAter interface {
	RetryAt() time.Time
}

// WindowName names a limit window by its length.
func WindowName(d time.Duration) string {
	switch {
	case d <= 0:
		return "window"
	case d == 5*time.Hour:
		return "5-hour"
	case d == 24*time.Hour:
		return "daily"
	case d == 7*24*time.Hour:
		return "weekly"
	case d%time.Hour == 0:
		return fmt.Sprintf("%d-hour", int(d.Hours()))
	default:
		return fmt.Sprintf("%d-min", int(d.Minutes()))
	}
}

// FormatStatus renders statuses for humans (Telegram /status, CLI).
func FormatStatus(statuses []Status, now time.Time) string {
	var b strings.Builder
	for i, s := range statuses {
		if i > 0 {
			b.WriteString("\n")
		}
		switch {
		case !s.Known:
			fmt.Fprintf(&b, "%s: unknown", s.Provider)
		case s.Available:
			fmt.Fprintf(&b, "%s: available", s.Provider)
		default:
			fmt.Fprintf(&b, "%s: unavailable", s.Provider)
		}
		if s.Reason != "" {
			b.WriteString(" — " + s.Reason)
		}
		if !s.Available && !s.ResetsAt.IsZero() {
			b.WriteString(" (resets " + formatWhen(s.ResetsAt, now) + ")")
		}
		b.WriteString("\n")
		for _, w := range s.Windows {
			fmt.Fprintf(&b, "  %s: %.0f%% used", w.Name, w.UsedPercent)
			if !w.ResetsAt.IsZero() {
				b.WriteString(", resets " + formatWhen(w.ResetsAt, now))
			}
			b.WriteString("\n")
		}
		if s.CoolingDownUntil.After(now) {
			b.WriteString("  skipped until " + formatWhen(s.CoolingDownUntil, now) + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func formatWhen(t, now time.Time) string {
	d := t.Sub(now).Round(time.Minute)
	stamp := t.In(now.Location()).Format("Jan 2 15:04 MST")
	if d <= 0 {
		return stamp
	}
	return fmt.Sprintf("%s, in %s", stamp, humanDuration(d))
}

func humanDuration(d time.Duration) string {
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh", days, hours)
	case hours > 0:
		return fmt.Sprintf("%dh %dm", hours, mins)
	default:
		return fmt.Sprintf("%dm", max(mins, 1))
	}
}
