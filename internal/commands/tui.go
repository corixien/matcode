package commands

import (
	"context"
	"flag"
	"fmt"
	"os"

	"matcode/internal/app"
	"matcode/internal/config"
	"matcode/internal/tui"
	"matcode/internal/tui/theme"
)

// Tui executes `mtc tui [-agent a] [-model p/m] [-session id]`: the
// interactive terminal UI (spec §10). `mtc` with no subcommand lands here.
func Tui(ctx context.Context, args []string) error {
	opts, cfg, err := tuiFlags(args, "mtc tui [-agent build|plan|summary|title] [-model provider/model] [-session ses_...]")
	if err != nil {
		return err
	}
	_ = ctx
	_ = cfg
	return tui.Run(opts)
}

// Mini executes `mtc mini [-agent a] [-model p/m] [-session id]`: one
// prompt line over a scrolling log, no overlays (spec §10 row 30).
func Mini(ctx context.Context, args []string) error {
	opts, _, err := tuiFlags(args, "mtc mini [-agent a] [-model p/m] [-session ses_...]")
	if err != nil {
		return err
	}
	return tui.RunMini(ctx, opts)
}

// tuiFlags parses the shared TUI/mini flag set into tui.Options.
func tuiFlags(args []string, usage string) (tui.Options, *config.Config, error) {
	fs := flag.NewFlagSet("tui", flag.ContinueOnError)
	agentFlag := fs.String("agent", "", "agent id (overrides config)")
	modelFlag := fs.String("model", "", "provider/model (overrides config)")
	sessionFlag := fs.String("session", "", "resume an existing session id")
	if err := fs.Parse(args); err != nil {
		return tui.Options{}, nil, err
	}
	if fs.NArg() > 0 {
		return tui.Options{}, nil, fmt.Errorf("usage: %s", usage)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return tui.Options{}, nil, err
	}
	cfg, err := config.Load(cwd)
	if err != nil {
		return tui.Options{}, nil, err
	}
	return tui.Options{
		Cwd:     cwd,
		Config:  cfg,
		Agent:   *agentFlag,
		Model:   *modelFlag,
		Session: *sessionFlag,
	}, cfg, nil
}

// ensure app is referenced: the TUI builds its own engines through it.
var _ = app.Options{}

// themeName exposes the configured theme for mini mode.
func themeName(cfg *config.Config) string {
	if cfg == nil || cfg.Theme == "" {
		return "default"
	}
	return cfg.Theme
}

// themeFor resolves the configured palette.
func themeFor(cfg *config.Config) theme.Theme {
	t, _ := theme.Get(cfg.ThemesDir(), themeName(cfg))
	return t
}
