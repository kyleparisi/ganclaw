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
	"github.com/kyleparisi/ganclaw/internal/appchannel"
	"github.com/kyleparisi/ganclaw/internal/followup"
	"github.com/kyleparisi/ganclaw/internal/logx"
	"github.com/kyleparisi/ganclaw/internal/provider"
	"github.com/kyleparisi/ganclaw/internal/router"
	"github.com/kyleparisi/ganclaw/internal/store"
	"github.com/kyleparisi/ganclaw/internal/telegram"
)

type Gateway struct {
	// Ctx is the gateway's lifetime; runs waiting in a queue give up when
	// it ends.
	Ctx    context.Context
	Router *router.Router
	Agents map[string]agent.Agent
	Bots   map[string]*telegram.Bot
	// App is the native-app channel, nil when it is off.
	App *appchannel.Channel
	// HasProvider reports whether a named provider is configured. Optional.
	HasProvider func(string) bool
	Logger      *slog.Logger
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
	if t.Channel == "app" {
		if err := g.checkApp(t); err != nil {
			return err
		}
		_, err := g.App.Post(ctx, t.Bot, store.RoleAgent, text, "")
		return err
	}
	b, ok := g.Bots[t.Bot]
	if !ok {
		return api.Errorf(api.CodeUnknownBot, "no bot named %q", t.Bot)
	}
	return b.Client.SendMessage(ctx, t.Chat, text)
}

