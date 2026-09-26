package commands

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	"matcode/internal/catalog"
)

// ctxCol formats a catalog context window for the -verbose table.
func ctxCol(n int) string {
	if n == 0 {
		return "-"
	}
	return strconv.Itoa(n)
}

// modelRow is one provider line of `mtc models`.
type modelRow struct {
	Name         string `json:"name"`
	Dialect      string `json:"dialect"`
	BaseURL      string `json:"base_url"`
	KeyEnv       string `json:"key_env,omitempty"`
	KeySet       bool   `json:"key_set"`
	DefaultModel string `json:"default_model,omitempty"`
	Active       bool   `json:"active"`
	// Context/Price come from the embedded models.dev catalog (0/"-" =
	// the default model is unknown to it).
	Context int    `json:"context,omitempty"`
	Price   string `json:"price,omitempty"`
}

// Models implements `mtc models [provider] [-verbose] [-json]`: the merged
// provider catalog from config, enriched with the embedded models.dev
// catalog (context window, $/1M rates) for each provider's default model.
func Models(args []string) error {
	fs := flag.NewFlagSet("models", flag.ContinueOnError)
	verbose := fs.Bool("verbose", false, "also show dialect and base URL")
	jsonOut := fs.Bool("json", false, "machine-readable JSON")
	if err := parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return fmt.Errorf("usage: mtc models [provider] [-verbose] [-json]")
	}
	cfg, _, err := sessionConfig()
	if err != nil {
		return err
	}
	names := make([]string, 0, len(cfg.Providers))
	for name := range cfg.Providers {
		names = append(names, name)
	}
	sort.Strings(names)

	want := fs.Arg(0)
	if want != "" {
		if _, ok := cfg.Providers[want]; !ok {
			known := strings.Join(names, ", ")
			return fmt.Errorf("unknown provider %q (configured: %s)", want, known)
		}
	}
	rows := make([]modelRow, 0, len(names))
	for _, name := range names {
		if want != "" && name != want {
			continue
		}
		p := cfg.Providers[name]
		dialect := p.Dialect
		if dialect == "" {
			dialect = "openai"
		}
		keySet := false
		if p.APIKey.Env != "" {
			keySet = os.Getenv(p.APIKey.Env) != ""
		}
		ctx := 0
		price := "-"
		if p.DefaultModel != "" {
			if _, _, m, ok := catalog.Lookup(name + "/" + p.DefaultModel); ok {
				ctx = m.Context
				if usd, ok := catalog.Price(name+"/"+p.DefaultModel, 1000000, 0); ok {
					price = fmt.Sprintf("$%.2f/M", usd)
				}
			}
		}
		rows = append(rows, modelRow{
			Name:         name,
			Dialect:      dialect,
			BaseURL:      p.BaseURL,
			KeyEnv:       p.APIKey.Env,
			KeySet:       keySet,
			DefaultModel: p.DefaultModel,
			Active:       cfg.Model != "" && strings.HasPrefix(cfg.Model, name+"/"),
			Context:      ctx,
			Price:        price,
		})
	}
	if *jsonOut {
		payload := struct {
			Model     string     `json:"model"`
			Providers []modelRow `json:"providers"`
		}{Model: cfg.Model, Providers: rows}
		b, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(b))
		return nil
	}
	if len(rows) == 0 {
		fmt.Println("no providers configured")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	if *verbose {
		fmt.Fprintln(w, "PROVIDER\tDIALECT\tBASE URL\tDEFAULT MODEL\tCTX\t$/1M IN\tAPI KEY ENV\tKEY")
	} else {
		fmt.Fprintln(w, "PROVIDER\tDEFAULT MODEL\tAPI KEY ENV\tKEY")
	}
	for _, r := range rows {
		key := "-"
		switch {
		case r.KeyEnv == "":
			key = "-"
		case r.KeySet:
			key = "set"
		default:
			key = "missing"
		}
		if *verbose {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				r.Name, r.Dialect, orDash(r.BaseURL), orDash(r.DefaultModel),
				ctxCol(r.Context), r.Price, orDash(r.KeyEnv), key)
			continue
		}
		mark := r.DefaultModel
		if r.Active {
			mark = r.DefaultModel + " *"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", r.Name, orDash(mark), orDash(r.KeyEnv), key)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if cfg.Model != "" {
		fmt.Printf("\nmodel      %s (from config; -model overrides per run)\n", cfg.Model)
	}
	return nil
}
