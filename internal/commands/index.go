package commands

import (
	"flag"
	"fmt"
	"os"

	"matcode/internal/index"
)

// Index implements `mtc index rebuild` and `mtc index search <query>`:
// the bbolt session sidecar (spec §5 line 284). Rebuild is idempotent
// and the only writer; search reads it and names rebuild when missing.
func Index(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: mtc index rebuild | mtc index search <query> [-n N]")
	}
	switch args[0] {
	case "rebuild":
		fs := flag.NewFlagSet("index rebuild", flag.ContinueOnError)
		if err := parse(fs, args[1:]); err != nil {
			return err
		}
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		cfg, _, err := sessionConfig()
		if err != nil {
			return err
		}
		_ = cwd
		n, err := index.Rebuild(cfg.DataDir())
		if err != nil {
			return err
		}
		fmt.Printf("indexed %d session(s) → %s\n", n, index.Path(cfg.DataDir()))
		return nil

	case "search":
		fs := flag.NewFlagSet("index search", flag.ContinueOnError)
		limit := fs.Int("n", 20, "maximum results")
		if err := parse(fs, args[1:]); err != nil {
			return err
		}
		if fs.NArg() < 1 {
			return fmt.Errorf("usage: mtc index search <query> [-n N]")
		}
		cfg, _, err := sessionConfig()
		if err != nil {
			return err
		}
		hits, err := index.Search(cfg.DataDir(), fs.Arg(0), *limit)
		if err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("%s: run `mtc index rebuild` first", err)
			}
			return err
		}
		if len(hits) == 0 {
			fmt.Println("no matches")
			return nil
		}
		for _, h := range hits {
			if h.Title != "" {
				fmt.Printf("%s  %s\n", h.ID, h.Title)
			} else {
				fmt.Println(h.ID)
			}
		}
		return nil

	default:
		return fmt.Errorf("unknown index action %q (want rebuild or search)", args[0])
	}
}
