// Package agenttools is an MCP server that gives agents ganclaw's own
// abilities: messaging people and asking or handing work to other agents.
// It runs as `ganclaw mcp`, a child of codex/claude, and talks to the
// gateway over its API socket.
package agenttools

import (
	"context"
	"errors"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kyleparisi/ganclaw/internal/api"
	"github.com/kyleparisi/ganclaw/internal/followup"
	"github.com/kyleparisi/ganclaw/internal/store"
)

// Deps are the gateway calls the tools make; *api.Client provides them.
type Deps struct {
	OpenItems        func(context.Context, bool) ([]store.OpenItem, error)
	OpenItem         func(context.Context, string) (store.OpenItem, error)
	SaveOpenItem     func(context.Context, store.OpenItem) (store.OpenItem, error)
	ScheduleFollowup func(context.Context, api.FollowupRequest) (store.Followup, error)
	Followups        func(context.Context, string, bool) ([]store.Followup, error)
	CancelFollowup   func(context.Context, string) (store.Followup, error)
	Tasks            func(context.Context, string, string) (api.TasksResponse, error)
	Send             func(ctx context.Context, req api.SendRequest) (api.SendResponse, error)
	Run              func(ctx context.Context, req api.RunRequest) (api.RunResponse, error)
	Agents           func(ctx context.Context) (api.AgentsResponse, error)
}

type SendIn struct {
	To   string `json:"to" jsonschema:"who to message: a contact name such as alex, or telegram:<bot>:<chat>"`
	Text string `json:"text" jsonschema:"the message"`
	Via  string `json:"via,omitempty" jsonschema:"bot to send through; defaults to the contact's default bot"`
}

type SendOut struct {
	Target string `json:"target"`
}

type AskIn struct {
	Agent   string `json:"agent" jsonschema:"agent to ask; see list_agents"`
	Prompt  string `json:"prompt" jsonschema:"the question or task, with all the context the other agent needs"`
	Session string `json:"session,omitempty" jsonschema:"name to continue an earlier conversation with that agent; empty starts a fresh one"`
}

type AskOut struct {
	Agent    string `json:"agent"`
	Provider string `json:"provider"`
	Answer   string `json:"answer"`
}

type CrossReviewIn struct {
	Agent    string `json:"agent" jsonschema:"agent whose workspace holds the code to review; see list_agents"`
	Provider string `json:"provider" jsonschema:"reviewer provider to run with no fallback, e.g. claude, codex, or antigravity"`
	Prompt   string `json:"prompt" jsonschema:"what to review: repo path or branch, diff/commit range, context, and focus areas"`
	Label    string `json:"label,omitempty" jsonschema:"optional short label shown in the Running tab, e.g. pr-42 or auth-refactor"`
	Session  string `json:"session,omitempty" jsonschema:"optional review session name to continue a multi-round review with the same reviewer"`
}

type CrossReviewOut struct {
	Agent    string `json:"agent"`
	Provider string `json:"provider"`
	Review   string `json:"review"`
}

// BackgroundJob is the app job name for work handed off to its own session.
const BackgroundJob = store.BackgroundJob

type HandOffIn struct {
	Session string `json:"session,omitempty" jsonschema:"separate named work session; use a unique name for background work so it does not occupy the destination chat"`
	Agent   string `json:"agent" jsonschema:"agent to hand the task to; see list_agents"`
	Prompt  string `json:"prompt" jsonschema:"the task, with all the context the other agent needs"`
	To      string `json:"to" jsonschema:"contact who should receive that agent's reply, e.g. alex"`
}

type HandOffOut struct {
	TaskID string `json:"task_id,omitempty"`
	Queued bool   `json:"queued"`
	Target string `json:"target"`
}

