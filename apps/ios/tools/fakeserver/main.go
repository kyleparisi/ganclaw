// Command fakeserver runs ganclaw's real app channel with scripted agents,
// for the iOS app's control-server tests. Nothing calls a model: replies
// come from a few canned rules, streamed in chunks like a real provider.
//
// It prints two lines on startup:
//
//	Fake server: http://127.0.0.1:<port>   (what the app connects to)
//	Admin: http://127.0.0.1:<port>         (test-only controls)
//
// Admin routes (POST, JSON): /admin/pair → {"code"}; /admin/seed adds the
// design's sample history; /admin/run {"agent","prompt","job"} starts a
// turn as a scheduler would; /admin/release lets "hold" turns finish;
// /admin/post {"agent","text","job","role"} adds a message; GET
// /admin/requests lists the prompts agents received.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/kyleparisi/ganclaw/internal/agent"
	"github.com/kyleparisi/ganclaw/internal/appchannel"
	"github.com/kyleparisi/ganclaw/internal/provider"
	"github.com/kyleparisi/ganclaw/internal/router"
	"github.com/kyleparisi/ganclaw/internal/store"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:0", "app channel address")
	adminAddr := flag.String("admin", "127.0.0.1:0", "admin address")
	dir := flag.String("dir", "", "state directory (default: a temp dir)")
	chunkDelay := flag.Duration("chunk-delay", 40*time.Millisecond, "delay between streamed chunks")
	flag.Parse()

	if err := run(*addr, *adminAddr, *dir, *chunkDelay); err != nil {
		fmt.Fprintln(os.Stderr, "fakeserver:", err)
		os.Exit(1)
	}
}

var agentNames = []string{"builder", "infra", "mailbox", "research", "trader"}

type fake struct {
	mu      sync.Mutex
	prompts []map[string]string
	hold    chan struct{}
	delay   time.Duration
}

func run(addr, adminAddr, dir string, delay time.Duration) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if dir == "" {
		d, err := os.MkdirTemp("", "ganclaw-fake")
		if err != nil {
			return err
		}
		defer os.RemoveAll(d)
		dir = d
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	st, err := store.Open(filepath.Join(dir, "ganclaw.db"))
	if err != nil {
		return err
	}
	defer st.Close()

	agents := map[string]agent.Agent{}
	for _, n := range agentNames {
		ws := filepath.Join(dir, "agents", n)
		if err := os.MkdirAll(ws, 0o700); err != nil {
			return err
		}
		agents[n] = agent.Agent{Name: n, Workspace: ws}
	}
	f := &fake{hold: make(chan struct{}), delay: delay}
	r := router.New(ctx, router.Config{Agents: agents, Store: st, Run: f.run, Logger: log, TypingInterval: 500 * time.Millisecond})
	ch := &appchannel.Channel{Agents: agents, Store: st, Submit: r.Submit, FilesDir: filepath.Join(dir, "app-files"), Logger: log}
	if err := ch.Start(ctx); err != nil {
		return err
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	aln, err := net.Listen("tcp", adminAddr)
	if err != nil {
		return err
	}
	fmt.Printf("Fake server: http://%s\n", ln.Addr())
	fmt.Printf("Admin: http://%s\n", aln.Addr())

	app := &http.Server{Handler: ch.Handler("fake"), ReadHeaderTimeout: 10 * time.Second}
	admin := &http.Server{Handler: f.admin(ch, st, dir), ReadHeaderTimeout: 10 * time.Second}
	go app.Serve(ln)
	go admin.Serve(aln)
	<-ctx.Done()
	sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	app.Shutdown(sctx)
	admin.Shutdown(sctx)
	r.Wait()
	return nil
}

// run is the scripted provider.
func (f *fake) run(ctx context.Context, sessions provider.Sessions, req provider.Request) (string, provider.Result, error) {
	f.mu.Lock()
	f.prompts = append(f.prompts, map[string]string{"cwd": filepath.Base(req.Cwd), "prompt": req.Prompt})
	hold := f.hold
	f.mu.Unlock()

	prompt := req.Prompt
	if i := strings.LastIndex(prompt, "\n\n"); strings.HasPrefix(prompt, "[") && i >= 0 {
		prompt = prompt[i+2:] // drop voice/reply context
	}
	lower := strings.ToLower(prompt)
	var text string
	switch {
	case strings.HasPrefix(req.Prompt, appchannel.VoicePrefix):
		text = "Yes, it's live. The alarm is set. Nothing needs you right now.\n---\nDeployed at 7:58 AM. Alarm: queue message size > 200 KB."
	case strings.Contains(lower, "hold"):
		select {
		case <-hold:
		case <-ctx.Done():
			return "", provider.Result{}, ctx.Err()
		}
		text = "Done holding."
	case strings.Contains(lower, "broke") || strings.Contains(lower, "fix"):
		text = "The queue consumer times out on oversized messages. I drafted a fix that stores the payload in S3 and passes a pointer.\n\nOpen a pull request with this fix?\nBUTTONS: Approve | Not now"
	case strings.Contains(lower, "draft") || strings.Contains(lower, "copy"):
		text = "Here's a draft. Subject and body are separate so you can paste each into its field.\n\n```subject\nMoving our call to Thursday\n```\n\n```body\nHi [NAME],\n\nSomething came up on my end. Could we move Tuesday's call to Thursday, same time?\n\nThanks,\nKyle\n```\n\n```shell\naws sqs get-queue-attributes --queue-url $QUEUE_URL --attribute-names All\n```"
	case lower == "approve" || strings.HasSuffix(lower, "approve"):
		text = "Opened PR #482: Store large payloads in S3."
	case len(req.Attachments) > 0:
		names := make([]string, len(req.Attachments))
		for i, a := range req.Attachments {
			names[i] = a.Name
		}
		text = "Got " + strings.Join(names, ", ") + "."
	default:
		text = "Got it: " + strings.TrimSpace(prompt)
	}

	if req.OnDelta != nil {
		for _, chunk := range chunks(text, 12) {
			select {
			case <-ctx.Done():
				return "", provider.Result{}, ctx.Err()
			case <-time.After(f.delay):
			}
			req.OnDelta(chunk)
		}
	}
	sessions["fake"] = "fake-session"
	return "fake", provider.Result{Session: "fake-session", Text: text}, nil
}

func chunks(s string, n int) []string {
	var out []string
	r := []rune(s)
	for len(r) > 0 {
		k := min(n, len(r))
		out = append(out, string(r[:k]))
		r = r[k:]
	}
	return out
}

func (f *fake) admin(ch *appchannel.Channel, st *store.Store, dir string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /admin/pair", func(w http.ResponseWriter, r *http.Request) {
		code, _ := ch.NewPairCode("")
		writeJSON(w, map[string]string{"code": code})
	})
	mux.HandleFunc("POST /admin/run", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Agent, Prompt, Job string }
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			http.Error(w, "bad json", 400)
			return
		}
		m := ch.Message(req.Agent, req.Prompt, req.Job)
		if req.Job != "" {
			m.ChatKey = "job:" + req.Agent + ":" + req.Job
		}
		writeJSON(w, map[string]bool{"ok": ch.Submit(m)})
	})
	mux.HandleFunc("POST /admin/release", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		close(f.hold)
		f.hold = make(chan struct{})
		f.mu.Unlock()
		writeJSON(w, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /admin/post", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Agent, Text, Job, Role string }
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			http.Error(w, "bad json", 400)
			return
		}
		if req.Role == "" {
			req.Role = store.RoleAgent
		}
		m, err := ch.Post(r.Context(), req.Agent, req.Role, req.Text, req.Job)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		writeJSON(w, m)
	})
	mux.HandleFunc("GET /admin/requests", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		writeJSON(w, map[string]any{"requests": f.prompts})
	})
	mux.HandleFunc("POST /admin/seed", func(w http.ResponseWriter, r *http.Request) {
		if err := seed(r.Context(), st, dir); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	})
	return mux
}

