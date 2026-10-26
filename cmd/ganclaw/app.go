package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/kyleparisi/ganclaw/internal/api"
	"github.com/kyleparisi/ganclaw/internal/config"
)

// serveApp serves the app channel until ctx is cancelled.
func serveApp(ctx context.Context, cfg config.App, h http.Handler, log *slog.Logger) error {
	srv := &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		// No write timeout: WebSockets and large downloads are long-lived.
	}
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return fmt.Errorf("app listen %s: %w", cfg.Listen, err)
	}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	log.Info("app channel listening", "addr", ln.Addr().String(), "tls", cfg.TLSCert != "")
	if cfg.TLSCert != "" {
		err = srv.ServeTLS(ln, cfg.TLSCert, cfg.TLSKey)
	} else {
		err = srv.Serve(ln)
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// pairResponse builds the pairing reply, including a link the app opens to
// fill in the server and code.
func pairResponse(publicURL, code string, expires time.Time) api.PairResponse {
	r := api.PairResponse{Code: code, ExpiresAt: expires.UTC(), URL: publicURL}
	if publicURL != "" {
		q := url.Values{"server": {publicURL}, "code": {code}}
		r.Link = "ganclaw://pair?" + q.Encode()
	}
	return r
}

// cmdApp manages phones paired with the app channel.
func cmdApp(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return appUsage()
	}
	fs := flag.NewFlagSet("app "+args[0], flag.ExitOnError)
	cf := addClientFlags(fs)
	name := fs.String("name", "", "device name, e.g. \"Kyle's iPhone\" (pair)")
	fs.Parse(args[1:])

	c, err := cf.client()
	if err != nil {
		return err
	}
	switch args[0] {
	case "pair":
		r, err := c.Pair(ctx, strings.TrimSpace(*name))
		if err != nil {
			return err
		}
		fmt.Printf("Pairing code: %s (expires %s)\n", r.Code, r.ExpiresAt.Local().Format("15:04"))
		if r.URL != "" {
			fmt.Printf("Server:       %s\n", r.URL)
			fmt.Printf("Link:         %s\n", r.Link)
		} else {
			fmt.Println("Server:       set [app] public_url to show the address phones should use")
		}
		return nil
	case "devices":
		ds, err := c.Devices(ctx)
		if err != nil {
			return err
		}
		if len(ds) == 0 {
			fmt.Println("No paired devices. Run `ganclaw app pair`.")
			return nil
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tNAME\tPAIRED\tLAST SEEN")
		for _, d := range ds {
			seen := "never"
			if d.LastSeen != nil {
				seen = d.LastSeen.Local().Format("2006-01-02 15:04")
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", d.ID, d.Name, d.Created.Local().Format("2006-01-02"), seen)
		}
		return tw.Flush()
	case "unpair":
		if fs.NArg() != 1 {
			return &exitError{code: exitUsage, err: errors.New("usage: ganclaw app unpair <device-id>")}
		}
		if err := c.Unpair(ctx, fs.Arg(0)); err != nil {
			return err
		}
		fmt.Println("Unpaired", fs.Arg(0))
		return nil
	default:
		return appUsage()
	}
}

func appUsage() error {
	return &exitError{code: exitUsage, err: errors.New(`usage: ganclaw app <command> [flags]

  pair [--name NAME]   print a one-time code that pairs a phone (valid 10 minutes)
  devices              list paired phones
  unpair <device-id>   revoke a phone's access`)}
}
