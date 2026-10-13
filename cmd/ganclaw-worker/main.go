// ganclaw-worker is an isolated build/test worker proof of concept.
// It never starts the gateway, bot polling, providers or scheduled agent turns.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/kyleparisi/ganclaw/internal/workers"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "ganclaw-worker:", err)
		os.Exit(1)
	}
}
func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: ganclaw-worker broker|worker|submit|status|cancel|resolve|artifact [flags]")
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	config := fs.String("config", "", "JSON config (broker/worker)")
	raw := fs.String("url", "", "HTTPS broker URL (client)")
	tokenPath := fs.String("token-file", "", "controller credential file (client)")
	id := fs.String("id", "", "stable job ID; use same ID when retrying submit")
	name := fs.String("worker", "", "named worker")
	profile := fs.String("profile", "", "locally approved profile")
	revision := fs.String("revision", "", "exact trusted checkout commit (optional)")
	timeout := fs.Int("timeout", 1800, "execution deadline seconds, 1..7200")
	output := fs.String("output", "", "artifact destination .tar.gz")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if args[0] == "broker" {
		var c workers.BrokerConfig
		if err := workers.LoadConfig(*config, &c); err != nil {
			return err
		}
		return workers.ServeBroker(ctx, c)
	}
	if args[0] == "worker" {
		var c workers.WorkerConfig
		if err := workers.LoadConfig(*config, &c); err != nil {
			return err
		}
		token, err := workers.Token(c.TokenFile)
		if err != nil {
			return err
		}
		client, err := workers.NewClient(c.URL, token)
		if err != nil {
			return err
		}
		r := workers.Runner{Client: client, Config: c, Report: func(s string) { fmt.Fprintln(os.Stderr, s) }}
		return r.Run(ctx)
	}
	token, err := workers.Token(*tokenPath)
	if err != nil {
		return err
	}
	client, err := workers.NewClient(*raw, token)
	if err != nil {
		return err
	}
	var out any
	switch args[0] {
	case "submit":
		if *id == "" {
			*id = workers.ID()
			fmt.Fprintln(os.Stderr, "job ID (retain for retries):", *id)
		}
		var j workers.Job
		err = client.Call(ctx, "POST", "/v1/jobs", workers.Job{ID: *id, Worker: *name, Profile: *profile, Revision: *revision, Timeout: *timeout}, &j)
		out = j
	case "status":
		var j workers.Job
		err = client.Call(ctx, "GET", "/v1/jobs/"+*id, nil, &j)
		out = j
	case "cancel", "resolve":
		var o struct{}
		err = client.Call(ctx, "POST", "/v1/jobs/"+*id+"/"+args[0], nil, &o)
		out = o
	case "artifact":
		if *output == "" {
			return errors.New("--output required")
		}
		resp, e := client.Request(ctx, "GET", "/v1/jobs/"+*id+"/bundle", nil, "")
		if e != nil {
			return e
		}
		defer resp.Body.Close()
		f, e := os.CreateTemp(filepath.Dir(*output), ".artifact-*")
		if e != nil {
			return e
		}
		defer os.Remove(f.Name())
		defer f.Close()
		hash := sha256.New()
		n, e := io.Copy(io.MultiWriter(f, hash), io.LimitReader(resp.Body, workers.MaxBundle+1))
		if e != nil {
			return e
		}
		if n > workers.MaxBundle || hex.EncodeToString(hash.Sum(nil)) != resp.Header.Get("X-Content-SHA256") {
			return errors.New("artifact size or checksum mismatch")
		}
		if e = f.Sync(); e != nil {
			return e
		}
		if e = f.Close(); e != nil {
			return e
		}
		return os.Rename(f.Name(), *output)
	default:
		return errors.New("unknown command")
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(out)
}
