package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/kyleparisi/ganclaw/internal/logx"
	"github.com/kyleparisi/ganclaw/internal/provider"
	"github.com/kyleparisi/ganclaw/internal/router"
	"github.com/kyleparisi/ganclaw/internal/store"
)

// Server handles API requests. Behaviour comes from its function fields.
type Server struct {
	Resolve func(to, via string) (Target, error)
	// Send delivers text to t.
	Send func(ctx context.Context, t Target, text string) error
	// Run executes an agent turn, delivering to t if non-nil. It returns
	// an *Error for requests that can't start (unknown agent, busy).
	Run func(ctx context.Context, req RunRequest, t *Target) (router.TurnResult, error)
	// Status reports provider availability.
	Status func(ctx context.Context) []provider.Status
	// AgentBot returns the bot that belongs to an agent ("" if none). Runs
	// addressed to a contact are delivered through the agent's own bot
	// unless the request names one with Via. Optional.
	AgentBot func(agent string) string
	// Agents lists agents and contacts for GET /v1/agents. Optional.
	Agents func(ctx context.Context) AgentsResponse
	// Health reports providers, bots and probes for GET /v1/health.
	// Optional.
	Health func(ctx context.Context) HealthResponse
	// Store holds idempotency keys. Optional; without it keys are ignored.
	Store  *store.Store
	Logger *slog.Logger
}

const maxBody = 1 << 20

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/send", s.handleSend)
	mux.HandleFunc("POST /v1/run", s.handleRun)
	mux.HandleFunc("GET /v1/status", s.handleStatus)
	mux.HandleFunc("GET /v1/agents", s.handleAgents)
	mux.HandleFunc("GET /v1/health", s.handleHealth)
	return mux
}

// Serve listens on a Unix socket until ctx is cancelled. The socket is
// chmod 0600; put it in a directory only the service user can enter (the
// default, <state_dir>, is 0700) so there is no window where others can
// connect.
func (s *Server) Serve(ctx context.Context, socket string) error {
	if err := removeStale(socket); err != nil {
		return err
	}
	ln, err := net.Listen("unix", socket)
	if err != nil {
		return fmt.Errorf("api listen %s: %w", socket, err)
	}
	if err := os.Chmod(socket, 0o600); err != nil {
		ln.Close()
		return err
	}
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	ln = &peerCheckListener{Listener: ln, allow: allowedUIDs(), log: logx.OrDiscard(s.Logger)}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	logx.OrDiscard(s.Logger).Info("api listening", "socket", socket)
	err = srv.Serve(ln)
	os.Remove(socket)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// removeStale deletes a leftover socket file if nothing is listening on it.
func removeStale(socket string) error {
	if _, err := os.Stat(socket); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if c, err := net.DialTimeout("unix", socket, time.Second); err == nil {
		c.Close()
		return fmt.Errorf("api socket %s is already in use", socket)
	}
	return os.Remove(socket)
}

func (s *Server) handleSend(w http.ResponseWriter, r *http.Request) {
	var req SendRequest
	if !decode(w, r, &req) {
		return
	}
	if req.Text == "" {
		writeErr(w, Errorf(CodeBadRequest, "text is required"))
		return
	}
	s.idempotent(w, r, req.IdempotencyKey, func() (any, error) {
		t, err := s.Resolve(req.To, req.Via)
		if err != nil {
			return nil, err
		}
		if err := s.Send(r.Context(), t, req.Text); err != nil {
			return nil, Errorf(CodeFailed, "send to %s: %v", t, err)
		}
		logx.OrDiscard(s.Logger).Info("api send", "target", t.String(), "text_len", len(req.Text))
		return SendResponse{OK: true, Target: t.String()}, nil
	})
}

func (s *Server) handleRun(w http.ResponseWriter, r *http.Request) {
	var req RunRequest
	if !decode(w, r, &req) {
		return
	}
	switch {
	case req.Agent == "":
		writeErr(w, Errorf(CodeBadRequest, "agent is required"))
		return
	case req.Prompt == "":
		writeErr(w, Errorf(CodeBadRequest, "prompt is required"))
		return
	}
	s.idempotent(w, r, req.IdempotencyKey, func() (any, error) {
		ctx := r.Context()
		if req.TimeoutSeconds > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, time.Duration(req.TimeoutSeconds)*time.Second)
			defer cancel()
		}
		var target *Target
		if req.To != "" {
			via := req.Via
			if _, raw := ParseAddress(req.To); !raw && via == "" && s.AgentBot != nil {
				via = s.AgentBot(req.Agent)
			}
			t, err := s.Resolve(req.To, via)
			if err != nil {
				return nil, err
			}
			target = &t
		}
		start := time.Now()
		res, err := s.Run(ctx, req, target)
		if err != nil {
			return nil, err
		}
		resp := RunResponse{OK: true, Provider: res.Provider, Text: res.Text}
		if target != nil {
			resp.Target = target.String()
		}
		if req.Async {
			logx.OrDiscard(s.Logger).Info("api run queued", "agent", req.Agent, "delivered", target != nil)
			resp.Queued = true
			return resp, nil
		}
		logx.OrDiscard(s.Logger).Info("api run", "agent", req.Agent, "provider", res.Provider, "delivered", target != nil, "duration", time.Since(start), "err", res.Err)
		if res.Err != nil {
			return nil, runError(ctx, res)
		}
		return resp, nil
	})
}

