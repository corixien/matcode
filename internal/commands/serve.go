package commands

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"matcode/internal/server"
)

// Serve runs `mtc serve [-host h] [-port n]`: the HTTP + SSE API (§9) that
// `mtc api`, the TUI, and scripts all talk to. Localhost only — the port is
// the credential in the env-only auth model.
func Serve(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	host := fs.String("host", "127.0.0.1", "interface to bind (loopback default)")
	port := fs.Int("port", 0, "listen port (default: config api_port)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	srv, err := server.New(cwd)
	if err != nil {
		return err
	}

	// Ctrl-C shuts the listener down cleanly.
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := srv.Listen(ctx, *host, *port); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "mtc serve: stopped")
	return nil
}
