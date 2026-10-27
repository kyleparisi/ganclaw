package claude

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestProgress(t *testing.T) {
	subject := parseStream
	t.Run("Reports safe top level activity and excludes thinking and nested events", func(t *testing.T) {
		input := `{"type":"stream_event","event":{"type":"content_block_start","content_block":{"type":"thinking","text":"private"}}}
{"type":"stream_event","parent_tool_use_id":"nested","event":{"type":"content_block_start","content_block":{"type":"tool_use"}}}
{"type":"stream_event","event":{"type":"content_block_start","content_block":{"type":"tool_use","input":{"secret":"private"}}}}
{"type":"stream_event","event":{"type":"content_block_start","content_block":{"type":"text","text":"private"}}}
{"type":"result","subtype":"success"}`
		var activity []string
		_, err := subject(strings.NewReader(input), nil, func(s string) { activity = append(activity, s) })
		require.NoError(t, err)
		assert.Equal(t, []string{"Tool activity observed", "Agent message activity observed"}, activity)
	})
}