// SendJob records text in an app thread as a run of a scheduled job, without
// running an agent. failed shows the run as failed in the app.
func (g *Gateway) SendJob(ctx context.Context, t api.Target, text, job string, failed bool) error {
	if err := g.checkApp(t); err != nil {
		return err
	}
	status := store.StatusDone
	if failed {
		status = store.StatusError
	}
	_, err := g.App.PostMessage(ctx, store.AppMessage{Thread: t.Bot, Role: store.RoleAgent, Text: text, Job: job, Status: status})
	return err
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
	prov := strings.TrimSpace(req.Provider)
	if prov != "" && g.HasProvider != nil && !g.HasProvider(prov) {
		return router.TurnResult{}, api.Errorf(api.CodeBadRequest, "provider %q is not configured", prov)
	}
	if req.Source != "review" && strings.HasPrefix(req.Session, "review:") {
		return router.TurnResult{}, api.Errorf(api.CodeBadRequest, "session prefix %q is reserved for reviews", "review:")
	}
	if t != nil {
		if err := g.CheckRunTarget(req.Agent, *t); err != nil {
			return router.TurnResult{}, err
		}
	}
	if req.Heartbeat {
		req.Prompt = WithHeartbeat(req.Prompt, a.Workspace)
	}

	var m router.Message
	if t != nil && t.Channel == "app" {
		// The reply lands in the target agent's thread, tagged with the
		// session as the job name so the app can list scheduled runs.
		job := req.Session
		if req.Async {
			job = ""
		}
		if req.Job != "" {
			job = req.Job
		}
		m = g.App.Message(t.Bot, req.Prompt, job)
		m.Agent = req.Agent
		if req.Agent != t.Bot {
			m.ChatKey += ":" + req.Agent
		}
		if req.Source == "review" {
			m.ChatKey = reviewKey(req.Agent, req.Session)
		} else if req.Session != "" {
			m.ChatKey = JobKey(req.Agent, req.Session)
		}
		if m.ChatKey != appchannel.ChatKey(t.Bot) || (job != "" && job != store.BackgroundJob) {
			// Separate-session jobs, reviews, and follow-ups don't occupy the
			// agent's interactive app thread; keep them out of AgentState.Working.
			m.Typing, m.OnDone, m.AppThread = nil, nil, false
		}
	} else if t != nil {
		b := g.Bots[t.Bot]
		m = b.Message(t.Chat, req.Agent, req.Prompt)
		if req.Source == "review" {
			m.ChatKey = reviewKey(req.Agent, req.Session)
		} else if req.Session != "" {
			// Deliver to the chat but keep the work in its own conversation.
			m.ChatKey = JobKey(req.Agent, req.Session)
		}
	} else {
		chatKey := JobKey(req.Agent, req.Session)
		if req.Source == "review" {
			chatKey = reviewKey(req.Agent, req.Session)
		}
		m = router.Message{
			ChatKey: chatKey,
			Agent:   req.Agent,
			Text:    req.Prompt,
			Reply:   func(context.Context, string) error { return nil }, // returned to the caller instead
		}
	}

	if req.Quiet {
		quiet(&m)
	}

	// Unnamed background work in the app runs in its own conversation, so it
	// neither blocks nor joins the thread's. Telegram handoffs keep joining
	// the chat's conversation: the contact may reply to the result.
	if req.Async && req.Session == "" && req.Source != "review" && t != nil && t.Channel == "app" {
		m.ChatKey = JobKey(req.Agent, "")
		m.AppThread = false
	}
	m.RunKind = runKind(req, t)
	m.RunLabel = runLabel(req)
	m.Provider = prov
	m.ReadOnly = req.ReadOnly
	if req.Source == "review" {
		m.ReadOnly = true
		m.Instructions = reviewInstructions
	}
	log := logx.OrDiscard(g.Logger).With("agent", req.Agent, "chat", m.ChatKey)
	if req.Async {
		task, err := g.Router.CreateTask(ctx, req.Agent)
		if err != nil {
			return router.TurnResult{}, err
		}
		m.TaskID = task.ID
		if g.App != nil {
			// Keep worker history separate from the ordinary agent conversation.
			// Original result delivery still happens through Reply and SendFile.
			m.Stream = nil
			if t != nil && t.Channel == "app" {
				m.Typing, m.OnDone = nil, nil
			}
			g.App.TrackTask(&m, task.ID)
		}
		// The caller has gone by the time this runs; only shutdown stops it.
		m.OnDone = chain(m.OnDone, func(res router.TurnResult) {
			log.Info("queued run finished", "provider", res.Provider, "err", res.Err)
		})
		if !g.Router.Submit(m) {
			g.Router.FailTask(task.ID)
			return router.TurnResult{}, api.Errorf(api.CodeBusy, "that conversation has too many queued messages; try again later")
		}
		return router.TurnResult{TaskID: task.ID}, nil
	}

	done := make(chan router.TurnResult, 1)
	m.Ctx = ctx
	m.OnDone = chain(m.OnDone, func(res router.TurnResult) { done <- res })
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

// CheckRunTarget verifies that agentName exists and can deliver to t.
func (g *Gateway) CheckRunTarget(agentName string, t api.Target) error {
	if _, ok := g.Agents[agentName]; !ok {
		return api.Errorf(api.CodeUnknownAgent, "no agent named %q", agentName)
	}
	if t.Channel == "app" {
		return g.checkApp(t)
	}
	if _, ok := g.Bots[t.Bot]; !ok {
		return api.Errorf(api.CodeUnknownBot, "no bot named %q", t.Bot)
	}
	return nil
}

// CheckFollowupTarget verifies that agentName exists and can deliver to the
// canonical address string to.
func (g *Gateway) CheckFollowupTarget(agentName, to string) error {
	target, ok := api.ParseAddress(to)
	if !ok {
		return api.Errorf(api.CodeBadRequest, "invalid follow-up target %q", to)
	}
	return g.CheckRunTarget(agentName, target)
}

// RunFollowup runs one due follow-up turn against its resolved target.
func (g *Gateway) RunFollowup(ctx context.Context, f store.Followup) error {
	target, ok := api.ParseAddress(f.To)
	if !ok {
		return api.Errorf(api.CodeBadRequest, "invalid follow-up target %q", f.To)
	}
	req := api.RunRequest{
		Agent:   f.Agent,
		Prompt:  followup.BuildPrompt(f),
		To:      f.To,
		Session: f.Session,
		Quiet:   f.Quiet,
		Source:  "followup",
	}
	if f.Session != "" {
		// store.BackgroundJob keeps the reply visible in the app conversation
		// instead of filing it under scheduled Jobs history.
		req.Job = store.BackgroundJob
	}
	res, err := g.Run(ctx, req, &target)
	if err != nil {
		return err
	}
	if res.Err != nil {
		if errors.Is(res.Err, ErrShuttingDown) {
			return res.Err
		}
		// Router already delivered an error reply to the chat.
		return followup.AlreadyNotified(res.Err)
	}
	return nil
}

// NotifyFollowup delivers a plain failure notice to a follow-up's destination.
func (g *Gateway) NotifyFollowup(ctx context.Context, f store.Followup, text string) error {
	target, ok := api.ParseAddress(f.To)
	if !ok {
		return api.Errorf(api.CodeBadRequest, "invalid follow-up target %q", f.To)
	}
	return g.Send(ctx, target, text)
}

// checkApp reports whether t can be delivered to the app channel.
func (g *Gateway) checkApp(t api.Target) error {
	if g.App == nil {
		return api.Errorf(api.CodeBadRequest, "the app channel is off; set [app] listen in the config")
	}
	if _, ok := g.Agents[t.Bot]; !ok {
		return api.Errorf(api.CodeUnknownAgent, "no agent named %q for %s", t.Bot, t)
	}
	return nil
}

// chain runs first (if set), then next.
func chain(first, next func(router.TurnResult)) func(router.TurnResult) {
	if first == nil {
		return next
	}
	return func(res router.TurnResult) {
		first(res)
		next(res)
	}
}

const reviewInstructions = `# Code review

You are performing an independent, read-only code review in this workspace.

- Inspect the requested git diff, commits, or files directly using read-only file reads (and read-only commands where permitted).
- Do not modify files, commit, push, or run destructive commands.
- Report concrete findings ranked by severity (High, Medium, Low) with file paths and line references, plus any missing tests or edge cases.
- End with a clear verdict on whether the change is ready to push or needs revisions first.`

// runKind classifies an API run for live activity views.
func runKind(req api.RunRequest, t *api.Target) string {
	switch req.Source {
	case "ask", "handoff", "followup", "review":
		return req.Source
	}
	if req.Async {
		return "handoff"
	}
	if (req.Job != "" && req.Job != store.BackgroundJob) || (req.Session != "" && t != nil && t.Channel == "app") {
		return "job"
	}
	if !req.Quiet && req.Session == "" && req.Job == "" && t != nil && t.Channel == "app" && req.Agent == t.Bot {
		return "app"
	}
	return "run"
}

func runLabel(req api.RunRequest) string {
	if req.Label != "" {
		return req.Label
	}
	if req.Job != "" && req.Job != store.BackgroundJob {
		return req.Job
	}
	return req.Session
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

// reviewKey isolates read-only review sessions under a reserved namespace so
// they never share provider sessions with interactive chats or writable jobs.
func reviewKey(agentName, session string) string {
	if session == "" {
		var b [8]byte
		_, _ = rand.Read(b[:])
		session = "once:" + hex.EncodeToString(b[:])
	}
	return "job:" + agentName + ":review:" + session
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

// quiet turns off streaming and typing, and drops quiet replies.
func quiet(m *router.Message) {
	m.Stream = nil
	m.Typing = nil
	m.AppThread = false
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
