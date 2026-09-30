// Package monitor decides which health problems to report and when:
// once when a problem starts (after its grace period), a reminder every
// RemindEvery while it lasts, and once when it clears.
package monitor

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/kyleparisi/ganclaw/internal/api"
)

// Settings tune Observe and Step.
type Settings struct {
	BotStaleAfter time.Duration            // a bot that hasn't polled for this long is a problem
	ProbeGrace    map[string]time.Duration // per-probe time before a failing probe is reported
	RemindEvery   time.Duration
}

// Observation is a problem seen right now.
type Observation struct {
	Key     string
	Message string
	Grace   time.Duration // report only once it has lasted this long
}

// Observe turns a health snapshot (or the error getting it) into problems.
func Observe(h api.HealthResponse, apiErr error, now time.Time, s Settings) []Observation {
	if apiErr != nil {
		return []Observation{{Key: "api", Message: "ganclaw isn't responding: " + apiErr.Error()}}
	}
	var obs []Observation

	if len(h.Providers) > 0 {
		anyUp := false
		var parts []string
		for _, p := range h.Providers {
			if p.Available {
				anyUp = true
				break
			}
			part := p.Provider
			if p.Reason != "" {
				part += " (" + p.Reason
				if !p.ResetsAt.IsZero() {
					part += ", resets " + p.ResetsAt.UTC().Format("Jan 2 15:04 UTC")
				}
				part += ")"
			}
			parts = append(parts, part)
		}
		if !anyUp {
			obs = append(obs, Observation{Key: "providers", Message: "No AI provider is available: " + strings.Join(parts, "; ")})
		}
	}

	for _, b := range h.Bots {
		switch {
		case !b.Running:
			obs = append(obs, Observation{Key: "bot:" + b.Name, Message: fmt.Sprintf("Bot %s is not running%s", b.Name, errSuffix(b.LastErr))})
		case b.LastPoll.IsZero() || now.Sub(b.LastPoll) > s.BotStaleAfter:
			obs = append(obs, Observation{Key: "bot:" + b.Name, Message: fmt.Sprintf("Bot %s hasn't reached Telegram since %s%s", b.Name, since(b.LastPoll), errSuffix(b.LastErr))})
		}
	}

	for _, p := range h.Probes {
		if !p.OK {
			obs = append(obs, Observation{Key: "probe:" + p.Name, Message: fmt.Sprintf("%s is unreachable%s", p.Name, errSuffix(p.Error)), Grace: s.ProbeGrace[p.Name]})
		}
	}
	return obs
}

func errSuffix(e string) string {
	if e == "" {
		return ""
	}
	if len(e) > 200 {
		e = e[:200] + "…"
	}
	return ": " + e
}

func since(t time.Time) string {
	if t.IsZero() {
		return "startup"
	}
	return t.UTC().Format("15:04 UTC")
}

// Tracked is a problem remembered between runs.
type Tracked struct {
	Message    string    `json:"message"`
	FirstSeen  time.Time `json:"first_seen"`
	NotifiedAt time.Time `json:"notified_at,omitempty"`
}

// State is persisted between runs.
type State struct {
	Problems map[string]*Tracked `json:"problems"`
}

// Step folds this run's observations into state and returns the lines to
// send (empty if nothing needs saying).
func Step(st State, obs []Observation, now time.Time, s Settings) (State, []string) {
	next := State{Problems: map[string]*Tracked{}}
	var lines []string
	seen := map[string]bool{}

	for _, o := range obs {
		seen[o.Key] = true
		t := st.Problems[o.Key]
		if t == nil {
			t = &Tracked{FirstSeen: now}
		} else {
			cp := *t
			t = &cp
		}
		t.Message = o.Message
		switch {
		case t.NotifiedAt.IsZero() && now.Sub(t.FirstSeen) >= o.Grace:
			lines = append(lines, "⚠️ "+o.Message)
			t.NotifiedAt = now
		case !t.NotifiedAt.IsZero() && s.RemindEvery > 0 && now.Sub(t.NotifiedAt) >= s.RemindEvery:
			lines = append(lines, fmt.Sprintf("⚠️ Still: %s (since %s)", o.Message, t.FirstSeen.UTC().Format("Jan 2 15:04 UTC")))
			t.NotifiedAt = now
		}
		next.Problems[o.Key] = t
	}

	var gone []string
	for k := range st.Problems {
		if !seen[k] {
			gone = append(gone, k)
		}
	}
	sort.Strings(gone)
	for _, k := range gone {
		if t := st.Problems[k]; !t.NotifiedAt.IsZero() {
			lines = append(lines, "✅ Resolved: "+t.Message)
		}
	}
	return next, lines
}

// Load reads state; a missing file is an empty state.
func Load(path string) (State, error) {
	st := State{Problems: map[string]*Tracked{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return State{Problems: map[string]*Tracked{}}, nil // corrupt state: start over
	}
	if st.Problems == nil {
		st.Problems = map[string]*Tracked{}
	}
	return st, nil
}

// Save writes state atomically.
func Save(path string, st State) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
