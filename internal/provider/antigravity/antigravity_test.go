package antigravity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/kyleparisi/ganclaw/internal/procexec"
	"github.com/kyleparisi/ganclaw/internal/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRun(t *testing.T) {
	subject := New
	t.Run("Streams a response and resumes its session", func(t *testing.T) {
		p := subject(Config{Environ: func() []string { return []string{"HOME=" + t.TempDir(), "PATH=/usr/bin:/bin"} }, Exec: procexec.Exec{Start: func(ctx context.Context, cmd procexec.Cmd) (*procexec.Proc, error) {
			assert.Equal(t, "agy", cmd.Bin)
			assert.Contains(t, cmd.Args, "acp")
			inReader, inWriter := io.Pipe()
			outReader, outWriter := io.Pipe()
			go func() {
				defer outWriter.Close()
				dec := json.NewDecoder(inReader)
				enc := json.NewEncoder(outWriter)
				for {
					var m struct {
						ID     int            `json:"id"`
						Method string         `json:"method"`
						Params map[string]any `json:"params"`
					}
					if dec.Decode(&m) != nil {
						return
					}
					result := map[string]any{}
					switch m.Method {
					case "session/resume":
						assert.Equal(t, "session-1", m.Params["sessionId"])
					case "session/prompt":
						_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "Hi"}}}})
						_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"update": map[string]any{"sessionUpdate": "tool_call"}}})
						_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "Final"}}}})
						result["stopReason"] = "end_turn"
					}
					_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": result})
				}
			}()
			return &procexec.Proc{Stdin: inWriter, Stdout: outReader, Wait: func() error { return nil }}, nil
		}}})
		var text string
		res, err := p.Run(context.Background(), provider.Request{Cwd: t.TempDir(), Prompt: "hello", Session: "session-1", OnDelta: func(s string) { text += s }})
		require.NoError(t, err)
		assert.Equal(t, provider.Result{Session: "session-1", Text: "Final"}, res)
		assert.Equal(t, "Final", text)
	})
	t.Run("Marks an empty response unavailable so the chain falls back", func(t *testing.T) {
		p := subject(Config{Environ: func() []string { return []string{"HOME=" + t.TempDir(), "PATH=/usr/bin:/bin"} }, Exec: procexec.Exec{Start: func(ctx context.Context, cmd procexec.Cmd) (*procexec.Proc, error) {
			inReader, inWriter := io.Pipe()
			outReader, outWriter := io.Pipe()
			go func() {
				defer outWriter.Close()
				dec := json.NewDecoder(inReader)
				enc := json.NewEncoder(outWriter)
				for {
					var m struct {
						ID     int    `json:"id"`
						Method string `json:"method"`
					}
					if dec.Decode(&m) != nil {
						return
					}
					result := map[string]any{}
					switch m.Method {
					case "session/new":
						result["sessionId"] = "session-2"
					case "session/prompt":
						result["stopReason"] = "end_turn"
					}
					_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": result})
				}
			}()
			return &procexec.Proc{Stdin: inWriter, Stdout: outReader, Wait: func() error { return nil }}, nil
		}}})
		_, err := p.Run(context.Background(), provider.Request{Cwd: t.TempDir(), Prompt: "hello"})
		require.ErrorIs(t, err, provider.ErrUnavailable)
	})
	t.Run("Marks a failed start unavailable", func(t *testing.T) {
		p := subject(Config{Environ: func() []string { return []string{"HOME=" + t.TempDir(), "PATH=/usr/bin:/bin"} }, Exec: procexec.Exec{Start: func(context.Context, procexec.Cmd) (*procexec.Proc, error) { return nil, errors.New("missing") }}})
		_, err := p.Run(context.Background(), provider.Request{Cwd: t.TempDir()})
		require.ErrorIs(t, err, provider.ErrUnavailable)
	})

	t.Run("Appends turn lifecycle guidance and honors NoMCP in read-only mode", func(t *testing.T) {
		runOnce := func(t *testing.T, settings provider.Settings) (string, []any, string, string) {
			t.Helper()
			var gotPrompt, gotMode, gotDecision string
			var gotServers []any
			p := subject(Config{
				Mode: "auto",
				MCPServers: []MCPServer{
					{Name: "ganclaw", Command: "/bin/ganclaw", Args: []string{"mcp"}},
				},
				Environ: func() []string { return []string{"HOME=" + t.TempDir(), "PATH=/usr/bin:/bin"} },
				Exec: procexec.Exec{Start: func(ctx context.Context, cmd procexec.Cmd) (*procexec.Proc, error) {
					inReader, inWriter := io.Pipe()
					outReader, outWriter := io.Pipe()
					go func() {
						defer outWriter.Close()
						dec := json.NewDecoder(inReader)
						enc := json.NewEncoder(outWriter)
						for {
							var m struct {
								ID     int            `json:"id"`
								Method string         `json:"method"`
								Params map[string]any `json:"params"`
								Result map[string]any `json:"result"`
							}
							if dec.Decode(&m) != nil {
								return
							}
							if m.Method == "" {
								if outcome, ok := m.Result["outcome"].(map[string]any); ok {
									gotDecision, _ = outcome["optionId"].(string)
								}
								continue
							}
							result := map[string]any{}
							switch m.Method {
							case "session/new":
								result["sessionId"] = "session-3"
								gotServers, _ = m.Params["mcpServers"].([]any)
								if meta, ok := m.Params["_meta"].(map[string]any); ok {
									if ag, ok := meta["antigravity"].(map[string]any); ok {
										if sp, ok := ag["systemPrompt"].(map[string]any); ok {
											gotPrompt, _ = sp["append"].(string)
										}
									}
								}
							case "session/set_config_option":
								if m.Params["configId"] == "mode" {
									gotMode, _ = m.Params["value"].(string)
								}
							case "session/prompt":
								_ = enc.Encode(map[string]any{
									"jsonrpc": "2.0",
									"id":      99,
									"method":  "session/request_permission",
									"params": map[string]any{
										"toolCall": map[string]any{"name": "run_command"},
										"options": []any{
											map[string]any{"optionId": "yes", "kind": "allow_once"},
											map[string]any{"optionId": "no", "kind": "reject_once"},
										},
									},
								})
								var resp struct {
									Result map[string]any `json:"result"`
								}
								if dec.Decode(&resp) == nil {
									if outcome, ok := resp.Result["outcome"].(map[string]any); ok {
										gotDecision, _ = outcome["optionId"].(string)
									}
								}
								_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "OK"}}}})
								result["stopReason"] = "end_turn"
							}
							_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": result})
						}
					}()
					return &procexec.Proc{Stdin: inWriter, Stdout: outReader, Wait: func() error { return nil }}, nil
				}},
			})
			_, err := p.Run(context.Background(), provider.Request{Cwd: t.TempDir(), Instructions: "Base instructions.", Prompt: "check", Settings: settings})
			require.NoError(t, err)
			return gotPrompt, gotServers, gotMode, gotDecision
		}

		normalPrompt, normalServers, normalMode, normalDecision := runOnce(t, provider.Settings{})
		assert.Contains(t, normalPrompt, "Base instructions.")
		assert.Contains(t, normalPrompt, "Ending your turn")
		assert.Contains(t, normalPrompt, "cross_review")
		assert.Len(t, normalServers, 1)
		assert.Equal(t, "auto", normalMode)
		assert.Equal(t, "yes", normalDecision)

		roPrompt, roServers, roMode, roDecision := runOnce(t, provider.Settings{CodexSandbox: "read-only", AntigravityMode: "plan", NoMCP: true})
		assert.Contains(t, roPrompt, "Ending your turn")
		assert.NotContains(t, roPrompt, "cross_review")
		assert.NotContains(t, roPrompt, "MCP server names for this session:")
		assert.Empty(t, roServers)
		assert.Equal(t, "plan", roMode)
		assert.Equal(t, "no", roDecision)
	})
}

