package workers

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestQueue(t *testing.T) {
	subject := OpenQueue
	t.Run("Assignment survives restart and cannot move to another instance", func(t *testing.T) {
		dir := t.TempDir()
		q, err := subject(dir)
		require.NoError(t, err)
		id, owner := ID(), ID()
		first, err := q.Submit(Job{ID: id, Worker: "mac", Profile: "ios-test", Timeout: 60})
		require.NoError(t, err)
		assert.Equal(t, "queued", first.State)
		_, err = q.Submit(Job{ID: ID(), Worker: "mac", Profile: "android-test", Timeout: 60})
		require.NoError(t, err)
		claimed, err := q.Poll("mac", owner)
		require.NoError(t, err)
		assert.Equal(t, id, claimed.ID)
		require.NoError(t, q.Close())
		q, err = subject(dir)
		require.NoError(t, err)
		defer q.Close()
		claimed, err = q.Poll("mac", owner)
		require.NoError(t, err)
		assert.Equal(t, id, claimed.ID)
		_, err = q.Poll("mac", ID())
		require.Error(t, err)
		_, err = subject(dir)
		require.Error(t, err)
		require.NoError(t, q.Complete(id, "mac", owner, Result{State: "uncertain", ExitCode: -1}))
		claimed, err = q.Poll("mac", owner)
		require.NoError(t, err)
		assert.Equal(t, id, claimed.ID)
		assert.Equal(t, "uncertain", claimed.State)
		require.NoError(t, q.Resolve(id))
		claimed, err = q.Poll("mac", owner)
		require.NoError(t, err)
		assert.NotEqual(t, id, claimed.ID)
	})
	t.Run("Submission is idempotent and rejects a changed request", func(t *testing.T) {
		q, err := subject(t.TempDir())
		require.NoError(t, err)
		defer q.Close()
		j := Job{ID: ID(), Worker: "mac", Profile: "ios-test", Timeout: 60}
		first, err := q.Submit(j)
		require.NoError(t, err)
		second, err := q.Submit(j)
		require.NoError(t, err)
		assert.Equal(t, first, second)
		j.Profile = "android-test"
		_, err = q.Submit(j)
		require.Error(t, err)
	})
	t.Run("Queued cancellation never executes", func(t *testing.T) {
		q, err := subject(t.TempDir())
		require.NoError(t, err)
		defer q.Close()
		j, err := q.Submit(Job{ID: ID(), Worker: "mac", Profile: "ios-test", Timeout: 60})
		require.NoError(t, err)
		require.NoError(t, q.CancelJob(j.ID))
		claimed, err := q.Poll("mac", ID())
		require.NoError(t, err)
		assert.Nil(t, claimed)
	})
}

func TestExecute(t *testing.T) {
	subject := Execute
	t.Run("Real process failure returns exit code logs and artifact bytes", func(t *testing.T) {
		cwd, dir := t.TempDir(), t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(cwd, "screen.png"), []byte("artifact-bytes"), 0600))
		p := Profile{Command: "/bin/sh", Args: []string{"-c", "echo assertion-failed; exit 7"}, Cwd: cwd, RequiredArtifacts: []string{"screen.png"}}
		out := subject(context.Background(), Job{ID: ID()}, p, dir)
		assert.Equal(t, "failed", out.State)
		assert.Equal(t, 7, out.ExitCode)
		files := unpack(t, filepath.Join(dir, "bundle.tar.gz"))
		assert.Contains(t, string(files["job.log"]), "assertion-failed")
		assert.Equal(t, "artifact-bytes", string(files["artifacts/screen.png"]))
	})
	t.Run("Deadline kills child processes and returns timeout", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		dir := t.TempDir()
		out := subject(ctx, Job{ID: ID()}, Profile{Command: "/bin/sh", Args: []string{"-c", "sleep 30 & wait"}, Cwd: t.TempDir()}, dir)
		assert.Equal(t, "timed_out", out.State)
		assert.Equal(t, -1, out.ExitCode)
	})
	t.Run("No fixture environment leaks into build", func(t *testing.T) {
		t.Setenv("GANCLAW_SECRET", "must-not-appear")
		dir := t.TempDir()
		out := subject(context.Background(), Job{ID: ID()}, Profile{Command: "/bin/sh", Args: []string{"-c", "test -z \"$GANCLAW_SECRET\"; echo safe"}, Cwd: t.TempDir()}, dir)
		assert.Equal(t, "succeeded", out.State)
		assert.NotContains(t, string(unpack(t, filepath.Join(dir, "bundle.tar.gz"))["job.log"]), "must-not-appear")
	})
	t.Run("Artifact symlink outside checkout is rejected without reading it", func(t *testing.T) {
		cwd, dir := t.TempDir(), t.TempDir()
		secret := filepath.Join(t.TempDir(), "secret")
		require.NoError(t, os.WriteFile(secret, []byte("private"), 0600))
		require.NoError(t, os.Symlink(secret, filepath.Join(cwd, "escape")))
		out := subject(context.Background(), Job{ID: ID()}, Profile{Command: "/bin/echo", Cwd: cwd, Artifacts: []string{"escape"}}, dir)
		assert.Equal(t, "failed", out.State)
		assert.Contains(t, out.Message, "artifact")
		files := unpack(t, filepath.Join(dir, "bundle.tar.gz"))
		assert.NotContains(t, files, "artifacts/escape")
	})
	t.Run("Missing required evidence fails a successful command", func(t *testing.T) {
		dir := t.TempDir()
		out := subject(context.Background(), Job{ID: ID()}, Profile{Command: "/bin/echo", Cwd: t.TempDir(), RequiredArtifacts: []string{"results.xml"}}, dir)
		assert.Equal(t, "failed", out.State)
		assert.Equal(t, 0, out.ExitCode)
	})
}
func unpack(t *testing.T, path string) map[string][]byte {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()
	gz, err := gzip.NewReader(f)
	require.NoError(t, err)
	defer gz.Close()
	tr := tar.NewReader(gz)
	files := map[string][]byte{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		b, err := io.ReadAll(tr)
		require.NoError(t, err)
		files[h.Name] = b
	}
	return files
}

