// Command matcode is the mtc entrypoint: a thin dispatcher over subcommands.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"matcode/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	args := os.Args[1:]
	// Bare `mtc` starts the TUI (spec §2: mtc tui is the default); an
	// unknown first word is a prompt, so `mtc "fix the build"` still runs.
	name := "tui"
	if len(args) > 0 {
		if cli.IsSubcommand(args[0]) {
			name = args[0]
			args = args[1:]
		} else {
			// Neither a subcommand nor a flag: treat the whole argv as a
			// prompt so `mtc fix the build` behaves like `mtc run ...`.
			name = "run"
		}
	}

	if err := cli.Dispatch(ctx, name, args); err != nil {
		fmt.Fprintln(os.Stderr, "mtc: "+err.Error())
		os.Exit(1)
	}
}
