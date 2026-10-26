package appchannel

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/kyleparisi/ganclaw/internal/store"
)

// maxJSON caps JSON request bodies.
const maxJSON = 1 << 20

// Handler serves the app API under /app/v1/. Every route except /pair and
// /health needs "Authorization: Bearer <device token>".
func (c *Channel) Handler(version string) http.Handler {
	c.setup()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /app/v1/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": version})
	})
	mux.HandleFunc("POST /app/v1/pair", c.handlePair)

	auth := http.NewServeMux()
	auth.HandleFunc("GET /app/v1/me", c.handleMe)
	auth.HandleFunc("GET /app/v1/agents", c.handleAgents)
	auth.HandleFunc("GET /app/v1/threads/{agent}/messages", c.handleMessages)
	auth.HandleFunc("POST /app/v1/threads/{agent}/messages", c.handleSend)
	auth.HandleFunc("POST /app/v1/threads/{agent}/stop", c.handleStop)
	auth.HandleFunc("POST /app/v1/threads/{agent}/reset", c.handleReset)
	auth.HandleFunc("POST /app/v1/threads/{agent}/files", c.handleUpload)
	auth.HandleFunc("GET /app/v1/files/{id}", c.handleFile)
	auth.HandleFunc("GET /app/v1/jobs", c.handleJobs)
	auth.HandleFunc("GET /app/v1/jobs/{job}/runs", c.handleJobRuns)
	auth.HandleFunc("GET /app/v1/events", c.handleEvents)
	mux.Handle("/app/v1/", c.requireDevice(auth))
	return mux
}

type ctxKey struct{}

func (c *Channel) requireDevice(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || token == "" {
			writeErr(w, http.StatusUnauthorized, "unauthorized", "missing device token")
			return
		}
		d, found, err := c.Store.AppDeviceByToken(r.Context(), token)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "failed", err.Error())
			return
		}
		if !found {
			writeErr(w, http.StatusUnauthorized, "unauthorized", "unknown device token; pair this device again")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, d)))
	})
}

// NewPairCode returns a one-time code that pairs a device named name. It is
// called through the local API, i.e. by someone with shell access.
func (c *Channel) NewPairCode(name string) (code string, expires time.Time) {
	c.setup()
	const alphabet = "ABCDEFGHJKMNPQRSTVWXYZ23456789" // no 0/O, 1/I/L, U
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	code = string(b[:4]) + "-" + string(b[4:])
	expires = c.Now().Add(c.PairTTL)
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, v := range c.codes {
		if c.Now().After(v.expires) {
			delete(c.codes, k)
		}
	}
	c.codes[code] = pairCode{name: name, expires: expires}
	return code, expires
}

// takeCode consumes a pairing code, returning the device name it was made
// for. Codes are compared in constant time and work once.
func (c *Channel) takeCode(code string) (string, bool) {
	code = strings.ToUpper(strings.TrimSpace(code))
	if len(code) == 8 {
		code = code[:4] + "-" + code[4:]
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, v := range c.codes {
		if subtle.ConstantTimeCompare([]byte(k), []byte(code)) == 1 {
			delete(c.codes, k)
			if c.Now().After(v.expires) {
				return "", false
			}
			return v.name, true
		}
	}
	return "", false
}

type pairRequest struct {
	Code       string `json:"code"`
	DeviceName string `json:"device_name,omitempty"`
}

type pairResponse struct {
	Token  string          `json:"token"`
	Device store.AppDevice `json:"device"`
}

func (c *Channel) handlePair(w http.ResponseWriter, r *http.Request) {
	var req pairRequest
	if !decode(w, r, &req) {
		return
	}
	name, ok := c.takeCode(req.Code)
	if !ok {
		// Slow down guessing.
		time.Sleep(300 * time.Millisecond)
		writeErr(w, http.StatusUnauthorized, "bad_code", "that pairing code is wrong or expired; run `ganclaw app pair` for a new one")
		return
	}
	if name == "" {
		name = strings.TrimSpace(req.DeviceName)
	}
	if name == "" {
		name = "phone"
	}
	token := "gca_" + newID(32)
	d := store.AppDevice{ID: newID(8), Name: name, Created: c.Now().UTC()}
	if err := c.Store.AddAppDevice(r.Context(), d.ID, d.Name, token); err != nil {
		writeErr(w, http.StatusInternalServerError, "failed", err.Error())
		return
	}
	c.log.Info("device paired", "device", d.Name, "id", d.ID)
	writeJSON(w, http.StatusOK, pairResponse{Token: token, Device: d})
}

func (c *Channel) handleMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"device": r.Context().Value(ctxKey{})})
}

// AgentSummary is one inbox row.
type AgentSummary struct {
	Name  string            `json:"name"`
	State AgentState        `json:"state"`
	Last  *store.AppMessage `json:"last_message,omitempty"`
	// NeedsYou is true when the newest message is asking the user to pick
	// a button.
	NeedsYou bool `json:"needs_you"`
}