// seed adds history resembling the design: an approval waiting in Infra,
// files from Mailbox, and a scheduled job's runs.
func seed(ctx context.Context, st *store.Store, dir string) error {
	file := func(thread, name, mime, body string) (store.AppFile, error) {
		p := filepath.Join(dir, "seed", thread, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return store.AppFile{}, err
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			return store.AppFile{}, err
		}
		f := store.AppFile{ID: "seed-" + thread + "-" + strings.ReplaceAll(name, ".", "-"), Thread: thread, Name: name, Mime: mime, Size: int64(len(body)), Path: p}
		return f, st.AddAppFile(ctx, f)
	}
	add := func(m store.AppMessage) error {
		_, err := st.AddAppMessage(ctx, m)
		return err
	}
	for _, j := range []struct {
		text, status string
		actions      []string
	}{
		{"Couldn't read the log bucket: access denied.", store.StatusError, nil},
		{"No issues.", store.StatusDone, nil},
		{"No issues.", store.StatusDone, nil},
		{"2 consumer timeouts overnight. Both retried and went through.\n\nSame large-message pattern as last week. Worth shipping the payload fix.", store.StatusDone, []string{"Mute similar"}},
	} {
		if err := add(store.AppMessage{Thread: "infra", Role: store.RoleAgent, Text: j.text, Status: j.status, Job: "nightly-log-check", Actions: j.actions}); err != nil {
			return err
		}
	}
	log, err := file("infra", "worker.log", "text/plain", "ERR consumer timeout\nmsg size exceeded limit\nretry 3/3 … dropped\n")
	if err != nil {
		return err
	}
	if err := add(store.AppMessage{Thread: "infra", Role: store.RoleUser, Text: "Here's last night's failure. What broke?", Files: []store.AppFile{log}}); err != nil {
		return err
	}
	diff, err := file("infra", "large-payloads.diff", "text/x-diff", "diff --git a/consumer.go b/consumer.go\n")
	if err != nil {
		return err
	}
	if err := add(store.AppMessage{Thread: "infra", Role: store.RoleAgent, Text: "The queue consumer times out on oversized messages. I drafted a fix that stores the payload in S3 and passes a pointer.\n\nOpen a pull request with this fix?", Files: []store.AppFile{diff}, Actions: []string{"Approve", "Not now"}}); err != nil {
		return err
	}
	inv, err := file("mailbox", "invoice-oct.pdf", "application/pdf", "%PDF-1.4\n")
	if err != nil {
		return err
	}
	if err := add(store.AppMessage{Thread: "mailbox", Role: store.RoleAgent, Text: "Sent you invoice-oct.pdf and 2 images", Files: []store.AppFile{inv}}); err != nil {
		return err
	}
	if err := add(store.AppMessage{Thread: "trader", Role: store.RoleAgent, Text: "Watching the open positions until close."}); err != nil {
		return err
	}
	return add(store.AppMessage{Thread: "research", Role: store.RoleAgent, Text: "Reading sources."})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
