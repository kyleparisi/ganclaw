package appchannel

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/kyleparisi/ganclaw/internal/provider"
	"github.com/kyleparisi/ganclaw/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFreeTextReplyIncludesOriginalMessageReference(t *testing.T) {
	subject := newHarness
	t.Run("Replies carry the exact ID and quoted text without changing the user's stored text", func(t *testing.T) {
		h := subject(t, func(provider.Request) string { return "I will check that result." })
		h.pair()
		orig, err := h.ch.Store.AddAppMessage(context.Background(), store.AppMessage{
			Thread: "infra", Role: store.RoleAgent, Text: "The nightly check found two slow requests.",
		})
		require.NoError(t, err)
		var sent struct {
			Message store.AppMessage `json:"message"`
		}
		require.Equal(t, http.StatusOK, h.do("POST", "/app/v1/threads/infra/messages", map[string]any{
			"text": "Why were they slow?", "reply_to": orig.ID,
		}, &sent))
		h.waitDone("infra", 3)
		reqs := h.run.requests()
		require.Len(t, reqs, 1)
		assert.Contains(t, reqs[0].Prompt, "> The nightly check found two slow requests.")
		assert.Contains(t, reqs[0].Prompt, fmt.Sprintf(`[App message reference: thread="infra", id=%d]`, orig.ID))
		assert.True(t, strings.HasSuffix(reqs[0].Prompt, "Why were they slow?"))
		assert.Equal(t, orig.ID, sent.Message.ReplyTo)
		assert.Equal(t, "Why were they slow?", sent.Message.Text)
	})
	t.Run("A reply cannot quote a message from another agent thread", func(t *testing.T) {
		h := subject(t, func(provider.Request) string { t.Error("Rejected replies must not reach the bot"); return "" })
		h.pair()
		orig, err := h.ch.Store.AddAppMessage(context.Background(), store.AppMessage{
			Thread: "research", Role: store.RoleAgent, Text: "A private research result.",
		})
		require.NoError(t, err)
		assert.Equal(t, http.StatusBadRequest, h.do("POST", "/app/v1/threads/infra/messages", map[string]any{
			"text": "Explain", "reply_to": orig.ID,
		}, nil))
		assert.Empty(t, h.run.requests())
	})
}

func TestReplyContextIsBoundedAndIdentifiesAttachments(t *testing.T) {
	subject := withReply
	t.Run("Long Unicode messages are capped while retaining the pointer", func(t *testing.T) {
		prompt := subject(store.AppMessage{ID: 42, Thread: "support", Role: store.RoleUser,
			Text: strings.Repeat("界", maxReplyContext+20)}, "Continue")
		assert.Equal(t, maxReplyContext, strings.Count(prompt, "界"))
		assert.Contains(t, prompt, `[App message reference: thread="support", id=42]`)
		assert.Contains(t, prompt, "the user's earlier message")
	})
	t.Run("Replies to file-only messages identify the original attachment", func(t *testing.T) {
		prompt := subject(store.AppMessage{ID: 8, Thread: "support", Role: store.RoleAgent,
			Files: []store.AppFile{{Name: "summary.pdf"}}}, "Explain this")
		assert.Contains(t, prompt, `attachments: "summary.pdf"`)
		assert.Contains(t, prompt, `id=8]`)
	})
	t.Run("Replies to system notices are not attributed to the agent", func(t *testing.T) {
		prompt := subject(store.AppMessage{ID: 9, Thread: "support", Role: store.RoleSystem,
			Text: "I'm busy with earlier messages — please try again in a bit."}, "Retry")
		assert.Contains(t, prompt, "[Replying to an earlier system notice:]")
		assert.NotContains(t, prompt, "your earlier message")
	})
}