func testBroker(t *testing.T) (*Broker, *Client, *Client) {
	t.Helper()
	q, err := OpenQueue(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { q.Close() })
	b := &Broker{Queue: q, Controller: strings.Repeat("c", 32), Tokens: map[string]string{"mac": strings.Repeat("w", 32)}, Profiles: map[string][]string{"mac": {"ios-test"}}}
	srv := httptest.NewTLSServer(b.Handler())
	t.Cleanup(srv.Close)
	controller, err := NewClient(srv.URL, b.Controller)
	require.NoError(t, err)
	worker, err := NewClient(srv.URL, b.Tokens["mac"])
	require.NoError(t, err)
	controller.HTTP = srv.Client()
	worker.HTTP = srv.Client()
	worker.Instance = ID()
	return b, controller, worker
}

func TestTransport(t *testing.T) {
	subject := NewClient
	t.Run("HTTPS credential scopes do not grant gateway or other worker access", func(t *testing.T) {
		_, controller, worker := testBroker(t)
		ctx := context.Background()
		var out any
		require.Error(t, worker.Call(ctx, "POST", "/v1/jobs", Job{ID: ID(), Worker: "mac", Profile: "ios-test", Timeout: 60}, &out))
		require.Error(t, controller.Call(ctx, "POST", "/v1/workers/mac/poll", nil, &out))
		require.Error(t, worker.Call(ctx, "POST", "/v1/workers/another/poll", nil, &out))
		_, err := subject("http://example.com", worker.Token)
		require.Error(t, err)
		_, err = subject("https://example.com", worker.Token)
		require.NoError(t, err)
	})
	t.Run("Failure and checksum verified artifact round trip can be retried", func(t *testing.T) {
		b, controller, worker := testBroker(t)
		ctx := context.Background()
		j := Job{ID: ID(), Worker: "mac", Profile: "ios-test", Timeout: 60}
		var submitted Job
		require.NoError(t, controller.Call(ctx, "POST", "/v1/jobs", j, &submitted))
		var assigned *Job
		require.NoError(t, worker.Call(ctx, "POST", workerPath("mac")+"/poll", nil, &assigned))
		payload := []byte("fixture-bundle")
		hash := sha256.Sum256(payload)
		path := workerPath("mac") + "/jobs/" + j.ID + "/bundle"
		resp, err := worker.Request(ctx, "PUT", path, bytes.NewReader(payload), hex.EncodeToString(hash[:]))
		require.NoError(t, err)
		resp.Body.Close()
		var out any
		result := Result{State: "failed", ExitCode: 65}
		require.NoError(t, worker.Call(ctx, "POST", workerPath("mac")+"/jobs/"+j.ID+"/result", result, &out))
		require.NoError(t, worker.Call(ctx, "POST", workerPath("mac")+"/jobs/"+j.ID+"/result", result, &out))
		resp, err = worker.Request(ctx, "PUT", path, bytes.NewReader(payload), hex.EncodeToString(hash[:]))
		require.NoError(t, err)
		resp.Body.Close()
		resp, err = controller.Request(ctx, "GET", "/v1/jobs/"+j.ID+"/bundle", nil, "")
		require.NoError(t, err)
		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		require.NoError(t, err)
		assert.Equal(t, payload, data)
		stored, err := b.Queue.Get(j.ID)
		require.NoError(t, err)
		assert.Equal(t, 65, stored.Result.ExitCode)
	})
}