// NewServer returns the MCP server with ganclaw's agent tools.
func NewServer(d Deps, version string) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "ganclaw", Version: version}, nil)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_agents",
		Description: "List the other agents you can ask or hand work to, and the contacts you can message.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, api.AgentsResponse, error) {
		out, err := d.Agents(ctx)
		return nil, out, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "send_message",
		Description: "Send a plain message to a person through a chat bot. Use for notifications; the message is delivered as written.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in SendIn) (*mcp.CallToolResult, SendOut, error) {
		if in.To == "" || in.Text == "" {
			return nil, SendOut{}, errors.New("to and text are required")
		}
		resp, err := d.Send(ctx, api.SendRequest{To: in.To, Via: in.Via, Text: in.Text})
		return nil, SendOut{Target: resp.Target}, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "ask_agent",
		Description: "Ask another agent a question or give it a task, and wait for its answer (can take minutes). Nothing is sent to anyone; the answer comes back to you.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in AskIn) (*mcp.CallToolResult, AskOut, error) {
		if in.Agent == "" || in.Prompt == "" {
			return nil, AskOut{}, errors.New("agent and prompt are required")
		}
		resp, err := d.Run(ctx, api.RunRequest{Agent: in.Agent, Prompt: in.Prompt, Session: in.Session, Source: "ask"})
		return nil, AskOut{Agent: in.Agent, Provider: resp.Provider, Answer: resp.Text}, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "cross_review",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		Description: "Run a synchronous, read-only cross-model code review in an agent's workspace using a pinned provider (claude, codex, or antigravity) with no fallback and MCP tools disabled. Waits inside your turn (sharing your turn time budget) and returns the review directly while showing live progress in the Running tab. Fails instead of substituting another provider if the requested provider is unavailable.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in CrossReviewIn) (*mcp.CallToolResult, CrossReviewOut, error) {
		if d.Run == nil {
			return nil, CrossReviewOut{}, errors.New("agent runs unavailable")
		}
		agentName := strings.TrimSpace(in.Agent)
		prov := strings.TrimSpace(in.Provider)
		prompt := strings.TrimSpace(in.Prompt)
		label := strings.TrimSpace(in.Label)
		session := strings.TrimSpace(in.Session)
		if agentName == "" || prov == "" || prompt == "" {
			return nil, CrossReviewOut{}, errors.New("agent, provider, and prompt are required")
		}
		runLabel := prov
		if label != "" {
			runLabel = prov + ": " + label
		}
		resp, err := d.Run(ctx, api.RunRequest{
			Agent:    agentName,
			Prompt:   prompt,
			Session:  session,
			Provider: prov,
			ReadOnly: true,
			Source:   "review",
			Label:    runLabel,
		})
		if err != nil {
			return nil, CrossReviewOut{}, err
		}
		return nil, CrossReviewOut{Agent: agentName, Provider: resp.Provider, Review: resp.Text}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "hand_off",
		Description: "Hand a task to another agent; it works in the background and sends its reply straight to the contact. Returns immediately with queued=true and task_id; queued does not mean running. Use task_status for later status questions.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in HandOffIn) (*mcp.CallToolResult, HandOffOut, error) {
		if in.Agent == "" || in.Prompt == "" || in.To == "" {
			return nil, HandOffOut{}, errors.New("agent, prompt and to are required")
		}
		req := api.RunRequest{Agent: in.Agent, Prompt: in.Prompt, To: in.To, Session: in.Session, Async: true, Source: "handoff"}
		if in.Session != "" {
			// One "background" job in the app, not one per task; it also
			// tells drive mode the result isn't an answer to a question.
			req.Job = BackgroundJob
		}
		resp, err := d.Run(ctx, req)
		return nil, HandOffOut{TaskID: resp.TaskID, Queued: resp.Queued, Target: resp.Target}, err
	})
	mcp.AddTool(s, &mcp.Tool{Name: "task_status", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}, Description: "Read background task status by task_id. Omit task_id to discover the latest 50 tasks, optionally filtered by agent. Queued means waiting; running means the router is handling the turn, not continuous activity. Progress is the latest observed safe provider event, not reasoning or a guarantee of responsiveness. Completed includes reply delivery; failed after restart means outcome unknown."}, func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
		TaskID string `json:"task_id,omitempty"`
		Agent  string `json:"agent,omitempty"`
	}) (*mcp.CallToolResult, api.TasksResponse, error) {
		if d.Tasks == nil {
			return nil, api.TasksResponse{}, errors.New("task tracking unavailable")
		}
		out, err := d.Tasks(ctx, in.TaskID, in.Agent)
		return nil, out, err
	})
	mcp.AddTool(s, &mcp.Tool{Name: "list_open_items", Description: "List persistent todos and external dependencies. Use done=true to find completed items.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
		Done bool `json:"done,omitempty"`
	}) (*mcp.CallToolResult, struct {
		Items []store.OpenItem `json:"items"`
	}, error) {
		var out struct {
			Items []store.OpenItem `json:"items"`
		}
		if d.OpenItems == nil {
			return nil, out, errors.New("open items unavailable")
		}
		var err error
		out.Items, err = d.OpenItems(ctx, in.Done)
		return nil, out, err
	})
	mcp.AddTool(s, &mcp.Tool{Name: "save_open_item", Description: "Create or update a persistent next step. Use state todo, waiting, or done; waiting requires waiting_on. Optional agent links its conversation; follow_up is YYYY-MM-DD, a displayed date, not an automatic reminder. Omit id to create (title required). To update, pass id plus only the fields to change: omitted fields keep their current values and an empty string clears one. Track concrete user actions and external dependencies; list first and keep existing items current rather than creating duplicates."}, func(ctx context.Context, _ *mcp.CallToolRequest, in SaveOpenItemIn) (*mcp.CallToolResult, store.OpenItem, error) {
		if d.SaveOpenItem == nil || d.OpenItem == nil {
			return nil, store.OpenItem{}, errors.New("open items unavailable")
		}
		var item store.OpenItem
		if in.ID != "" {
			var err error
			if item, err = d.OpenItem(ctx, in.ID); err != nil {
				return nil, store.OpenItem{}, err
			}
		}
		in.apply(&item)
		out, err := d.SaveOpenItem(ctx, item)
		return nil, out, err
	})
	mcp.AddTool(s, &mcp.Tool{
		Name:        "schedule_followup",
		Description: "Schedule a one-shot delayed checkback that wakes an agent after a delay and delivers its reply to a chat. Use this when waiting on external work (CI runs, builds, deploys, TestFlight) instead of holding a turn open or ending without a wakeup. Defaults to quiet=true so a check that finds work still in progress can schedule another follow-up and reply NO_REPLY without notifying the user.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in ScheduleFollowupIn) (*mcp.CallToolResult, store.Followup, error) {
		if d.ScheduleFollowup == nil {
			return nil, store.Followup{}, errors.New("follow-ups unavailable")
		}
		if in.Agent == "" || in.To == "" || in.Prompt == "" || in.Delay == "" {
			return nil, store.Followup{}, errors.New("agent, to, prompt, and delay are required")
		}
		delay, err := followup.ParseDelay(in.Delay)
		if err != nil {
			return nil, store.Followup{}, err
		}
		out, err := d.ScheduleFollowup(ctx, api.FollowupRequest{
			Agent:        in.Agent,
			To:           in.To,
			Via:          in.Via,
			Session:      in.Session,
			Prompt:       in.Prompt,
			DelaySeconds: int(delay.Seconds()),
			Quiet:        in.Quiet,
		})
		return nil, out, err
	})
	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_followups",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		Description: "List scheduled follow-up checkbacks. Returns pending follow-ups by default; set all=true to include recent completed, failed, and cancelled ones.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
		Agent string `json:"agent,omitempty" jsonschema:"optional agent name filter"`
		All   bool   `json:"all,omitempty" jsonschema:"include recent completed, failed, and cancelled follow-ups"`
	}) (*mcp.CallToolResult, api.FollowupsResponse, error) {
		if d.Followups == nil {
			return nil, api.FollowupsResponse{}, errors.New("follow-ups unavailable")
		}
		items, err := d.Followups(ctx, in.Agent, in.All)
		return nil, api.FollowupsResponse{Followups: items}, err
	})
	mcp.AddTool(s, &mcp.Tool{
		Name:        "cancel_followup",
		Description: "Cancel a pending scheduled follow-up by ID.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
		ID string `json:"id" jsonschema:"ID of the pending follow-up to cancel"`
	}) (*mcp.CallToolResult, store.Followup, error) {
		if d.CancelFollowup == nil {
			return nil, store.Followup{}, errors.New("follow-ups unavailable")
		}
		if in.ID == "" {
			return nil, store.Followup{}, errors.New("id is required")
		}
		out, err := d.CancelFollowup(ctx, in.ID)
		return nil, out, err
	})
	return s
}

