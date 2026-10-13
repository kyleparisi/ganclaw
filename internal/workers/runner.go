package workers

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Profile struct {
	logsOnly        bool
	RequiredOutputs []string `json:"required_outputs"`
	CleanupCommand  string   `json:"cleanup_command"`
	CleanupArgs     []string `json:"cleanup_args"`
	// Commands and environment are trusted local configuration, never job input.
	Command           string            `json:"command"`
	Args              []string          `json:"args"`
	Cwd               string            `json:"cwd"`
	Env               map[string]string `json:"env"`
	Artifacts         []string          `json:"artifacts"`
	RequiredArtifacts []string          `json:"required_artifacts"`
}
type WorkerConfig struct {
	URL       string             `json:"url"`
	Name      string             `json:"name"`
	TokenFile string             `json:"token_file"`
	StateDir  string             `json:"state_dir"`
	Profiles  map[string]Profile `json:"profiles"`
}
type Journal struct {
	Job    Job     `json:"job"`
	Result *Result `json:"result,omitempty"`
}
type Runner struct {
	Client *Client
	Config WorkerConfig
	// Execute is replaceable for transport/recovery tests.
	Execute func(context.Context, Job, Profile, string) Result
	// Report receives safe progress messages; credentials and command output are never logged.
	Report func(string)
}