func runError(ctx context.Context, res router.TurnResult) *Error {
	switch {
	case errors.Is(res.Err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded):
		return Errorf(CodeTimeout, "%s", res.Text)
	case errors.Is(res.Err, provider.ErrUnavailable):
		return Errorf(CodeUnavailable, "%s", res.Text)
	default:
		return Errorf(CodeFailed, "%s", res.Text)
	}
}

func (s *Server) handleAgents(w http.ResponseWriter, r *http.Request) {
	if s.Agents == nil {
		writeJSON(w, http.StatusOK, AgentsResponse{})
		return
	}
	writeJSON(w, http.StatusOK, s.Agents(r.Context()))
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if s.Health == nil {
		writeJSON(w, http.StatusOK, HealthResponse{})
		return
	}
	writeJSON(w, http.StatusOK, s.Health(r.Context()))
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "providers": s.Status(r.Context())})
}

// idempotent runs do once per key. A repeated key returns the stored
// response with replayed=true, or in_progress while the first is running.
// Failed attempts release the key so they can be retried.
func (s *Server) idempotent(w http.ResponseWriter, r *http.Request, key string, do func() (any, error)) {
	if key == "" || s.Store == nil {
		respond(w, do)
		return
	}
	ctx := context.WithoutCancel(r.Context())
	claimed, stored, err := s.Store.Claim(ctx, key)
	switch {
	case err != nil:
		writeErr(w, Errorf(CodeFailed, "idempotency: %v", err))
		return
	case !claimed && stored == nil:
		writeErr(w, Errorf(CodeInProgress, "a request with idempotency key %q is still running", key))
		return
	case !claimed:
		var body map[string]any
		if json.Unmarshal(stored, &body) != nil {
			body = map[string]any{"ok": true}
		}
		body["replayed"] = true
		writeJSON(w, http.StatusOK, body)
		return
	}
	body, err := do()
	if err != nil {
		_ = s.Store.Release(ctx, key)
		writeErr(w, err)
		return
	}
	data, _ := json.Marshal(body)
	if err := s.Store.Complete(ctx, key, data); err != nil {
		logx.OrDiscard(s.Logger).Error("idempotency complete", "err", err)
	}
	writeJSON(w, http.StatusOK, body)
}

func respond(w http.ResponseWriter, do func() (any, error)) {
	body, err := do()
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, body)
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(io.LimitReader(r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeErr(w, Errorf(CodeBadRequest, "invalid JSON: %v", err))
		return false
	}
	return true
}

func writeErr(w http.ResponseWriter, err error) {
	var e *Error
	if !errors.As(err, &e) {
		e = Errorf(CodeFailed, "%v", err)
	}
	writeJSON(w, e.Status, errorResponse{OK: false, Error: e})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
