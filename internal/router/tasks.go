package router

import (
	"context"
	"sync"
	"time"

	"github.com/kyleparisi/ganclaw/internal/store"
)

// progressWriter limits task progress writes to one per interval: providers
// report every tool call and message, and each write is a database
// transaction on the provider's event path. Repeats are dropped; the turn's
// final state is written separately when it ends.
func progressWriter(write func(string), every time.Duration, now func() time.Time) func(string) {
	var mu sync.Mutex
	var last string
	var at time.Time
	return func(activity string) {
		mu.Lock()
		t := now()
		if activity == last || (!at.IsZero() && t.Sub(at) < every) {
			mu.Unlock()
			return
		}
		last, at = activity, t
		mu.Unlock()
		write(activity)
	}
}

func (r *Router) CreateTask(ctx context.Context, agent string) (store.Task, error) {
	return r.cfg.Store.CreateTask(ctx, agent)
}
func (r *Router) FailTask(id string) { r.taskUpdate(id, "failed", "Router rejected queue submission") }
func (r *Router) taskUpdate(id, state, progress string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.cfg.Store.UpdateTask(ctx, id, state, progress); err != nil {
		r.log.Error("update worker task", "task", id, "err", err)
	}
}
