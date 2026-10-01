// Package gateway connects API requests to bots and the router.
package gateway

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/kyleparisi/ganclaw/internal/agent"
	"github.com/kyleparisi/ganclaw/internal/api"
	"github.com/kyleparisi/ganclaw/internal/logx"
	"github.com/kyleparisi/ganclaw/internal/provider"
	"github.com/kyleparisi/ganclaw/internal/router"
	"github.com/kyleparisi/ganclaw/internal/telegram"
)

type Gateway struct {
	// Ctx is the gateway's lifetime; runs waiting in a queue give up when
	// it ends.
	Ctx    context.Context
	Router *router.Router
	Agents map[string]agent.Agent
	Bots   map[string]*telegram.Bot
	Logger *slog.Logger
}

// Probe is an HTTP check reported by Health.
type Probe struct {
	Name string
	URL  string
}

// Health collects bot state, provider status and probe results.
func (g *Gateway) Health(version string, status func(context.Context) []provider.Status, probes []Probe, httpc *http.Client) func(context.Context) api.HealthResponse {
	if httpc == nil {
		httpc = &http.Client{Timeout: 5 * time.Second}
	}
	return func(ctx context.Context) api.HealthResponse {
		resp := api.HealthResponse{Version: version, Bots: []api.BotHealth{}, Probes: []api.ProbeResult{}}
		if status != nil {
			resp.Providers = status(ctx)
		}
		names := make([]string, 0, len(g.Bots))
		for n := range g.Bots {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			h := g.Bots[n].Health()
			resp.Bots = append(resp.Bots, api.BotHealth{Name: n, Running: h.Running, LastPoll: h.LastPoll, LastErr: h.LastErr})
		}
		for _, p := range probes {
			resp.Probes = append(resp.Probes, probe(ctx, httpc, p))
		}
		if g.Router != nil {
			resp.ActiveTurns = g.Router.Active()
		}
		return resp
	}
}

func probe(ctx context.Context, httpc *http.Client, p Probe) api.ProbeResult {
	r := api.ProbeResult{Name: p.Name}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.URL, nil)
	if err != nil {
		r.Error = err.Error()
		return r
	}
	resp, err := httpc.Do(req)
	if err != nil {
		r.Error = err.Error()
		return r
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		r.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
		return r
	}
	r.OK = true
	return r
}

// ListAgents describes the agents (with their bots) and contacts.
func (g *Gateway) ListAgents(contacts []string) func(context.Context) api.AgentsResponse {
	return func(context.Context) api.AgentsResponse {
		names := make([]string, 0, len(g.Agents))
		for n := range g.Agents {
			names = append(names, n)
		}
		sort.Strings(names)
		resp := api.AgentsResponse{Contacts: append([]string{}, contacts...)}
		for _, n := range names {
			resp.Agents = append(resp.Agents, api.AgentInfo{Name: n, Bot: g.AgentBot(n)})
		}
		return resp
	}
}

// AgentBot returns the name of the first bot serving agentName, or "".
func (g *Gateway) AgentBot(agentName string) string {
	names := make([]string, 0, len(g.Bots))
	for n := range g.Bots {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if g.Bots[n].Agent == agentName {
			return n
		}
	}
	return ""
}

// ErrShuttingDown is returned for runs interrupted by shutdown.
var ErrShuttingDown = errors.New("ganclaw is shutting down")

// Send delivers plain text to t.
func (g *Gateway) Send(ctx context.Context, t api.Target, text string) error {
	b, ok := g.Bots[t.Bot]
	if !ok {
		return api.Errorf(api.CodeUnknownBot, "no bot named %q", t.Bot)
	}
	return b.Client.SendMessage(ctx, t.Chat, text)
}

