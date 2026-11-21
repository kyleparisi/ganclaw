package gemini

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseStream(t *testing.T) {
	subject := parseStream
	t.Run("Collects assistant messages and session", func(t *testing.T) {
		input := `{"type":"init","session_id":"session-1"}
{"type":"message","role":"user","content":"hello"}
{"type":"message","role":"assistant","content":"Hi","delta":true}
{"type":"tool_use","tool_name":"example"}
{"type":"message","role":"assistant","content":" there","delta":true}
{"type":"result","status":"success"}`
		var deltas, progress []string
		resets := 0
		st, err := subject(strings.NewReader(input), func(s string) { deltas = append(deltas, s) }, func(s string) { progress = append(progress, s) }, func() { resets++ })
		require.NoError(t, err)
		assert.Equal(t, "session-1", st.session)
		assert.Equal(t, " there", st.text.String())
		assert.Equal(t, 1, resets)
		assert.Equal(t, []string{"Hi", " there"}, deltas)
		assert.Equal(t, []string{"Tool activity observed"}, progress)
		assert.Equal(t, "success", st.status)
	})
	t.Run("Requires a final result", func(t *testing.T) {
		_, err := subject(strings.NewReader(`{"type":"init","session_id":"session-1"}`), nil, nil, nil)
		require.ErrorContains(t, err, "without a result")
	})
	t.Run("Retains error events even if result reports success", func(t *testing.T) {
		st, err := subject(strings.NewReader(`{"type":"error","severity":"error","message":"turn limit"}
{"type":"result","status":"success"}`), nil, nil, nil)
		require.NoError(t, err)
		assert.Equal(t, "turn limit", st.failure)
	})
	t.Run("Skips a plain-text notice before events", func(t *testing.T) {
		st, err := subject(strings.NewReader(`A notice
{"type":"result","status":"success"}`), nil, nil, nil)
		require.NoError(t, err)
		assert.Equal(t, "success", st.status)
	})

}
