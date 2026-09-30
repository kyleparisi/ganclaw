// Package procexec starts subprocesses behind a swappable function field so
// providers can be tested against in-memory fakes.
package procexec

import (
	"context"
	"io"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// Cmd describes a process to start.
type Cmd struct {
	Bin  string
	Args []string
	Env  []string
	Dir  string
	// Stdin, if set, is fed to the process. If nil, Proc.Stdin is a pipe
	// the caller writes to.
	Stdin io.Reader
	// Stderr receives the process's stderr. Nil discards it.
	Stderr io.Writer
}

// Proc is a running process.
type Proc struct {
	Stdin  io.WriteCloser // nil when Cmd.Stdin was set
	Stdout io.Reader
	Wait   func() error
	Kill   func() error
}

// Exec starts processes. Tests replace Start with an inline fake.
type Exec struct {
	Start func(ctx context.Context, cmd Cmd) (*Proc, error)
}

// OS starts real processes. Cancelling ctx sends SIGINT, then SIGKILL after
// 10 seconds.
var OS = Exec{Start: startOS}

func startOS(ctx context.Context, c Cmd) (*Proc, error) {
	cmd := exec.CommandContext(ctx, c.Bin, c.Args...)
	cmd.Env = c.Env
	cmd.Dir = c.Dir
	cmd.Stderr = c.Stderr
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGINT) }
	cmd.WaitDelay = 10 * time.Second

	p := &Proc{}
	if c.Stdin != nil {
		cmd.Stdin = c.Stdin
	} else {
		in, err := cmd.StdinPipe()
		if err != nil {
			return nil, err
		}
		p.Stdin = in
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	p.Stdout = out
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p.Wait = cmd.Wait
	p.Kill = func() error { return cmd.Process.Kill() }
	return p, nil
}

// FilterEnv returns env without the variables for which drop(key) is true.
func FilterEnv(env []string, drop func(key string) bool) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if !drop(k) {
			out = append(out, kv)
		}
	}
	return out
}
