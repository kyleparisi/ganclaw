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
)

// Deps are the gateway calls the tools make; *api.Client provides them.
type Deps struct {
	Send   func(ctx context.Context, req api.SendRequest) (api.SendResponse, error)
	Run    func(ctx context.Context, req api.RunRequest) (api.RunResponse, error)
	Agents func(ctx context.Context) (api.AgentsResponse, error)
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

type HandOffIn struct {
	Agent  string `json:"agent" jsonschema:"agent to hand the task to; see list_agents"`
	Prompt string `json:"prompt" jsonschema:"the task, with all the context the other agent needs"`
	To     string `json:"to" jsonschema:"contact who should receive that agent's reply, e.g. alex"`
}

type HandOffOut struct {
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
		Description: "Hand a task to another agent; it works in the background and sends its reply straight to the contact. Returns immediately.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in HandOffIn) (*mcp.CallToolResult, HandOffOut, error) {
		if in.Agent == "" || in.Prompt == "" || in.To == "" {
			return nil, HandOffOut{}, errors.New("agent, prompt and to are required")
		}
		resp, err := d.Run(ctx, api.RunRequest{Agent: in.Agent, Prompt: in.Prompt, To: in.To, Async: true})
		return nil, HandOffOut{Queued: resp.Queued, Target: resp.Target}, err
	})
	return s
}
