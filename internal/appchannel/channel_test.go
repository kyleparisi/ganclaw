package appchannel

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kyleparisi/ganclaw/internal/agent"
	"github.com/kyleparisi/ganclaw/internal/provider"
	"github.com/kyleparisi/ganclaw/internal/router"
	"github.com/kyleparisi/ganclaw/internal/store"
)

// fakeRun answers every turn with reply(req), streaming it in two parts.
type fakeRun struct {
	mu    sync.Mutex
	reqs  []provider.Request
	reply func(provider.Request) string
}

func (f *fakeRun) run(ctx context.Context, sessions provider.Sessions, req provider.Request) (string, provider.Result, error) {
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	f.mu.Unlock()
	text := f.reply(req)
	if req.OnDelta != nil {
		half := len(text) / 2
		req.OnDelta(text[:half])
		req.OnDelta(text[half:])
	}
	sessions["fake"] = "s1"
	return "fake", provider.Result{Session: "s1", Text: text}, nil
}

func (f *fakeRun) requests() []provider.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]provider.Request{}, f.reqs...)
}

type harness struct {
	t     *testing.T
	ch    *Channel
	run   *fakeRun
	srv   *httptest.Server
	token string
	ws    string
}

func newHarness(t *testing.T, reply func(provider.Request) string) *harness {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	st := store.NewTestStore(t)
	ws := t.TempDir()
	agents := map[string]agent.Agent{"infra": {Name: "infra", Workspace: ws}, "research": {Name: "research", Workspace: t.TempDir()}}
	fr := &fakeRun{reply: reply}
	r := router.New(ctx, router.Config{Agents: agents, Store: st, Run: fr.run, TypingInterval: 10 * time.Millisecond})
	ch := &Channel{Agents: agents, Store: st, Submit: r.Submit, FilesDir: t.TempDir(), StreamInterval: time.Nanosecond}
	require.NoError(t, ch.Start(ctx))
	srv := httptest.NewServer(ch.Handler("test"))
	t.Cleanup(func() {
		srv.Close()
		cancel()
		r.Wait()
	})
	return &harness{t: t, ch: ch, run: fr, srv: srv, ws: ws}
}

func (h *harness) do(method, path string, body any, out any) int {
	h.t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		require.NoError(h.t, err)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, h.srv.URL+path, rd)
	require.NoError(h.t, err)
	if h.token != "" {
		req.Header.Set("Authorization", "Bearer "+h.token)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(h.t, err)
	defer resp.Body.Close()
	if out != nil {
		require.NoError(h.t, json.NewDecoder(resp.Body).Decode(out))
	}
	return resp.StatusCode
}

func (h *harness) pair() {
	h.t.Helper()
	code, _ := h.ch.NewPairCode("test phone")
	var resp pairResponse
	require.Equal(h.t, http.StatusOK, h.do("POST", "/app/v1/pair", map[string]string{"code": code}, &resp))
	require.True(h.t, strings.HasPrefix(resp.Token, "gca_"))
	h.token = resp.Token
}

func (h *harness) messages(agentName string) []store.AppMessage {
	h.t.Helper()
	var resp struct {
		Messages []store.AppMessage `json:"messages"`
	}
	require.Equal(h.t, http.StatusOK, h.do("GET", "/app/v1/threads/"+agentName+"/messages", nil, &resp))
	return resp.Messages
}

// waitDone polls until the thread's newest message is a finished agent
// reply.
func (h *harness) waitDone(agentName string, n int) []store.AppMessage {
	h.t.Helper()
	var msgs []store.AppMessage
	require.Eventually(h.t, func() bool {
		msgs = h.messages(agentName)
		return len(msgs) >= n && msgs[len(msgs)-1].Role != store.RoleUser && msgs[len(msgs)-1].Status != store.StatusStreaming
	}, 5*time.Second, 10*time.Millisecond)
	return msgs
}

func TestPairing(t *testing.T) {
	h := newHarness(t, func(provider.Request) string { return "hi" })

	assert.Equal(t, http.StatusUnauthorized, h.do("GET", "/app/v1/agents", nil, nil), "no token")
	h.token = "gca_wrong"
	assert.Equal(t, http.StatusUnauthorized, h.do("GET", "/app/v1/agents", nil, nil), "bad token")
	h.token = ""

	code, _ := h.ch.NewPairCode("phone")
	lower := strings.ToLower(strings.ReplaceAll(code, "-", ""))
	var resp pairResponse
	require.Equal(t, http.StatusOK, h.do("POST", "/app/v1/pair", map[string]string{"code": lower}, &resp), "codes ignore case and dash")
	assert.Equal(t, "phone", resp.Device.Name)
	assert.Equal(t, http.StatusUnauthorized, h.do("POST", "/app/v1/pair", map[string]string{"code": code}, nil), "codes work once")

	h.token = resp.Token
	assert.Equal(t, http.StatusOK, h.do("GET", "/app/v1/agents", nil, nil))

	devices, err := h.ch.Store.AppDevices(context.Background())
	require.NoError(t, err)
	require.Len(t, devices, 1)
	assert.NotNil(t, devices[0].LastSeen)
}