func TestRunner(t *testing.T) {
	subject := (*Runner).Run
	t.Run("Lost completion acknowledgement retries cached result without executing twice", func(t *testing.T) {
		b, _, client := testBroker(t)
		job, err := b.Queue.Submit(Job{ID: ID(), Worker: "mac", Profile: "ios-test", Timeout: 60})
		require.NoError(t, err)
		var executions, results atomic.Int32
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/result") && results.Add(1) == 1 {
				recorder := httptest.NewRecorder()
				b.Handler().ServeHTTP(recorder, r)
				assert.Equal(t, 200, recorder.Code)
				http.Error(w, "simulated lost acknowledgement", 503)
				return
			}
			b.Handler().ServeHTTP(w, r)
		}))
		defer server.Close()
		client.URL = server.URL
		client.HTTP = server.Client()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		r := &Runner{Client: client, Config: WorkerConfig{Name: "mac", StateDir: t.TempDir(), Profiles: map[string]Profile{"ios-test": {Command: "/bin/echo", Cwd: t.TempDir()}}}, Execute: func(ctx context.Context, j Job, p Profile, dir string) Result {
			executions.Add(1)
			return Execute(ctx, j, p, dir)
		}, Report: func(s string) {
			if strings.Contains(s, "acknowledged") {
				cancel()
			}
		}}
		err = subject(r, ctx)
		assert.ErrorIs(t, err, context.Canceled)
		assert.EqualValues(t, 1, executions.Load())
		stored, err := b.Queue.Get(job.ID)
		require.NoError(t, err)
		assert.Equal(t, "succeeded", stored.State)
	})
	t.Run("Restart after acceptance reports uncertain and blocks queued work", func(t *testing.T) {
		b, _, client := testBroker(t)
		dir := t.TempDir()
		owner := ID()
		require.NoError(t, atomicJSON(filepath.Join(dir, "instance.json"), owner))
		job, err := b.Queue.Submit(Job{ID: ID(), Worker: "mac", Profile: "ios-test", Timeout: 60})
		require.NoError(t, err)
		claimed, err := b.Queue.Poll("mac", owner)
		require.NoError(t, err)
		require.NoError(t, atomicJSON(filepath.Join(dir, "journal.json"), Journal{Job: *claimed}))
		r := &Runner{Client: client, Config: WorkerConfig{Name: "mac", StateDir: dir, Profiles: map[string]Profile{"ios-test": {Command: "/bin/echo", Cwd: t.TempDir()}}}, Execute: func(context.Context, Job, Profile, string) Result {
			t.Fatal("must never execute uncertain job")
			return Result{}
		}}
		err = subject(r, context.Background())
		require.ErrorContains(t, err, "uncertain outcome")
		stored, err := b.Queue.Get(job.ID)
		require.NoError(t, err)
		assert.Equal(t, "uncertain", stored.State)
	})
	t.Run("Active cancellation is polled and preserves artifacts", func(t *testing.T) {
		b, _, client := testBroker(t)
		job, err := b.Queue.Submit(Job{ID: ID(), Worker: "mac", Profile: "ios-test", Timeout: 60})
		require.NoError(t, err)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		r := &Runner{Client: client, Config: WorkerConfig{Name: "mac", StateDir: t.TempDir(), Profiles: map[string]Profile{"ios-test": {Command: "/bin/sh", Args: []string{"-c", "echo running; sleep 30"}, Cwd: t.TempDir()}}}, Execute: func(ctx context.Context, j Job, p Profile, dir string) Result {
			require.NoError(t, b.Queue.CancelJob(j.ID))
			return Execute(ctx, j, p, dir)
		}, Report: func(s string) {
			if strings.Contains(s, "acknowledged") {
				cancel()
			}
		}}
		err = subject(r, ctx)
		assert.ErrorIs(t, err, context.Canceled)
		stored, err := b.Queue.Get(job.ID)
		require.NoError(t, err)
		assert.Equal(t, "cancelled", stored.State)
		assert.NotEmpty(t, stored.BundleSHA)
	})
}