func LoadConfig(path string, out any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 1<<20))
	d.DisallowUnknownFields()
	if err = d.Decode(out); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("trailing config data")
	}
	return nil
}
func (p Profile) Validate() error {
	if !filepath.IsAbs(p.Command) || !filepath.IsAbs(p.Cwd) {
		return errors.New("profile command and cwd must be absolute")
	}
	if _, err := os.Stat(p.Command); err != nil {
		return err
	}
	if s, err := os.Stat(p.Cwd); err != nil || !s.IsDir() {
		return errors.New("profile cwd must exist")
	}
	if p.CleanupCommand != "" {
		if !filepath.IsAbs(p.CleanupCommand) {
			return errors.New("cleanup command must be absolute")
		}
		if _, err := os.Stat(p.CleanupCommand); err != nil {
			return err
		}
	}
	for _, a := range append(append(append([]string(nil), p.Artifacts...), p.RequiredArtifacts...), p.RequiredOutputs...) {
		if !fs.ValidPath(a) || a == "." {
			return errors.New("artifact paths must be relative and confined")
		}
	}
	for k := range p.Env {
		if strings.ContainsAny(k, "=\x00") || strings.HasPrefix(k, "GANCLAW_") {
			return errors.New("invalid or reserved profile env")
		}
	}
	return nil
}
func (r *Runner) Run(ctx context.Context) error {
	if os.Getuid() == 0 {
		return errors.New("worker must run as a normal user, not root")
	}
	if r.Config.Name == "" || len(r.Config.Profiles) == 0 {
		return errors.New("worker name and profiles required")
	}
	for _, p := range r.Config.Profiles {
		if err := p.Validate(); err != nil {
			return err
		}
	}
	lock, err := OpenQueue(r.Config.StateDir)
	if err != nil {
		return err
	}
	defer lock.Close()
	instancePath := filepath.Join(r.Config.StateDir, "instance.json")
	var instance string
	if err := LoadConfig(instancePath, &instance); errors.Is(err, os.ErrNotExist) {
		instance = ID()
		if err = atomicJSON(instancePath, instance); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if !validID(instance) {
		return errors.New("invalid worker instance state")
	}
	r.Client.Instance = instance

	journalPath := filepath.Join(r.Config.StateDir, "journal.json")
	// A crash between recording start and recording completion cannot safely be replayed.
	var recovered Journal
	if data, err := os.ReadFile(journalPath); err == nil {
		if err = json.Unmarshal(data, &recovered); err != nil {
			return err
		}
		if recovered.Result == nil {
			_ = bundle(filepath.Join(r.Config.StateDir, recovered.Job.ID), Profile{logsOnly: true})
			recovered.Result = &Result{State: "uncertain", ExitCode: -1, Message: "worker restarted after accepting job; verify old process/device state before resolving"}
			if err = atomicJSON(journalPath, recovered); err != nil {
				return err
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for ctx.Err() == nil {
		var j *Job
		if recovered.Result != nil {
			j = &recovered.Job
		} else {
			if err = r.Client.Call(ctx, "POST", workerPath(r.Config.Name)+"/poll", nil, &j); err != nil {
				r.report("broker unreachable; retrying without redispatch")
				if !pause(ctx) {
					break
				}
				continue
			}
			if j == nil {
				if !pause(ctx) {
					break
				}
				continue
			}
		}
		if terminal(j.State) {
			return errors.New("broker returned terminal job")
		}
		if recovered.Result == nil {
			if j.State == "uncertain" {
				return errors.New("worker blocked by uncertain outcome; operator resolution required")
			}
			local := Journal{Job: *j}
			if err = atomicJSON(journalPath, local); err != nil {
				return err
			}
			p, ok := r.Config.Profiles[j.Profile]
			outDir := filepath.Join(r.Config.StateDir, j.ID)
			if err = os.MkdirAll(outDir, 0700); err != nil {
				return err
			}
			if err = os.MkdirAll(filepath.Join(outDir, "output"), 0700); err != nil {
				return err
			}
			var result Result
			if !ok {
				result = Result{State: "failed", ExitCode: -1, Message: "profile not approved locally"}
				result = writeFailureBundle(outDir, result)
			} else {
				result = r.runJob(ctx, *j, p, outDir)
			}
			local.Result = &result
			if err = atomicJSON(journalPath, local); err != nil {
				return err
			}
			recovered = local
		}
		if err = r.deliver(ctx, recovered); err != nil {
			r.report("result delivery interrupted; keeping local result for retry")
			if !pause(ctx) {
				break
			}
			continue
		}
		if recovered.Result.State == "uncertain" {
			r.report("uncertain outcome reported; worker is blocked")
			// Do not exit/relaunch/execute while the broker still holds this uncertainty.
			return errors.New("uncertain outcome: operator must verify processes, resolve broker job and archive journal")
		}
		if err = os.Remove(journalPath); err != nil {
			return err
		}
		recovered = Journal{}
		r.report("job " + j.ID + " result and artifact acknowledged")
	}
	return ctx.Err()
}
func writeFailureBundle(dir string, result Result) Result {
	if err := os.WriteFile(filepath.Join(dir, "job.log"), []byte(result.Message+"\n"), 0600); err != nil {
		return Result{State: "uncertain", ExitCode: -1, Message: "cannot persist failure log"}
	}
	if err := bundle(dir, Profile{}); err != nil {
		return Result{State: "uncertain", ExitCode: -1, Message: "cannot persist failure artifact"}
	}
	return result
}
func (r *Runner) report(s string) {
	if r.Report != nil {
		r.Report(s)
	}
}
func pause(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(2 * time.Second):
		return true
	}
}
func (r *Runner) runJob(ctx context.Context, j Job, p Profile, dir string) Result {
	jobCtx, cancel := context.WithDeadline(ctx, j.Deadline)
	defer cancel()
	if j.Cancel {
		cancel()
	}
	execute := r.Execute
	if execute == nil {
		execute = Execute
	}
	resultc := make(chan Result, 1)
	go func() { resultc <- execute(jobCtx, j, p, dir) }()
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	cancelled := j.Cancel
	for {
		select {
		case result := <-resultc:
			if cancelled && result.State != "uncertain" {
				result.State = "cancelled"
			}
			return result
		case <-tick.C:
			var current *Job
			if err := r.Client.Call(ctx, "POST", workerPath(r.Config.Name)+"/poll", nil, &current); err == nil && current != nil {
				if current.ID != j.ID {
					cancel()
					return Result{State: "uncertain", ExitCode: -1, Message: "broker assignment changed while executing"}
				}
				if current.Cancel {
					cancelled = true
					cancel()
				}
			}
		}
	}
}
func (r *Runner) deliver(ctx context.Context, j Journal) error {
	base := workerPath(r.Config.Name) + "/jobs/" + j.Job.ID
	upload := func() error {
		f, err := os.Open(filepath.Join(r.Config.StateDir, j.Job.ID, "bundle.tar.gz"))
		if err != nil {
			return err
		}
		defer f.Close()
		hash := sha256.New()
		if _, err = io.Copy(hash, f); err != nil {
			return err
		}
		if _, err = f.Seek(0, 0); err != nil {
			return err
		}
		resp, err := r.Client.Request(ctx, "PUT", base+"/bundle", f, hex.EncodeToString(hash.Sum(nil)))
		if err != nil {
			return err
		}
		resp.Body.Close()
		return nil
	}
	if j.Result.State != "uncertain" || fileExists(filepath.Join(r.Config.StateDir, j.Job.ID, "bundle.tar.gz")) {
		if err := upload(); err != nil {
			if j.Result.State != "uncertain" {
				return err
			}
			r.report("uncertain evidence unavailable; reporting outcome without artifact")
		}
	}
	var out struct{}
	return r.Client.Call(ctx, "POST", base+"/result", j.Result, &out)
}

type cappedLog struct {
	mu        sync.Mutex
	f         *os.File
	n         int64
	truncated bool
}

func (l *cappedLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := len(p)
	left := int64(16<<20) - l.n
	if left > 0 {
		part := p
		if int64(len(part)) > left {
			part = part[:left]
		}
		written, err := l.f.Write(part)
		l.n += int64(written)
		if err != nil {
			return written, err
		}
	}
	if int64(n) > left && !l.truncated {
		l.truncated = true
		if _, err := l.f.WriteString("\n[log truncated at 16 MiB]\n"); err != nil {
			return 0, err
		}
	}
	return n, nil
}

// Execute runs one fixed profile with a clean, explicitly selected environment.
// Process-group cancellation prevents children from surviving a timeout/cancel.
func Execute(ctx context.Context, j Job, p Profile, dir string) Result {
	log, err := os.OpenFile(filepath.Join(dir, "job.log"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return Result{State: "uncertain", ExitCode: -1, Message: "cannot create log"}
	}
	result := Result{State: "failed", ExitCode: -1}
	writer := &cappedLog{f: log}
	err = func() error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// The POC never fetches arbitrary repositories or checks out caller-supplied code.
		// Pin a trusted local checkout by exact commit; fail if tracked/untracked files differ.
		if j.Revision != "" {
			if len(j.Revision) != 40 && len(j.Revision) != 64 {
				return errors.New("full commit hash required")
			}
			if _, err := hex.DecodeString(j.Revision); err != nil {
				return errors.New("invalid commit")
			}
			cmd := exec.CommandContext(ctx, "/usr/bin/git", "rev-parse", "HEAD")
			cmd.Dir = p.Cwd
			cmd.Env = profileEnv(p, dir, j.ID)
			head, err := cmd.Output()
			if err != nil || strings.TrimSpace(string(head)) != j.Revision {
				return errors.New("local checkout does not match requested commit")
			}
			cmd = exec.CommandContext(ctx, "/usr/bin/git", "status", "--porcelain")
			cmd.Dir = p.Cwd
			cmd.Env = profileEnv(p, dir, j.ID)
			status, err := cmd.Output()
			if err != nil || len(status) != 0 {
				return errors.New("trusted checkout must be clean")
			}
		}
		cmd := exec.CommandContext(ctx, p.Command, p.Args...)
		cmd.Dir = p.Cwd
		cmd.Env = profileEnv(p, dir, j.ID)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error {
			err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			if errors.Is(err, syscall.ESRCH) {
				return os.ErrProcessDone
			}
			return err
		}
		cmd.WaitDelay = 5 * time.Second
		cmd.Stdout = writer
		cmd.Stderr = writer
		err := cmd.Run()
		// Kill remaining children even on normal exit. Daemonized processes that
		// escape the group must be prohibited by trusted profiles.
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		if cmd.ProcessState != nil {
			result.ExitCode = cmd.ProcessState.ExitCode()
		}
		return err
	}()
	if err == nil {
		result.State = "succeeded"
	} else {
		result.Message = err.Error()
		fmt.Fprintln(writer, result.Message)
	}
	if ctx.Err() == context.DeadlineExceeded {
		result.State = "timed_out"
	} else if ctx.Err() != nil {
		result.State = "cancelled"
	}
	if p.CleanupCommand != "" {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 30*time.Second)
		cleanupProfile := p
		cleanupProfile.Command = p.CleanupCommand
		cleanupProfile.Args = p.CleanupArgs
		cleanup := exec.CommandContext(cleanupCtx, cleanupProfile.Command, cleanupProfile.Args...)
		cleanup.Dir = p.Cwd
		cleanup.Env = profileEnv(p, dir, j.ID)
		cleanup.Stdout = writer
		cleanup.Stderr = writer
		cleanup.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cleanup.Cancel = func() error {
			err := syscall.Kill(-cleanup.Process.Pid, syscall.SIGKILL)
			if errors.Is(err, syscall.ESRCH) {
				return os.ErrProcessDone
			}
			return err
		}
		cleanup.WaitDelay = 5 * time.Second
		cleanupErr := cleanup.Run()
		if cleanup.Process != nil {
			_ = syscall.Kill(-cleanup.Process.Pid, syscall.SIGKILL)
		}
		stop()
		if cleanupErr != nil {
			result = Result{State: "uncertain", ExitCode: result.ExitCode, Message: "device cleanup failed; operator verification required"}
		}
	}

	if err = log.Sync(); err != nil {
		result = Result{State: "uncertain", ExitCode: result.ExitCode, Message: "cannot persist execution log"}
	}
	log.Close()
	if err = bundle(dir, p); err != nil {
		result.Message = "artifact collection failed: " + err.Error()
		if result.State == "succeeded" {
			result.State = "failed"
		}
		if fallback := bundle(dir, Profile{logsOnly: true}); fallback != nil {
			result = Result{State: "uncertain", ExitCode: result.ExitCode, Message: "cannot persist artifact bundle"}
		}
	}
	return result
}

// bundle uses os.Root to confine all reads, rejects links, and bounds input/output.
func bundle(dir string, p Profile) error {
	f, err := os.OpenFile(filepath.Join(dir, "bundle.tar.gz"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	var total int64
	add := func(root *os.Root, path, name string) error {
		return fs.WalkDir(root.FS(), path, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.Type()&os.ModeSymlink != 0 {
				return errors.New("artifact symlink rejected")
			}
			if d.IsDir() {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return errors.New("nonregular artifact rejected")
			}
			total += info.Size()
			if total > MaxBundle {
				return errors.New("artifact input exceeds 256 MiB")
			}
			file, err := root.Open(path)
			if err != nil {
				return err
			}
			defer file.Close()
			h := &tar.Header{Name: filepath.ToSlash(filepath.Join(name, path)), Mode: 0600, Size: info.Size()}
			if err = tw.WriteHeader(h); err != nil {
				return err
			}
			n, err := io.CopyN(tw, file, info.Size())
			if err != nil || n != info.Size() {
				return errors.New("artifact changed during collection")
			}
			return nil
		})
	}
	spool, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer spool.Close()
	for _, path := range p.RequiredOutputs {
		info, err := spool.Stat(filepath.Join("output", path))
		if err != nil {
			return fmt.Errorf("required job output %s missing", path)
		}
		if !info.IsDir() && info.Size() == 0 {
			return fmt.Errorf("required job output %s empty", path)
		}
	}

	if err = add(spool, "job.log", ""); err != nil {
		return err
	}
	if _, err := spool.Stat("output"); err == nil && !p.logsOnly {
		if err = add(spool, "output", ""); err != nil {
			return err
		}
	}
	if p.Cwd != "" {
		root, err := os.OpenRoot(p.Cwd)
		if err != nil {
			return err
		}
		defer root.Close()
		for _, path := range p.RequiredArtifacts {
			if _, err := root.Stat(path); err != nil {
				return fmt.Errorf("required artifact %s missing", path)
			}
		}
		paths := append(append([]string(nil), p.Artifacts...), p.RequiredArtifacts...)
		seen := map[string]bool{}
		for _, path := range paths {
			if seen[path] {
				continue
			}
			seen[path] = true
			if _, err := root.Stat(path); errors.Is(err, os.ErrNotExist) {
				continue
			} else if err != nil {
				return err
			}
			if err = add(root, path, "artifacts"); err != nil {
				return err
			}
		}
	}
	if err = tw.Close(); err != nil {
		return err
	}
	if err = gz.Close(); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.Size() > MaxBundle {
		return errors.New("compressed artifact exceeds 256 MiB")
	}
	return nil
}

// ServeBroker serves only verified TLS and has no gateway or messaging startup.
func ServeBroker(ctx context.Context, c BrokerConfig) error {
	if c.Listen == "" || c.Certificate == "" || c.Key == "" {
		return errors.New("listen, TLS certificate and key required")
	}
	b, err := NewBroker(c)
	if err != nil {
		return err
	}
	defer b.Queue.Close()
	srv := &http.Server{TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12}, Addr: c.Listen, Handler: b.Handler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 5 * time.Minute, WriteTimeout: 5 * time.Minute, MaxHeaderBytes: 16 << 10}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
	}()
	err = srv.ListenAndServeTLS(c.Certificate, c.Key)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func fileExists(path string) bool { _, err := os.Stat(path); return err == nil }
func profileEnv(p Profile, dir, id string) []string {
	env := map[string]string{}
	for _, k := range []string{"HOME", "USER", "LOGNAME", "PATH", "TMPDIR", "LANG"} {
		if v := os.Getenv(k); v != "" {
			env[k] = v
		}
	}
	for k, v := range p.Env {
		env[k] = v
	}
	env["GANCLAW_JOB_DIR"] = dir
	env["GANCLAW_JOB_ID"] = id
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}
