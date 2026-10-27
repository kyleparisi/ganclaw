package router

import (
	"context"
	"github.com/kyleparisi/ganclaw/internal/store"
	"time"
)

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