func TestDeviceCleanup(t *testing.T) {
	subject := Execute
	t.Run("Cleanup failure blocks further execution with uncertain outcome", func(t *testing.T) {
		dir := t.TempDir()
		out := subject(context.Background(), Job{ID: ID()}, Profile{Command: "/bin/echo", Cwd: t.TempDir(), CleanupCommand: "/bin/sh", CleanupArgs: []string{"-c", "exit 3"}}, dir)
		assert.Equal(t, "uncertain", out.State)
		assert.Equal(t, 0, out.ExitCode)
		assert.Contains(t, out.Message, "cleanup")
		assert.Contains(t, unpack(t, filepath.Join(dir, "bundle.tar.gz")), "job.log")
	})
	t.Run("Required fresh output cannot be replaced by stale checkout evidence", func(t *testing.T) {
		dir, cwd := t.TempDir(), t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(cwd, "screen.png"), []byte("old"), 0600))
		out := subject(context.Background(), Job{ID: ID()}, Profile{Command: "/bin/echo", Cwd: cwd, RequiredOutputs: []string{"screen.png"}}, dir)
		assert.Equal(t, "failed", out.State)
		assert.Contains(t, out.Message, "required job output")
		assert.Equal(t, 0, out.ExitCode)
	})
}

func TestNetworkLoss(t *testing.T) {
	subject := (*Runner).Run
	t.Run("Running job survives broker connection loss and reconnects once", func(t *testing.T) {
		b, controller, client := testBroker(t)
		job, err := b.Queue.Submit(Job{ID: ID(), Worker: "mac", Profile: "ios-test", Timeout: 60})
		require.NoError(t, err)
		var offline atomic.Bool
		var executions atomic.Int32
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if offline.Load() {
				http.Error(w, "offline simulation", 503)
				return
			}
			b.Handler().ServeHTTP(w, r)
		}))
		defer server.Close()
		client.URL = server.URL
		client.HTTP = server.Client()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		reconnected := make(chan struct{})
		r := &Runner{Client: client, Config: WorkerConfig{Name: "mac", StateDir: t.TempDir(), Profiles: map[string]Profile{"ios-test": {Command: "/bin/echo", Cwd: t.TempDir()}}}, Execute: func(ctx context.Context, j Job, p Profile, dir string) Result {
			executions.Add(1)
			offline.Store(true)
			return Execute(ctx, j, p, dir)
		}, Report: func(s string) {
			if strings.Contains(s, "delivery interrupted") {
				offline.Store(false)
				select {
				case <-reconnected:
				default:
					close(reconnected)
				}
			}
			if strings.Contains(s, "acknowledged") {
				cancel()
			}
		}}
		err = subject(r, ctx)
		assert.ErrorIs(t, err, context.Canceled)
		assert.EqualValues(t, 1, executions.Load())
		select {
		case <-reconnected:
		default:
			t.Fatal("did not exercise reconnect")
		}
		var stored Job
		require.NoError(t, controller.Call(context.Background(), "GET", "/v1/jobs/"+job.ID, nil, &stored))
		assert.Equal(t, "succeeded", stored.State)
		assert.NotEmpty(t, stored.BundleSHA)
	})
	t.Run("Stale worker heartbeat reports uncertainty without releasing assignment", func(t *testing.T) {
		b, controller, _ := testBroker(t)
		job, err := b.Queue.Submit(Job{ID: ID(), Worker: "mac", Profile: "ios-test", Timeout: 60})
		require.NoError(t, err)
		owner := ID()
		_, err = b.Queue.Poll("mac", owner)
		require.NoError(t, err)
		b.Queue.mu.Lock()
		b.Queue.jobs[0].Seen = time.Now().Add(-time.Minute)
		b.Queue.mu.Unlock()
		var status Job
		require.NoError(t, controller.Call(context.Background(), "GET", "/v1/jobs/"+job.ID, nil, &status))
		assert.Equal(t, "disconnected", status.State)
		assert.Contains(t, status.Warning, "unknown")
		claimed, err := b.Queue.Poll("mac", owner)
		require.NoError(t, err)
		assert.Equal(t, job.ID, claimed.ID)
	})
}