func (c *Channel) handleAgents(w http.ResponseWriter, r *http.Request) {
	last, err := c.Store.LastAppMessages(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed", err.Error())
		return
	}
	out := []AgentSummary{}
	for _, n := range c.agentNames() {
		s := AgentSummary{Name: n, State: c.agentState(n)}
		if m, ok := last[n]; ok {
			s.Last = &m
			s.NeedsYou = len(m.Actions) > 0 && m.Answer == ""
		}
		out = append(out, s)
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": out})
}

func (c *Channel) agentParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	n := r.PathValue("agent")
	if _, ok := c.Agents[n]; !ok {
		writeErr(w, http.StatusNotFound, "unknown_agent", fmt.Sprintf("no agent named %q", n))
		return "", false
	}
	return n, true
}

func (c *Channel) handleMessages(w http.ResponseWriter, r *http.Request) {
	n, ok := c.agentParam(w, r)
	if !ok {
		return
	}
	before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	msgs, err := c.Store.AppMessages(r.Context(), n, before, limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed", err.Error())
		return
	}
	if msgs == nil {
		msgs = []store.AppMessage{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": msgs, "state": c.agentState(n)})
}

type sendRequest struct {
	Text    string   `json:"text"`
	FileIDs []string `json:"file_ids,omitempty"`
	// ReplyTo is the message this answers, e.g. the one whose button was
	// tapped.
	ReplyTo int64 `json:"reply_to,omitempty"`
}

func (c *Channel) handleSend(w http.ResponseWriter, r *http.Request) {
	n, ok := c.agentParam(w, r)
	if !ok {
		return
	}
	var req sendRequest
	if !decode(w, r, &req) {
		return
	}
	req.Text = strings.TrimSpace(req.Text)
	if req.Text == "" && len(req.FileIDs) == 0 {
		writeErr(w, http.StatusBadRequest, "bad_request", "text or file_ids is required")
		return
	}
	ctx := r.Context()
	var files []store.AppFile
	for _, id := range req.FileIDs {
		f, found, err := c.Store.AppFile(ctx, id)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "failed", err.Error())
			return
		}
		if !found || f.Thread != n {
			writeErr(w, http.StatusBadRequest, "bad_request", fmt.Sprintf("no file %q in this thread", id))
			return
		}
		files = append(files, f)
	}

	prompt := req.Text
	if req.ReplyTo != 0 {
		orig, err := c.Store.AppMessage(ctx, req.ReplyTo)
		if err != nil || orig.Thread != n {
			writeErr(w, http.StatusBadRequest, "bad_request", "reply_to is not a message in this thread")
			return
		}
		prompt = withReply(orig, req.Text)
		if orig.Answer == "" && containsFold(orig.Actions, req.Text) {
			orig.Answer = req.Text
			if updated, err := c.Store.UpdateAppMessage(ctx, orig); err == nil {
				c.publishMessage(updated)
			}
		}
	}

	m, err := c.Store.AddAppMessage(ctx, store.AppMessage{Thread: n, Role: store.RoleUser, Text: req.Text, Files: files, ReplyTo: req.ReplyTo})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed", err.Error())
		return
	}
	c.publishMessage(m)

	msg := c.Message(n, prompt, "")
	if len(files) > 0 {
		msg.Fetch = c.fetcher(files)
	}
	// Set before submitting: a fast turn's OnDone must win over this.
	prev := c.agentState(n)
	if !prev.Working {
		c.setState(n, AgentState{Working: true, Detail: "Queued", Since: c.Now()})
	}
	if !c.Submit(msg) {
		c.setState(n, prev)
		_, _ = c.Post(context.WithoutCancel(ctx), n, store.RoleSystem, "I'm busy with earlier messages — please try again in a bit.", "")
		writeErr(w, http.StatusTooManyRequests, "busy", "that thread has too many queued messages")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"message": m})
}

// maxReplyContext caps how much of a replied-to message is quoted.
const maxReplyContext = 2000

// withReply tells the agent which message the user is answering, the same
// way the Telegram channel does.
func withReply(orig store.AppMessage, text string) string {
	who := "the user's earlier message"
	if orig.Role != store.RoleUser {
		who = "your earlier message"
	}
	quoted := orig.Text
	if quoted == "" {
		quoted = "(a message without text)"
	}
	if runes := []rune(quoted); len(runes) > maxReplyContext {
		quoted = string(runes[:maxReplyContext]) + " …"
	}
	return "[Replying to " + who + ":]\n> " + strings.ReplaceAll(quoted, "\n", "\n> ") + "\n\n" + text
}

func containsFold(list []string, s string) bool {
	for _, v := range list {
		if strings.EqualFold(v, s) {
			return true
		}
	}
	return false
}

// handleStop and handleReset reuse the router's /stop and /new commands.
func (c *Channel) handleStop(w http.ResponseWriter, r *http.Request) {
	c.command(w, r, "/stop")
}

