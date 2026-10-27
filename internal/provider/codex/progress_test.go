package codex

import (
	"encoding/json"
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestProgress(t *testing.T) {
	subject := (*Provider).handleNotification
	t.Run("Reports item activity without raw content or reasoning", func(t *testing.T) {
		var activity []string
		p := &Provider{turns: map[string]*turnState{"thread-1": {phase: map[string]string{}, onProgress: func(s string) { activity = append(activity, s) }}}}
		subject(p, "item/started", json.RawMessage(`{"threadId":"thread-1","item":{"type":"commandExecution","command":"private"}}`))
		subject(p, "item/started", json.RawMessage(`{"threadId":"thread-1","item":{"type":"reasoning","text":"private"}}`))
		subject(p, "item/completed", json.RawMessage(`{"threadId":"thread-1","item":{"type":"agentMessage","id":"message-1","phase":"commentary","text":"private"}}`))
		assert.Equal(t, []string{"Command activity observed", "Agent message activity observed"}, activity)
	})
}