func TestPairCodeExpires(t *testing.T) {
	h := newHarness(t, func(provider.Request) string { return "hi" })
	now := time.Now()
	h.ch.Now = func() time.Time { return now }
	code, _ := h.ch.NewPairCode("phone")
	now = now.Add(11 * time.Minute)
	assert.Equal(t, http.StatusUnauthorized, h.do("POST", "/app/v1/pair", map[string]string{"code": code}, nil))
}

func TestSendAndReplyWithButtons(t *testing.T) {
	h := newHarness(t, func(req provider.Request) string {
		return "Drafted a fix.\nBUTTONS: Approve | Not now"
	})
	h.pair()

	var sent struct {
		Message store.AppMessage `json:"message"`
	}
	require.Equal(t, http.StatusOK, h.do("POST", "/app/v1/threads/infra/messages", map[string]string{"text": "What broke?"}, &sent))
	assert.Equal(t, store.RoleUser, sent.Message.Role)

	msgs := h.waitDone("infra", 2)
	reply := msgs[1]
	assert.Equal(t, "Drafted a fix.", reply.Text)
	assert.Equal(t, []string{"Approve", "Not now"}, reply.Actions)
	assert.Equal(t, store.StatusDone, reply.Status)

	var agents struct {
		Agents []AgentSummary `json:"agents"`
	}
	h.do("GET", "/app/v1/agents", nil, &agents)
	require.Len(t, agents.Agents, 2)
	assert.Equal(t, "infra", agents.Agents[0].Name)
	assert.True(t, agents.Agents[0].NeedsYou)
	assert.False(t, agents.Agents[0].State.Working)
	assert.Nil(t, agents.Agents[1].Last, "research has no messages")

	// Tapping a button answers the message and quotes it to the agent.
	require.Equal(t, http.StatusOK, h.do("POST", "/app/v1/threads/infra/messages", map[string]any{"text": "Approve", "reply_to": reply.ID}, nil))
	msgs = h.waitDone("infra", 4)
	assert.Equal(t, "Approve", msgs[1].Answer)
	reqs := h.run.requests()
	require.Len(t, reqs, 2)
	assert.Contains(t, reqs[1].Prompt, "[Replying to your earlier message:]\n> Drafted a fix.")
	assert.True(t, strings.HasSuffix(reqs[1].Prompt, "Approve"))
}

func TestVoiceMessage(t *testing.T) {
	h := newHarness(t, func(provider.Request) string { return "Yes, it's live." })
	h.pair()
	require.Equal(t, http.StatusOK, h.do("POST", "/app/v1/threads/infra/messages", map[string]any{"text": "Did the fix ship?", "voice": true}, nil))
	msgs := h.waitDone("infra", 2)
	assert.Equal(t, "Did the fix ship?", msgs[0].Text, "stored text has no instructions")
	reqs := h.run.requests()
	require.Len(t, reqs, 1)
	assert.True(t, strings.HasPrefix(reqs[0].Prompt, VoicePrefix))
	assert.Contains(t, reqs[0].Prompt, `to "app:infra"`)
	assert.Contains(t, reqs[0].Prompt, "unique nonempty session")
	assert.True(t, strings.HasSuffix(reqs[0].Prompt, "Did the fix ship?"))
}

func TestUnknownAgentAndBadRequests(t *testing.T) {
	h := newHarness(t, func(provider.Request) string { return "hi" })
	h.pair()
	assert.Equal(t, http.StatusNotFound, h.do("GET", "/app/v1/threads/nobody/messages", nil, nil))
	assert.Equal(t, http.StatusBadRequest, h.do("POST", "/app/v1/threads/infra/messages", map[string]string{"text": "  "}, nil))
	assert.Equal(t, http.StatusBadRequest, h.do("POST", "/app/v1/threads/infra/messages", map[string]any{"text": "x", "file_ids": []string{"nope"}}, nil))
	assert.Equal(t, http.StatusBadRequest, h.do("POST", "/app/v1/threads/infra/messages", map[string]any{"text": "x", "bogus": 1}, nil))
}