func (c *Channel) handleReset(w http.ResponseWriter, r *http.Request) {
	c.command(w, r, "/new")
}

func (c *Channel) command(w http.ResponseWriter, r *http.Request, cmd string) {
	n, ok := c.agentParam(w, r)
	if !ok {
		return
	}
	m := c.Message(n, cmd, "")
	t := &turn{c: c, agent: n}
	m.Reply = func(ctx context.Context, text string) error {
		_, err := t.c.Post(ctx, n, store.RoleSystem, text, "")
		return err
	}
	m.Stream, m.Typing, m.OnDone = nil, nil, nil
	if !c.Submit(m) {
		writeErr(w, http.StatusTooManyRequests, "busy", "that thread has too many queued messages")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleUpload stores the raw request body as a file in the thread. The
// name comes from ?name= (or X-Filename) and the type from Content-Type.
func (c *Channel) handleUpload(w http.ResponseWriter, r *http.Request) {
	n, ok := c.agentParam(w, r)
	if !ok {
		return
	}
	name := r.URL.Query().Get("name")
	if name == "" {
		name = r.Header.Get("X-Filename")
	}
	if name == "" {
		name = "file"
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxFileSize+1)
	f, err := c.saveFile(r.Context(), n, name, r.Header.Get("Content-Type"), r.Body)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) || strings.Contains(err.Error(), "too large") {
			writeErr(w, http.StatusRequestEntityTooLarge, "too_large", fmt.Sprintf("files are limited to %d MB", MaxFileSize>>20))
			return
		}
		writeErr(w, http.StatusInternalServerError, "failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"file": f})
}

func (c *Channel) handleFile(w http.ResponseWriter, r *http.Request) {
	f, found, err := c.Store.AppFile(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed", err.Error())
		return
	}
	if !found {
		writeErr(w, http.StatusNotFound, "not_found", "no such file")
		return
	}
	fh, err := os.Open(f.Path)
	if err != nil {
		writeErr(w, http.StatusGone, "gone", "the file is no longer stored")
		return
	}
	defer fh.Close()
	st, err := fh.Stat()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed", err.Error())
		return
	}
	w.Header().Set("Content-Type", f.Mime)
	w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%q", f.Name))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, "", st.ModTime(), fh)
}

// JobSummary is a scheduled job as the app sees it: the runs it posted.
type JobSummary struct {
	Name    string           `json:"name"`
	Agent   string           `json:"agent"`
	LastRun store.AppMessage `json:"last_run"`
	Runs    int              `json:"runs"`
	// Problems counts recent runs that failed or asked for attention.
	Problems int `json:"problems"`
}

func (c *Channel) handleJobs(w http.ResponseWriter, r *http.Request) {
	runs, err := c.Store.AppJobRuns(r.Context(), "", 500)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed", err.Error())
		return
	}
	byName := map[string]*JobSummary{}
	out := []*JobSummary{}
	for _, m := range runs { // newest first
		j, ok := byName[m.Job]
		if !ok {
			j = &JobSummary{Name: m.Job, Agent: m.Thread, LastRun: m}
			byName[m.Job] = j
			out = append(out, j)
		}
		j.Runs++
		if m.Status == store.StatusError || (len(m.Actions) > 0 && m.Answer == "") {
			j.Problems++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": out})
}

func (c *Channel) handleJobRuns(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	runs, err := c.Store.AppJobRuns(r.Context(), r.PathValue("job"), limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed", err.Error())
		return
	}
	if runs == nil {
		runs = []store.AppMessage{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
}

// handleEvents streams Events over a WebSocket. It starts with one "agent"
// event per working agent so the client's state is complete.
func (c *Channel) handleEvents(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return // Accept has written the error
	}
	defer conn.CloseNow()
	sub := c.hub.subscribe()
	defer c.hub.unsubscribe(sub)
	ctx := conn.CloseRead(r.Context())

	send := func(e Event) error {
		data, err := json.Marshal(e)
		if err != nil {
			return err
		}
		wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		return conn.Write(wctx, websocket.MessageText, data)
	}
	if err := send(Event{Type: "hello"}); err != nil {
		return
	}
	for _, n := range c.agentNames() {
		if s := c.agentState(n); s.Working {
			if err := send(Event{Type: "agent", Agent: n, State: &s}); err != nil {
				return
			}
		}
	}
	ping := time.NewTicker(30 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-sub.closed:
			conn.Close(websocket.StatusTryAgainLater, "fell behind; reconnect")
			return
		case e := <-sub.ch:
			if err := send(e); err != nil {
				return
			}
		case <-ping.C:
			pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := conn.Ping(pctx)
			cancel()
			if err != nil {
				return
			}
		}
	}
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(io.LimitReader(r.Body, maxJSON))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return false
	}
	return true
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{"ok": false, "error": map[string]string{"code": code, "message": msg}})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