// Run queues an agent turn and waits for it. With a target, the reply
// streams into that chat and the turn joins the chat's conversation, unless
// req.Session names a separate one. Without a target it runs in a named (or
// one-off) job conversation.
func (g *Gateway) Run(ctx context.Context, req api.RunRequest, t *api.Target) (router.TurnResult, error) {
	a, ok := g.Agents[req.Agent]
	if !ok {
		return router.TurnResult{}, api.Errorf(api.CodeUnknownAgent, "no agent named %q", req.Agent)
	}
	if req.Heartbeat {
		req.Prompt = WithHeartbeat(req.Prompt, a.Workspace)
	}

	var m router.Message
	if t != nil {
		b, ok := g.Bots[t.Bot]
		if !ok {
			return router.TurnResult{}, api.Errorf(api.CodeUnknownBot, "no bot named %q", t.Bot)
		}
		m = b.Message(t.Chat, req.Agent, req.Prompt)
		if req.Session != "" {
			// Deliver to the chat but keep the work in its own conversation.
			m.ChatKey = JobKey(req.Agent, req.Session)
		}
	} else {
		m = router.Message{
			ChatKey: JobKey(req.Agent, req.Session),
			Agent:   req.Agent,
			Text:    req.Prompt,
			Reply:   func(context.Context, string) error { return nil }, // returned to the caller instead
		}
	}

	if req.Quiet {
		quiet(&m)
	}

	log := logx.OrDiscard(g.Logger).With("agent", req.Agent, "chat", m.ChatKey)
	if req.Async {
		// The caller has gone by the time this runs; only shutdown stops it.
		m.OnDone = func(res router.TurnResult) {
			log.Info("queued run finished", "provider", res.Provider, "err", res.Err)
		}
		if !g.Router.Submit(m) {
			return router.TurnResult{}, api.Errorf(api.CodeBusy, "that conversation has too many queued messages; try again later")
		}
		return router.TurnResult{}, nil
	}

	done := make(chan router.TurnResult, 1)
	m.Ctx = ctx
	m.OnDone = func(res router.TurnResult) { done <- res }
	if !g.Router.Submit(m) {
		return router.TurnResult{}, api.Errorf(api.CodeBusy, "that conversation has too many queued messages; try again later")
	}
	select {
	case res := <-done:
		return res, nil
	case <-g.Ctx.Done():
		return router.TurnResult{Err: ErrShuttingDown, Text: ErrShuttingDown.Error()}, nil
	}
}

// JobKey is the conversation key for runs that aren't delivered to a chat.
// An empty session gets a unique key, i.e. a fresh conversation.
func JobKey(agentName, session string) string {
	if session == "" {
		var b [8]byte
		_, _ = rand.Read(b[:])
		session = "once:" + hex.EncodeToString(b[:])
	}
	return "job:" + agentName + ":" + session
}

// QuietReplies are answers that mean "nothing to tell the user".
var QuietReplies = []string{"NO_REPLY", "HEARTBEAT_OK"}

// IsQuiet reports whether text is a quiet reply.
func IsQuiet(text string) bool {
	t := strings.Trim(strings.TrimSpace(text), "`*_. ")
	for _, q := range QuietReplies {
		if t == q {
			return true
		}
	}
	return false
}

// quiet turns off streaming and drops quiet replies.
func quiet(m *router.Message) {
	m.Stream = nil
	reply := m.Reply
	m.Reply = func(ctx context.Context, text string) error {
		if IsQuiet(text) {
			return nil
		}
		return reply(ctx, text)
	}
}

// WithHeartbeat appends the standing tasks from workspace/HEARTBEAT.md,
// ignoring headings, comments and blank lines. Unchanged if there are none.
func WithHeartbeat(prompt, workspace string) string {
	data, err := os.ReadFile(filepath.Join(workspace, "HEARTBEAT.md"))
	if err != nil {
		return prompt
	}
	var tasks []string
	for _, line := range strings.Split(string(data), "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "<!--") {
			continue
		}
		tasks = append(tasks, line)
	}
	if len(tasks) == 0 {
		return prompt
	}
	return prompt + "\n\nAlso do your standing heartbeat tasks (from HEARTBEAT.md):\n" + strings.Join(tasks, "\n")
}
