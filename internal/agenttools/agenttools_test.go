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
	"github.com/kyleparisi/ganclaw/internal/store"
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
	t.Run("Lists the available tools", func(t *testing.T) {
		cs := connect(t, Deps{})

		res, err := cs.ListTools(context.Background(), nil)

		require.NoError(t, err)
		var names []string
		for _, tl := range res.Tools {
			names = append(names, tl.Name)
		}
		assert.ElementsMatch(t, []string{"list_agents", "send_message", "ask_agent", "cross_review", "hand_off", "task_status", "list_open_items", "save_open_item", "schedule_followup", "list_followups", "cancel_followup"}, names)
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
			assert.Equal(t, api.RunRequest{Agent: "support", Prompt: "How many signups today?", Session: "daily", Source: "ask"}, req)
			return api.RunResponse{OK: true, Provider: "claude", Text: "12 signups."}, nil
		}})

		out, isErr := call(t, cs, "ask_agent", map[string]any{"agent": "support", "prompt": "How many signups today?", "session": "daily"})

		assert.False(t, isErr)
		assert.Equal(t, "12 signups.", out["answer"])
		assert.Equal(t, "claude", out["provider"])
	})

	t.Run("cross_review runs a read-only pinned review and formats its label", func(t *testing.T) {
		var gotReqs []api.RunRequest
		cs := connect(t, Deps{Run: func(ctx context.Context, req api.RunRequest) (api.RunResponse, error) {
			gotReqs = append(gotReqs, req)
			return api.RunResponse{OK: true, Provider: req.Provider, Text: "No high severity findings."}, nil
		}})

		out, isErr := call(t, cs, "cross_review", map[string]any{
			"agent":    "support",
			"provider": "claude",
			"prompt":   "Review branch feat/auth",
			"label":    "auth-pr",
			"session":  "auth-round-1",
		})
		assert.False(t, isErr)
		assert.Equal(t, "support", out["agent"])
		assert.Equal(t, "claude", out["provider"])
		assert.Equal(t, "No high severity findings.", out["review"])

		_, isErr = call(t, cs, "cross_review", map[string]any{
			"agent":    " support ",
			"provider": " codex ",
			"prompt":   " Review latest commit ",
		})
		assert.False(t, isErr)

		require.Len(t, gotReqs, 2)
		assert.Equal(t, api.RunRequest{
			Agent:    "support",
			Prompt:   "Review branch feat/auth",
			Session:  "auth-round-1",
			Provider: "claude",
			ReadOnly: true,
			Source:   "review",
			Label:    "claude: auth-pr",
		}, gotReqs[0])
		assert.Equal(t, api.RunRequest{
			Agent:    "support",
			Prompt:   "Review latest commit",
			Provider: "codex",
			ReadOnly: true,
			Source:   "review",
			Label:    "codex",
		}, gotReqs[1])
	})

	t.Run("cross_review rejects missing required fields", func(t *testing.T) {
		cs := connect(t, Deps{Run: func(ctx context.Context, req api.RunRequest) (api.RunResponse, error) {
			t.Fatal("invalid request must not reach the gateway")
			return api.RunResponse{}, nil
		}})

		_, isErr1 := call(t, cs, "cross_review", map[string]any{"agent": "support", "provider": "", "prompt": "review"})
		_, isErr2 := call(t, cs, "cross_review", map[string]any{"agent": "", "provider": "claude", "prompt": "review"})
		_, isErr3 := call(t, cs, "cross_review", map[string]any{"agent": "support", "provider": "claude", "prompt": "   "})

		assert.True(t, isErr1)
		assert.True(t, isErr2)
		assert.True(t, isErr3)
	})

	t.Run("hand_off queues a delivered run", func(t *testing.T) {
		cs := connect(t, Deps{Run: func(ctx context.Context, req api.RunRequest) (api.RunResponse, error) {
			assert.Equal(t, api.RunRequest{Agent: "sales", Prompt: "Draft a reply to this lead", To: "alex", Async: true, Source: "handoff"}, req)
			return api.RunResponse{OK: true, Queued: true, TaskID: "task-1", Target: "telegram:sales:111"}, nil
		}})

		out, isErr := call(t, cs, "hand_off", map[string]any{"agent": "sales", "prompt": "Draft a reply to this lead", "to": "alex"})

		assert.False(t, isErr)
		assert.Equal(t, true, out["queued"])
		assert.Equal(t, "task-1", out["task_id"])
	})

	t.Run("hand_off can isolate background work from the app conversation", func(t *testing.T) {
		cs := connect(t, Deps{Run: func(ctx context.Context, req api.RunRequest) (api.RunResponse, error) {
			assert.Equal(t, api.RunRequest{Agent: "support", Prompt: "Investigate the alert", To: "app:support", Session: "drive-task-1", Job: "background", Async: true, Source: "handoff"}, req)
			return api.RunResponse{OK: true, Queued: true, Target: "app:support"}, nil
		}})
		_, isErr := call(t, cs, "hand_off", map[string]any{"agent": "support", "prompt": "Investigate the alert", "to": "app:support", "session": "drive-task-1"})
		assert.False(t, isErr)
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

func TestTaskStatus(t *testing.T) {
	subject := connect
	t.Run("Passes task lookup to the read only API", func(t *testing.T) {
		cs := subject(t, Deps{Tasks: func(_ context.Context, id, agent string) (api.TasksResponse, error) {
			assert.Equal(t, "task-1", id)
			assert.Empty(t, agent)
			return api.TasksResponse{}, nil
		}})
		_, isErr := call(t, cs, "task_status", map[string]any{"task_id": "task-1"})
		assert.False(t, isErr)
	})
}

func TestOpenItemTools(t *testing.T) {
	subject := connect
	t.Run("Creates items and lists them", func(t *testing.T) {
		var saved store.OpenItem
		cs := subject(t, Deps{
			OpenItem: func(context.Context, string) (store.OpenItem, error) {
				t.Fatal("create must not load an existing item")
				return store.OpenItem{}, nil
			},
			SaveOpenItem: func(ctx context.Context, in store.OpenItem) (store.OpenItem, error) {
				saved = in
				in.ID = "item-1"
				return in, nil
			},
			OpenItems: func(ctx context.Context, done bool) ([]store.OpenItem, error) {
				assert.False(t, done)
				return []store.OpenItem{saved}, nil
			},
		})
		out, failed := call(t, cs, "save_open_item", map[string]any{"title": "Review draft", "state": "waiting", "waiting_on": "alex", "agent": "support", "follow_up": "2026-12-01"})
		assert.False(t, failed)
		assert.Equal(t, "item-1", out["id"])
		assert.Equal(t, store.OpenItem{Title: "Review draft", State: "waiting", WaitingOn: "alex", Agent: "support", FollowUp: "2026-12-01"}, saved)
		out, failed = call(t, cs, "list_open_items", map[string]any{})
		assert.False(t, failed)
		assert.Len(t, out["items"], 1)
	})
	t.Run("Updates keep omitted fields and clear empty ones", func(t *testing.T) {
		existing := store.OpenItem{ID: "item-1", Title: "Review draft", State: "waiting", WaitingOn: "alex", Agent: "support", FollowUp: "2026-12-01", Version: 3}
		var saved store.OpenItem
		cs := subject(t, Deps{
			OpenItem: func(ctx context.Context, id string) (store.OpenItem, error) {
				assert.Equal(t, "item-1", id)
				return existing, nil
			},
			SaveOpenItem: func(ctx context.Context, in store.OpenItem) (store.OpenItem, error) {
				saved = in
				return in, nil
			},
		})
		_, failed := call(t, cs, "save_open_item", map[string]any{"id": "item-1", "title": "Review final draft", "follow_up": ""})
		assert.False(t, failed)
		want := existing
		want.Title, want.FollowUp = "Review final draft", ""
		assert.Equal(t, want, saved, "version is carried so a concurrent edit is rejected")
	})
}

func TestFollowupTools(t *testing.T) {
	subject := connect

	t.Run("Schedules, lists, and cancels follow-ups", func(t *testing.T) {
		var gotReq api.FollowupRequest
		cs := subject(t, Deps{
			ScheduleFollowup: func(ctx context.Context, req api.FollowupRequest) (store.Followup, error) {
				gotReq = req
				return store.Followup{ID: "f-1", Agent: req.Agent, To: "app:support", Prompt: req.Prompt, Quiet: true, State: store.FollowupPending}, nil
			},
			Followups: func(ctx context.Context, agent string, all bool) ([]store.Followup, error) {
				assert.Equal(t, "support", agent)
				assert.True(t, all)
				return []store.Followup{{ID: "f-1", Agent: "support", State: store.FollowupPending}}, nil
			},
			CancelFollowup: func(ctx context.Context, id string) (store.Followup, error) {
				assert.Equal(t, "f-1", id)
				return store.Followup{ID: "f-1", State: store.FollowupCancelled}, nil
			},
		})

		out, isErr := call(t, cs, "schedule_followup", map[string]any{
			"agent":  "support",
			"to":     "app:support",
			"delay":  "2m30s",
			"prompt": "Check GitHub Actions run 42",
		})
		assert.False(t, isErr)
		assert.Equal(t, "f-1", out["id"])
		assert.Equal(t, api.FollowupRequest{
			Agent:        "support",
			To:           "app:support",
			Prompt:       "Check GitHub Actions run 42",
			DelaySeconds: 150,
		}, gotReq)

		_, isErr = call(t, cs, "schedule_followup", map[string]any{
			"agent":  "support",
			"to":     "app:support",
			"delay":  "1m",
			"prompt": "Always notify",
			"quiet":  false,
		})
		assert.False(t, isErr)
		require.NotNil(t, gotReq.Quiet)
		assert.False(t, *gotReq.Quiet)

		listed, isErr := call(t, cs, "list_followups", map[string]any{"agent": "support", "all": true})
		assert.False(t, isErr)
		assert.Len(t, listed["followups"], 1)

		cancelled, isErr := call(t, cs, "cancel_followup", map[string]any{"id": "f-1"})
		assert.False(t, isErr)
		assert.Equal(t, store.FollowupCancelled, cancelled["state"])
	})

	t.Run("Rejects invalid delay and missing required arguments", func(t *testing.T) {
		cs := subject(t, Deps{
			ScheduleFollowup: func(context.Context, api.FollowupRequest) (store.Followup, error) {
				t.Fatal("invalid request must not reach the gateway")
				return store.Followup{}, nil
			},
			CancelFollowup: func(context.Context, string) (store.Followup, error) {
				t.Fatal("empty id must not reach the gateway")
				return store.Followup{}, nil
			},
		})

		_, isErr1 := call(t, cs, "schedule_followup", map[string]any{"agent": "support", "to": "app:support", "delay": "2s", "prompt": "too soon"})
		_, isErr2 := call(t, cs, "schedule_followup", map[string]any{"agent": "support", "to": "app:support", "delay": "1m", "prompt": ""})
		_, isErr3 := call(t, cs, "cancel_followup", map[string]any{"id": ""})

		assert.True(t, isErr1)
		assert.True(t, isErr2)
		assert.True(t, isErr3)
	})
}
