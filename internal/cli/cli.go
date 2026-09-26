// Package cli implements matcode's subcommands. One file per command.
package cli

import (
	"context"
	"fmt"

	"matcode/internal/commands"
)

// subcommands is the dispatch table; also used to tell a subcommand
// from a bare prompt (`mtc "fix the build"`).
var subcommands = map[string]bool{
	"run": true, "tui": true, "mini": true, "revert": true, "restore": true,
	"undo": true, "redo": true, "session": true, "stats": true, "models": true,
	"api": true, "serve": true, "debug": true, "mcp": true, "version": true,
	"doctor": true, "plugin": true, "index": true, "upgrade": true,
}

// IsSubcommand reports whether name selects a known subcommand.
func IsSubcommand(name string) bool { return subcommands[name] }

// Dispatch routes a subcommand name to its implementation.
func Dispatch(ctx context.Context, name string, args []string) error {
	switch name {
	case "run":
		return commands.Run(ctx, args)
	case "tui":
		return commands.Tui(ctx, args)
	case "mini":
		return commands.Mini(ctx, args)
	case "revert":
		return commands.Revert(ctx, args)
	case "restore":
		return commands.Restore(ctx, args)
	case "undo":
		return commands.Undo(ctx, args)
	case "redo":
		return commands.Redo(ctx, args)
	case "session":
		return commands.Session(ctx, args)
	case "stats":
		return commands.Stats(args)
	case "models":
		return commands.Models(args)
	case "api":
		return commands.API(args)
	case "serve":
		return commands.Serve(ctx, args)
	case "debug":
		return commands.Debug(args)
	case "mcp":
		return commands.MCP(args)
	case "doctor":
		return commands.Doctor(ctx, args)
	case "index":
		return commands.Index(args)
	case "upgrade":
		return commands.Upgrade(args)
	case "plugin":
		return commands.Plugin(args)
	case "version":
		fmt.Println("matcode 0.0.1")
		return nil
	default:
		return fmt.Errorf("unknown command %q (try: mtc tui, mtc mini, mtc run, mtc undo)", name)
	}
}
