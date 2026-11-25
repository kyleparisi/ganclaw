package router

import (
	"crypto/rand"
	"encoding/hex"
	"sort"
	"strings"
	"time"
)

// ActiveRun describes a turn currently queued or executing in the router.
// It contains only safe lifecycle metadata (no prompts, replies, or tool args).
type ActiveRun struct {
	ID        string    `json:"id"`
	Agent     string    `json:"agent"`
	Kind      string    `json:"kind"`               // app | telegram | handoff | ask | followup | job | run
	Label     string    `json:"label,omitempty"`    // optional safe label (e.g. job or session name)
	TaskID    string    `json:"task_id,omitempty"`  // linked worker_tasks ID if backed by a persisted task
	State     string    `json:"state"`              // queued | running
	Progress  string    `json:"progress,omitempty"` // latest safe provider activity
	StartedAt time.Time `json:"started_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ActiveRuns returns a sorted snapshot of turns currently queued or running.
func (r *Router) ActiveRuns() []ActiveRun {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.activeRunsLocked()
}

// SetOnRunsChanged registers a callback notified whenever active runs change.
func (r *Router) SetOnRunsChanged(fn func([]ActiveRun)) {
	r.mu.Lock()
	r.onRunsChanged = fn
	r.mu.Unlock()
}

func (r *Router) activeRunsLocked() []ActiveRun {
	out := make([]ActiveRun, 0, len(r.runs))
	for _, run := range r.runs {
		out = append(out, run)
	}
	sort.Slice(out, func(i, j int) bool {
		if (out[i].State == "running") != (out[j].State == "running") {
			return out[i].State == "running"
		}
		if !out[i].StartedAt.Equal(out[j].StartedAt) {
			return out[i].StartedAt.Before(out[j].StartedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func (r *Router) notifyRuns() {
	r.notifyMu.Lock()
	defer r.notifyMu.Unlock()
	r.mu.Lock()
	fn := r.onRunsChanged
	if fn == nil {
		r.mu.Unlock()
		return
	}
	snap := r.activeRunsLocked()
	r.mu.Unlock()
	fn(snap)
}

func (r *Router) queueRunLocked(m *Message) {
	if !isTrackedRun(*m) {
		return
	}
	id := m.TaskID
	if id == "" {
		id = newRunID()
	}
	m.runID = id
	now := r.cfg.Now().UTC()
	r.runs[id] = ActiveRun{
		ID:        id,
		Agent:     m.Agent,
		Kind:      inferRunKind(*m),
		Label:     m.RunLabel,
		TaskID:    m.TaskID,
		State:     "queued",
		Progress:  "Waiting in router queue",
		StartedAt: now,
		UpdatedAt: now,
	}
}

func (r *Router) removeRuns(ids []string) {
	if len(ids) == 0 {
		return
	}
	changed := false
	r.mu.Lock()
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, ok := r.runs[id]; ok {
			delete(r.runs, id)
			changed = true
		}
	}
	r.mu.Unlock()
	if changed {
		r.notifyRuns()
	}
}

func (r *Router) startRun(id string) {
	if id == "" {
		return
	}
	r.mu.Lock()
	run, ok := r.runs[id]
	if ok {
		now := r.cfg.Now().UTC()
		run.State = "running"
		run.Progress = "Starting"
		run.StartedAt = now
		run.UpdatedAt = now
		r.runs[id] = run
	}
	r.mu.Unlock()
	if ok {
		r.notifyRuns()
	}
}

func (r *Router) updateRunProgress(id, activity string) {
	if id == "" || activity == "" {
		return
	}
	r.mu.Lock()
	run, ok := r.runs[id]
	if ok && run.State == "running" {
		run.Progress = activity
		run.UpdatedAt = r.cfg.Now().UTC()
		r.runs[id] = run
	} else {
		ok = false
	}
	r.mu.Unlock()
	if ok {
		r.notifyRuns()
	}
}

func (r *Router) finishRun(id string) {
	if id == "" {
		return
	}
	r.mu.Lock()
	_, ok := r.runs[id]
	if ok {
		delete(r.runs, id)
	}
	r.mu.Unlock()
	if ok {
		r.notifyRuns()
	}
}

func (r *Router) drainQueueRuns(q chan Message) {
	var ids []string
	for {
		select {
		case m := <-q:
			if m.runID != "" {
				ids = append(ids, m.runID)
			}
		default:
			r.removeRuns(ids)
			return
		}
	}
}

func isTrackedRun(m Message) bool {
	if m.Fetch == nil && isBuiltinCommand(command(m.Text)) {
		return false
	}
	return true
}

func isBuiltinCommand(cmd string) bool {
	switch cmd {
	case "new", "reset", "help", "start", "status":
		return true
	}
	return false
}

func inferRunKind(m Message) string {
	if m.RunKind != "" {
		return m.RunKind
	}
	switch {
	case strings.HasPrefix(m.ChatKey, "app:"):
		return "app"
	case strings.HasPrefix(m.ChatKey, "telegram:"):
		return "telegram"
	case strings.HasPrefix(m.ChatKey, "job:"):
		return "job"
	default:
		return "run"
	}
}

func newRunID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return "run-" + hex.EncodeToString(b[:])
}