type ScheduleFollowupIn struct {
	Agent   string `json:"agent" jsonschema:"agent to wake up; see list_agents"`
	To      string `json:"to" jsonschema:"recipient chat: a contact name such as alex, app:<agent>, or telegram:<bot>:<chat>"`
	Via     string `json:"via,omitempty" jsonschema:"bot override when addressing a contact"`
	Session string `json:"session,omitempty" jsonschema:"optional separate named session; leave empty to continue the destination chat's conversation"`
	Delay   string `json:"delay" jsonschema:"how long to wait before checking back, e.g. 30s, 2m, 1h, 2d (between 30s and 7d)"`
	Prompt  string `json:"prompt" jsonschema:"what to check and report when waking up; include IDs, repo, and context"`
	Quiet   *bool  `json:"quiet,omitempty" jsonschema:"suppress NO_REPLY answers and typing/streaming; defaults to true"`
}

// SaveOpenItemIn uses pointers so an update can tell omitted fields (kept)
// from empty ones (cleared).
type SaveOpenItemIn struct {
	ID        string  `json:"id,omitempty"`
	Title     *string `json:"title,omitempty"`
	State     *string `json:"state,omitempty"`
	WaitingOn *string `json:"waiting_on,omitempty"`
	Agent     *string `json:"agent,omitempty"`
	FollowUp  *string `json:"follow_up,omitempty"`
}

func (in SaveOpenItemIn) apply(item *store.OpenItem) {
	for _, f := range []struct {
		src *string
		dst *string
	}{{in.Title, &item.Title}, {in.State, &item.State}, {in.WaitingOn, &item.WaitingOn}, {in.Agent, &item.Agent}, {in.FollowUp, &item.FollowUp}} {
		if f.src != nil {
			*f.dst = *f.src
		}
	}
}
