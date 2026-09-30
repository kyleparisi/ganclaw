package procexec

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOSStart(t *testing.T) {
	subject := OS.Start

	t.Run("Stdin reader, env, dir and stderr are wired", func(t *testing.T) {
		var stderr bytes.Buffer
		p, err := subject(context.Background(), Cmd{
			Bin:    "/bin/sh",
			Args:   []string{"-c", `read line; echo "$line|$GANCLAW_TEST|$(pwd)"; echo oops >&2`},
			Env:    []string{"GANCLAW_TEST=yes"},
			Dir:    "/",
			Stdin:  strings.NewReader("hello\n"),
			Stderr: &stderr,
		})
		require.NoError(t, err)
		assert.Nil(t, p.Stdin, "no stdin pipe when a reader is supplied")

		out, err := io.ReadAll(p.Stdout)
		require.NoError(t, err)
		require.NoError(t, p.Wait())
		assert.Equal(t, "hello|yes|/\n", string(out))
		assert.Equal(t, "oops\n", stderr.String())
	})

	t.Run("Stdin pipe when no reader is supplied", func(t *testing.T) {
		p, err := subject(context.Background(), Cmd{Bin: "/bin/cat"})
		require.NoError(t, err)
		require.NotNil(t, p.Stdin)

		_, err = io.WriteString(p.Stdin, "round trip")
		require.NoError(t, err)
		require.NoError(t, p.Stdin.Close())

		out, err := io.ReadAll(p.Stdout)
		require.NoError(t, err)
		require.NoError(t, p.Wait())
		assert.Equal(t, "round trip", string(out))
	})

	t.Run("Cancelling the context interrupts the process", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		p, err := subject(ctx, Cmd{
			Bin:  "/bin/sh",
			Args: []string{"-c", `trap 'echo interrupted; exit 3' INT; echo ready; while :; do sleep 0.05; done`},
		})
		require.NoError(t, err)
		buf := make([]byte, len("ready\n"))
		_, err = io.ReadFull(p.Stdout, buf)
		require.NoError(t, err)

		cancel()
		rest, _ := io.ReadAll(p.Stdout)
		err = p.Wait()

		assert.Equal(t, "interrupted\n", string(rest), "SIGINT, not SIGKILL, is sent first")
		assert.Error(t, err)
	})

	t.Run("Kill stops the process", func(t *testing.T) {
		p, err := subject(context.Background(), Cmd{Bin: "/bin/sleep", Args: []string{"30"}})
		require.NoError(t, err)

		start := time.Now()
		require.NoError(t, p.Kill())
		err = p.Wait()

		assert.ErrorContains(t, err, "killed")
		assert.Less(t, time.Since(start), 5*time.Second)
	})

	t.Run("Missing binary fails to start", func(t *testing.T) {
		_, err := subject(context.Background(), Cmd{Bin: "/nonexistent/ganclaw-test-binary"})

		assert.Error(t, err)
	})
}

func TestFilterEnv(t *testing.T) {
	subject := FilterEnv

	t.Run("Drops matching keys and keeps order", func(t *testing.T) {
		got := subject(
			[]string{"PATH=/bin", "GANCLAW_TOKEN=secret", "HOME=/h", "GANCLAW_OTHER=x=y", "WEIRD"},
			func(k string) bool { return strings.HasPrefix(k, "GANCLAW_") },
		)

		assert.Equal(t, []string{"PATH=/bin", "HOME=/h", "WEIRD"}, got)
	})
}
