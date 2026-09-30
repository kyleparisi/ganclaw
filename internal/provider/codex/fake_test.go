package codex

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/kyleparisi/ganclaw/internal/procexec"
)

// fakeAppServer is an in-memory codex app-server. Each test sets Handle
// inline to script responses; Handle may push notifications with Notify
// (sent immediately, i.e. before the reply) or NotifyAfterReply.
type fakeAppServer struct {
	// OnInitialize observes the handshake; InitError fails it. Otherwise
	// initialize is answered automatically and never reaches Handle.
	OnInitialize func(p initializeParams)
	InitError    *rpcError

	Handle  func(s *fakeAppServer, method string, params json.RawMessage) (any, *rpcError)
	OnReply func(s *fakeAppServer, m message) // replies to server-initiated requests
	ExitErr error

	Cmd procexec.Cmd // what the provider asked to start

	mu            sync.Mutex
	out           *io.PipeWriter
	in            *io.PipeReader
	calls         []string
	notifications []string
	queued        []message
	done          chan struct{}
}

func (s *fakeAppServer) Exec() procexec.Exec {
	return procexec.Exec{Start: func(ctx context.Context, cmd procexec.Cmd) (*procexec.Proc, error) {
		s.Cmd = cmd
		inR, inW := io.Pipe()
		outR, outW := io.Pipe()
		s.in, s.out = inR, outW
		s.done = make(chan struct{})
		go s.serve(inR)
		return &procexec.Proc{
			Stdin:  inW,
			Stdout: outR,
			Wait: func() error {
				<-s.done
				s.mu.Lock()
				defer s.mu.Unlock()
				return s.ExitErr
			},
			Kill: func() error { s.Crash(errors.New("killed")); return nil },
		}, nil
	}}
}

func (s *fakeAppServer) serve(r io.Reader) {
	defer close(s.done)
	defer s.out.Close()
	dec := json.NewDecoder(r)
	for {
		var m message
		if dec.Decode(&m) != nil {
			return
		}
		switch {
		case m.Method == "":
			if s.OnReply != nil {
				s.OnReply(s, m)
			}
		case len(m.ID) == 0:
			s.mu.Lock()
			s.notifications = append(s.notifications, m.Method)
			s.mu.Unlock()
		default:
			s.mu.Lock()
			s.calls = append(s.calls, m.Method)
			s.mu.Unlock()
			reply := message{ID: m.ID}
			if res, rerr := s.handle(m.Method, m.Params); rerr != nil {
				reply.Error = rerr
			} else {
				reply.Result, _ = json.Marshal(res)
			}
			s.send(reply)
			s.mu.Lock()
			queued := s.queued
			s.queued = nil
			s.mu.Unlock()
			for _, q := range queued {
				s.send(q)
			}
		}
	}
}

func (s *fakeAppServer) handle(method string, params json.RawMessage) (any, *rpcError) {
	if method == "initialize" {
		if s.OnInitialize != nil {
			var p initializeParams
			_ = json.Unmarshal(params, &p)
			s.OnInitialize(p)
		}
		if s.InitError != nil {
			return nil, s.InitError
		}
		return initializeResponse{UserAgent: "fake"}, nil
	}
	if s.Handle == nil {
		return nil, &rpcError{Code: -32601, Message: "unhandled " + method}
	}
	return s.Handle(s, method, params)
}

func (s *fakeAppServer) send(m message) {
	b, _ := json.Marshal(m)
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _ = s.out.Write(append(b, '\n'))
}

func (s *fakeAppServer) Notify(method string, params any) {
	p, _ := json.Marshal(params)
	s.send(message{Method: method, Params: p})
}

func (s *fakeAppServer) NotifyAfterReply(method string, params any) {
	p, _ := json.Marshal(params)
	s.mu.Lock()
	s.queued = append(s.queued, message{Method: method, Params: p})
	s.mu.Unlock()
}

// Request sends a server-initiated request; the reply goes to OnReply.
func (s *fakeAppServer) Request(id int, method string, params any) {
	p, _ := json.Marshal(params)
	idb, _ := json.Marshal(id)
	s.send(message{ID: idb, Method: method, Params: p})
}

// Crash simulates the process dying: stdout closes, Wait returns ExitErr.
func (s *fakeAppServer) Crash(err error) {
	s.mu.Lock()
	s.ExitErr = err
	s.mu.Unlock()
	s.out.CloseWithError(io.EOF)
	s.in.CloseWithError(io.EOF)
}

func (s *fakeAppServer) Calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

func (s *fakeAppServer) Notifications() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.notifications...)
}

const (
	eventually = 2 * time.Second
	tick       = 5 * time.Millisecond
)

// Helpers that build notification payloads.

func agentItem(id, phase, text string) map[string]any {
	return map[string]any{"id": id, "type": "agentMessage", "phase": phase, "text": text}
}

func itemEvent(thread, turnID string, item map[string]any) map[string]any {
	return map[string]any{"threadId": thread, "turnId": turnID, "item": item, "completedAtMs": 0}
}

func delta(thread, turnID, itemID, text string) agentMessageDelta {
	return agentMessageDelta{ThreadID: thread, TurnID: turnID, ItemID: itemID, Delta: text}
}

func completed(thread, turnID, status string, errInfo any) map[string]any {
	t := map[string]any{"id": turnID, "status": status, "items": []any{}}
	if errInfo != nil {
		t["error"] = map[string]any{"message": "boom", "codexErrorInfo": errInfo}
	}
	return map[string]any{"threadId": thread, "turn": t}
}

func threadResult(id string) threadResponse {
	var r threadResponse
	r.Thread.ID = id
	r.Model = "gpt-test"
	return r
}

func turnResult(id string) turnStartResponse {
	return turnStartResponse{Turn: turn{ID: id, Status: "inProgress"}}
}
