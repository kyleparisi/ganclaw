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
	t.Run("Marks a failed start unavailable", func(t *testing.T) {
		p := subject(Config{Environ: func() []string { return []string{"HOME=" + t.TempDir(), "PATH=/usr/bin:/bin"} }, Exec: procexec.Exec{Start: func(context.Context, procexec.Cmd) (*procexec.Proc, error) { return nil, errors.New("missing") }}})
		_, err := p.Run(context.Background(), provider.Request{Cwd: t.TempDir()})
		require.ErrorIs(t, err, provider.ErrUnavailable)
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
}