func TestUploadAttachAndDownload(t *testing.T) {
	h := newHarness(t, func(req provider.Request) string { return "got it" })
	h.pair()

	req, _ := http.NewRequest("POST", h.srv.URL+"/app/v1/threads/infra/files?name=worker.log", strings.NewReader("ERR consumer timeout\n"))
	req.Header.Set("Authorization", "Bearer "+h.token)
	req.Header.Set("Content-Type", "text/plain")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	var up struct {
		File store.AppFile `json:"file"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&up))
	resp.Body.Close()
	assert.Equal(t, "worker.log", up.File.Name)
	assert.EqualValues(t, 21, up.File.Size)

	// A file from one thread can't be sent in another.
	assert.Equal(t, http.StatusBadRequest, h.do("POST", "/app/v1/threads/research/messages", map[string]any{"file_ids": []string{up.File.ID}}, nil))

	require.Equal(t, http.StatusOK, h.do("POST", "/app/v1/threads/infra/messages", map[string]any{"text": "look", "file_ids": []string{up.File.ID}}, nil))
	msgs := h.waitDone("infra", 2)
	require.Len(t, msgs[0].Files, 1)
	reqs := h.run.requests()
	require.Len(t, reqs, 1)
	require.Len(t, reqs[0].Attachments, 1)
	assert.Equal(t, provider.KindFile, reqs[0].Attachments[0].Kind)
	data, err := os.ReadFile(reqs[0].Attachments[0].Path)
	require.NoError(t, err)
	assert.Equal(t, "ERR consumer timeout\n", string(data))

	req, _ = http.NewRequest("GET", h.srv.URL+"/app/v1/files/"+up.File.ID, nil)
	req.Header.Set("Authorization", "Bearer "+h.token)
	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Equal(t, "ERR consumer timeout\n", string(body))
	assert.Equal(t, "text/plain", resp.Header.Get("Content-Type"))
}

func TestAgentSendsFile(t *testing.T) {
	var path string
	h := newHarness(t, func(req provider.Request) string { return "Here it is.\nMEDIA: " + path })
	path = h.ws + "/fix.diff"
	require.NoError(t, os.WriteFile(path, []byte("diff --git"), 0o600))
	h.pair()

	require.Equal(t, http.StatusOK, h.do("POST", "/app/v1/threads/infra/messages", map[string]string{"text": "diff?"}, nil))
	var msgs []store.AppMessage
	require.Eventually(t, func() bool {
		msgs = h.messages("infra")
		return len(msgs) == 2 && len(msgs[1].Files) == 1
	}, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, "Here it is.", msgs[1].Text)
	assert.Equal(t, "fix.diff", msgs[1].Files[0].Name)
}

func TestEventsStream(t *testing.T) {
	release := make(chan struct{})
	h := newHarness(t, func(req provider.Request) string {
		<-release
		return "The queue consumer times out."
	})
	h.pair()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, strings.Replace(h.srv.URL, "http", "ws", 1)+"/app/v1/events", &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + h.token}},
	})
	require.NoError(t, err)
	defer conn.CloseNow()

	next := func() Event {
		t.Helper()
		_, data, err := conn.Read(ctx)
		require.NoError(t, err)
		var e Event
		require.NoError(t, json.Unmarshal(data, &e))
		return e
	}
	assert.Equal(t, "hello", next().Type)

	require.Equal(t, http.StatusOK, h.do("POST", "/app/v1/threads/infra/messages", map[string]string{"text": "what broke?"}, nil))
	close(release)

	var sawUser, sawWorking, sawDelta, sawDone, sawIdle bool
	for !(sawDone && sawIdle) {
		e := next()
		switch {
		case e.Type == "message" && e.Message.Role == store.RoleUser:
			sawUser = true
		case e.Type == "agent" && e.State.Working:
			sawWorking = true
		case e.Type == "agent" && !e.State.Working:
			sawIdle = true
		case e.Type == "delta":
			sawDelta = true
			assert.Equal(t, "infra", e.Thread)
		case e.Type == "message" && e.Message.Status == store.StatusDone:
			sawDone = true
			assert.Equal(t, "The queue consumer times out.", e.Message.Text)
		}
	}
	assert.True(t, sawUser && sawWorking && sawDelta)

	assert.Equal(t, http.StatusUnauthorized, func() int {
		_, resp, _ := websocket.Dial(ctx, strings.Replace(h.srv.URL, "http", "ws", 1)+"/app/v1/events", nil)
		return resp.StatusCode
	}(), "events need a token too")
}

func TestJobsAndPost(t *testing.T) {
	h := newHarness(t, func(req provider.Request) string { return "2 consumer timeouts overnight." })
	h.pair()
	ctx := context.Background()

	// A scheduled run delivered to the app, as `ganclaw run --to app:infra
	// --session nightly` does through the gateway.
	done := make(chan struct{})
	m := h.ch.Message("infra", "check the logs", "nightly")
	m.OnDone = func(router.TurnResult) { close(done) }
	require.True(t, h.ch.Submit(m))
	<-done

	_, err := h.ch.Post(ctx, "infra", store.RoleAgent, "Plain notice", "")
	require.NoError(t, err)
	_, err = h.ch.Post(ctx, "nobody", store.RoleAgent, "x", "")
	assert.Error(t, err)

	var jobs struct {
		Jobs []JobSummary `json:"jobs"`
	}
	require.Equal(t, http.StatusOK, h.do("GET", "/app/v1/jobs", nil, &jobs))
	require.Len(t, jobs.Jobs, 1)
	assert.Equal(t, "nightly", jobs.Jobs[0].Name)
	assert.Equal(t, "infra", jobs.Jobs[0].Agent)
	assert.Equal(t, 1, jobs.Jobs[0].Runs)
	assert.Equal(t, "2 consumer timeouts overnight.", jobs.Jobs[0].LastRun.Text)

	var runs struct {
		Runs []store.AppMessage `json:"runs"`
	}
	require.Equal(t, http.StatusOK, h.do("GET", "/app/v1/jobs/nightly/runs", nil, &runs))
	assert.Len(t, runs.Runs, 1)

	msgs := h.messages("infra")
	require.Len(t, msgs, 2)
	assert.Equal(t, "Plain notice", msgs[1].Text)
}

func TestStopWhenIdle(t *testing.T) {
	h := newHarness(t, func(req provider.Request) string { return "hi" })
	h.pair()
	require.Equal(t, http.StatusOK, h.do("POST", "/app/v1/threads/infra/stop", nil, nil))
	require.Eventually(t, func() bool {
		msgs := h.messages("infra")
		return len(msgs) == 1 && msgs[0].Text == "Nothing is running." && msgs[0].Role == store.RoleSystem
	}, 5*time.Second, 10*time.Millisecond)
}

func TestInterruptedRepliesAreFailedOnStart(t *testing.T) {
	st := store.NewTestStore(t)
	ctx := context.Background()
	m, err := st.AddAppMessage(ctx, store.AppMessage{Thread: "infra", Role: store.RoleAgent, Status: store.StatusStreaming, Text: "half"})
	require.NoError(t, err)
	ch := &Channel{Store: st, FilesDir: t.TempDir()}
	require.NoError(t, ch.Start(ctx))
	got, err := st.AppMessage(ctx, m.ID)
	require.NoError(t, err)
	assert.Equal(t, store.StatusError, got.Status)
	assert.Contains(t, got.Text, "half")
	assert.Contains(t, got.Text, "Interrupted")
}

func TestExtractButtons(t *testing.T) {
	for _, tc := range []struct {
		in, text string
		labels   []string
	}{
		{"plain", "plain", []string{}},
		{"Ship it?\nBUTTONS: Yes | No", "Ship it?", []string{"Yes", "No"}},
		{"Pick\nBUTTONS: `A` | **B** |  | C | D | E", "Pick", []string{"A", "B", "C", "D"}},
		{"```\nBUTTONS: in code\n```", "```\nBUTTONS: in code\n```", []string{}},
		{"BUTTONS: old\ntext\nBUTTONS: new", "BUTTONS: old\ntext", []string{"new"}},
	} {
		text, labels := ExtractButtons(tc.in)
		assert.Equal(t, tc.text, text, tc.in)
		assert.Equal(t, tc.labels, labels, tc.in)
	}
}

func TestDriveDelegationPolicy(t *testing.T) {
	subject := newHarness
	t.Run("Full answers still receive the drive work policy", func(t *testing.T) {
		h := subject(t, func(provider.Request) string { return "Queued." })
		h.pair()
		require.Equal(t, http.StatusOK, h.do("POST", "/app/v1/threads/infra/messages", map[string]any{"text": "Investigate the alert", "drive_mode": true}, nil))
		msgs := h.waitDone("infra", 2)
		assert.Equal(t, "Investigate the alert", msgs[0].Text)
		reqs := h.run.requests()
		require.Len(t, reqs, 1)
		assert.Contains(t, reqs[0].Prompt, "delegate it to a background agent")
		assert.Contains(t, reqs[0].Prompt, `to "app:infra"`)
		assert.NotContains(t, reqs[0].Prompt, VoicePrefix)
	})
	t.Run("Ordinary typed messages do not receive the drive policy", func(t *testing.T) {
		h := subject(t, func(provider.Request) string { return "Done." })
		h.pair()
		require.Equal(t, http.StatusOK, h.do("POST", "/app/v1/threads/infra/messages", map[string]any{"text": "Investigate the alert"}, nil))
		h.waitDone("infra", 2)
		reqs := h.run.requests()
		require.Len(t, reqs, 1)
		assert.NotContains(t, reqs[0].Prompt, "Drive mode:")
	})
}
