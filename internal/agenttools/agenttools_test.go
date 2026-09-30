package agenttools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kyleparisi/ganclaw/internal/api"
)

// connect runs the server in memory and returns a client session.
func connect(t *testing.T, d Deps) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	st, ct := mcp.NewInMemoryTransports()
	go func() { _ = NewServer(d, "test").Run(ctx, st) }()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil).Connect(ctx, ct, nil)
	require.NoError(t, err)
	t.Cleanup(func() { cs.Close() })
	return cs
}

func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (map[string]any, bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	require.NoError(t, err)
	var out map[string]any
	if res.StructuredContent != nil {
		b, _ := json.Marshal(res.StructuredContent)
		_ = json.Unmarshal(b, &out)
	}
	return out, res.IsError
}

func TestTools(t *testing.T) {
	t.Run("Lists the four tools", func(t *testing.T) {
		cs := connect(t, Deps{})

		res, err := cs.ListTools(context.Background(), nil)

		require.NoError(t, err)
		var names []string
		for _, tl := range res.Tools {
			names = append(names, tl.Name)
		}
		assert.ElementsMatch(t, []string{"list_agents", "send_message", "ask_agent", "hand_off"}, names)
	})

	t.Run("send_message sends through the API", func(t *testing.T) {
		cs := connect(t, Deps{Send: func(ctx context.Context, req api.SendRequest) (api.SendResponse, error) {
			assert.Equal(t, api.SendRequest{To: "alex", Text: "invoice paid", Via: "support"}, req)
			return api.SendResponse{OK: true, Target: "telegram:support:111"}, nil
		}})

		out, isErr := call(t, cs, "send_message", map[string]any{"to": "alex", "text": "invoice paid", "via": "support"})

		assert.False(t, isErr)
		assert.Equal(t, "telegram:support:111", out["target"])
	})

	t.Run("ask_agent waits for the answer and delivers nothing", func(t *testing.T) {
		cs := connect(t, Deps{Run: func(ctx context.Context, req api.RunRequest) (api.RunResponse, error) {
			assert.Equal(t, api.RunRequest{Agent: "support", Prompt: "How many signups today?", Session: "daily"}, req)
			return api.RunResponse{OK: true, Provider: "claude", Text: "12 signups."}, nil
		}})

		out, isErr := call(t, cs, "ask_agent", map[string]any{"agent": "support", "prompt": "How many signups today?", "session": "daily"})

		assert.False(t, isErr)
		assert.Equal(t, "12 signups.", out["answer"])
		assert.Equal(t, "claude", out["provider"])
	})

	t.Run("hand_off queues a delivered run", func(t *testing.T) {
		cs := connect(t, Deps{Run: func(ctx context.Context, req api.RunRequest) (api.RunResponse, error) {
			assert.Equal(t, api.RunRequest{Agent: "sales", Prompt: "Draft a reply to this lead", To: "alex", Async: true}, req)
			return api.RunResponse{OK: true, Queued: true, Target: "telegram:sales:111"}, nil
		}})

		out, isErr := call(t, cs, "hand_off", map[string]any{"agent": "sales", "prompt": "Draft a reply to this lead", "to": "alex"})

		assert.False(t, isErr)
		assert.Equal(t, true, out["queued"])
	})

	t.Run("list_agents returns agents and contacts", func(t *testing.T) {
		cs := connect(t, Deps{Agents: func(ctx context.Context) (api.AgentsResponse, error) {
			return api.AgentsResponse{Agents: []api.AgentInfo{{Name: "support", Bot: "support"}}, Contacts: []string{"alex"}}, nil
		}})

		out, isErr := call(t, cs, "list_agents", map[string]any{})

		assert.False(t, isErr)
		assert.Equal(t, []any{"alex"}, out["contacts"])
		assert.Len(t, out["agents"], 1)
	})

	t.Run("Missing arguments and API errors become tool errors", func(t *testing.T) {
		cs := connect(t, Deps{Run: func(ctx context.Context, req api.RunRequest) (api.RunResponse, error) {
			return api.RunResponse{}, errors.New("unknown_agent: no agent named \"ghost\"")
		}})

		_, isErr1 := call(t, cs, "send_message", map[string]any{"to": "alex", "text": ""})
		_, isErr2 := call(t, cs, "ask_agent", map[string]any{"agent": "ghost", "prompt": "hi"})
		_, isErr3 := call(t, cs, "hand_off", map[string]any{"agent": "support", "prompt": "hi"})

		assert.True(t, isErr1)
		assert.True(t, isErr2)
		assert.True(t, isErr3, "hand_off needs a recipient")
	})
}