func TestPermissionRequests(t *testing.T) {
	subject := (*client).call
	t.Run("Approves only the configured session server", func(t *testing.T) {
		stream := `{"jsonrpc":"2.0","id":7,"method":"session/request_permission","params":{"toolCall":{"name":"mcp_ganclaw_nonce_example_tool"},"options":[{"optionId":"yes","kind":"allow_once"},{"optionId":"no","kind":"reject_once"}]}}
{"jsonrpc":"2.0","id":8,"method":"session/request_permission","params":{"toolCall":{"name":"mcp_other_tool"},"options":[{"optionId":"yes","kind":"allow_once"},{"optionId":"no","kind":"reject_once"}]}}
{"jsonrpc":"2.0","id":1,"result":{}}`
		var out bytes.Buffer
		c := &client{enc: json.NewEncoder(&out), dec: json.NewDecoder(strings.NewReader(stream)), servers: []MCPServer{{Name: "ganclaw_nonce_example"}}}
		_, err := subject(c, 1, "test", map[string]any{})
		require.NoError(t, err)
		dec := json.NewDecoder(&out)
		var request, allowed, denied map[string]any
		require.NoError(t, dec.Decode(&request))
		require.NoError(t, dec.Decode(&allowed))
		require.NoError(t, dec.Decode(&denied))
		assert.Equal(t, "yes", allowed["result"].(map[string]any)["outcome"].(map[string]any)["optionId"])
		assert.Equal(t, "no", denied["result"].(map[string]any)["outcome"].(map[string]any)["optionId"])
	})
	t.Run("Approves every tool when allowAll is set", func(t *testing.T) {
		stream := `{"jsonrpc":"2.0","id":8,"method":"session/request_permission","params":{"toolCall":{"name":"run_command"},"options":[{"optionId":"yes","kind":"allow_once"},{"optionId":"no","kind":"reject_once"}]}}
{"jsonrpc":"2.0","id":1,"result":{}}`
		var out bytes.Buffer
		c := &client{enc: json.NewEncoder(&out), dec: json.NewDecoder(strings.NewReader(stream)), allowAll: true}
		_, err := subject(c, 1, "test", map[string]any{})
		require.NoError(t, err)
		dec := json.NewDecoder(&out)
		var request, allowed map[string]any
		require.NoError(t, dec.Decode(&request))
		require.NoError(t, dec.Decode(&allowed))
		assert.Equal(t, "yes", allowed["result"].(map[string]any)["outcome"].(map[string]any)["optionId"])
	})
}
