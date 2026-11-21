package antigravity

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kyleparisi/ganclaw/internal/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLiveConversation(t *testing.T) {
	subject := New
	if os.Getenv("GANCLAW_ANTIGRAVITY_LIVE_TEST") != "1" {
		t.Skip("live Antigravity test not enabled")
	}
	t.Run("Starts and resumes a conversation", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		p := subject(Config{})
		cwd := t.TempDir()
		first, err := p.Run(ctx, provider.Request{Cwd: cwd, Prompt: "Remember the word banana. Reply with only that word. Do not use tools."})
		require.NoError(t, err)
		require.NotEmpty(t, first.Session)
		assert.Contains(t, first.Text, "banana")
		second, err := p.Run(ctx, provider.Request{Cwd: cwd, Session: first.Session, Prompt: "Reply only with the word I asked you to remember. Do not use tools."})
		require.NoError(t, err)
		assert.Equal(t, first.Session, second.Session)
		assert.Contains(t, second.Text, "banana")
	})
	t.Run("Runs shell commands under danger-full-access", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		cwd := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(cwd, "marker-7f3a.txt"), []byte("x"), 0o600))
		p := subject(Config{Sandbox: "danger-full-access"})
		res, err := p.Run(ctx, provider.Request{Cwd: cwd, Prompt: "Run `ls` in the current directory with your shell tool and reply with the file name you see."})
		require.NoError(t, err)
		assert.Contains(t, res.Text, "marker-7f3a")
	})
	t.Run("Calls a configured MCP tool", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		root := t.TempDir()
		cwd := filepath.Join(root, "work")
		require.NoError(t, os.Mkdir(cwd, 0o700))
		script := `import sys,json
for line in sys.stdin:
 try:
  m=json.loads(line); i=m.get("id"); k=m.get("method")
  if i is None: continue
  if k=="initialize": r={"protocolVersion":"2024-11-05","capabilities":{"tools":{}},"serverInfo":{"name":"example","version":"1"}}
  elif k=="tools/list": r={"tools":[{"name":"give_word","description":"Return the test word","inputSchema":{"type":"object","properties":{"request":{"type":"string","description":"Request the test word"}},"required":["request"]}}]}
  elif k=="tools/call": r={"content":[{"type":"text","text":"kiwifruit"}]}
  else: r={}
  print(json.dumps({"jsonrpc":"2.0","id":i,"result":r}),flush=True)
 except Exception: pass
`
		scriptPath := filepath.Join(cwd, "server.py")
		require.NoError(t, os.WriteFile(scriptPath, []byte(script), 0o600))

		p := subject(Config{MCPServers: []MCPServer{{Name: "example-services", Command: "/usr/bin/python3", Args: []string{scriptPath}}}})
		res, err := p.Run(ctx, provider.Request{Cwd: cwd, Prompt: "Call the available MCP tool named give_word, then reply only with the word it returns. Do not use shell commands."})
		require.NoError(t, err)
		assert.Contains(t, res.Text, "kiwifruit")
		resumed, err := p.Run(ctx, provider.Request{Cwd: cwd, Session: res.Session, Prompt: "Call the available MCP tool named give_word again, then reply only with the word it returns. Do not use shell commands."})
		require.NoError(t, err)
		assert.Equal(t, res.Session, resumed.Session)
		assert.Contains(t, resumed.Text, "kiwifruit")

	})

}
