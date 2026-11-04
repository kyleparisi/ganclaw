// Package agenttools is an MCP server that gives agents ganclaw's own
// abilities: messaging people and asking or handing work to other agents.
// It runs as `ganclaw mcp`, a child of codex/claude, and talks to the
// gateway over its API socket.
package agenttools

import (
	"context"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kyleparisi/ganclaw/internal/api"
	"github.com/kyleparisi/ganclaw/internal/store"
)

// Deps are the gateway calls the tools make; *api.Client provides them.
type Deps struct {
	OpenItems    func(context.Context, bool) ([]store.OpenItem, error)
	OpenItem     func(context.Context, string) (store.OpenItem, error)
	SaveOpenItem func(context.Context, store.OpenItem) (store.OpenItem, error)
	Tasks        func(context.Context, string, string) (api.TasksResponse, error)
	Send         func(ctx context.Context, req api.SendRequest) (api.SendResponse, error)
	Run          func(ctx context.Context, req api.RunRequest) (api.RunResponse, error)
	Agents       func(ctx context.Context) (api.AgentsResponse, error)
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
		resp, err := d.Run(ctx, api.RunRequest{Agent: in.Agent, Prompt: in.Prompt, Session: in.Session})
		return nil, AskOut{Agent: in.Agent, Provider: resp.Provider, Answer: resp.Text}, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "hand_off",
		Description: "Hand a task to another agent; it works in the background and sends its reply straight to the contact. Returns immediately with queued=true and task_id; queued does not mean running. Use task_status for later status questions.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in HandOffIn) (*mcp.CallToolResult, HandOffOut, error) {
		if in.Agent == "" || in.Prompt == "" || in.To == "" {
			return nil, HandOffOut{}, errors.New("agent, prompt and to are required")
		}
		req := api.RunRequest{Agent: in.Agent, Prompt: in.Prompt, To: in.To, Session: in.Session, Async: true}
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
	return s
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
