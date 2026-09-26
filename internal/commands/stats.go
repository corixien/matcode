package commands

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"text/tabwriter"
	"time"

	"matcode/internal/store"
)

// statsRow is one aggregate bucket — the whole run or one model.
type statsRow struct {
	Model     string  `json:"model,omitempty"`
	Sessions  int     `json:"sessions"`
	Messages  int     `json:"messages"`
	TokensIn  int64   `json:"tokens_in"`
	TokensOut int64   `json:"tokens_out"`
	Cost      float64 `json:"cost"`
}

// Stats implements `mtc stats [-days N] [-models] [-cost] [-json]`: token
// and money totals over session.json (usage lands there in item #1).
func Stats(args []string) error {
	fs := flag.NewFlagSet("stats", flag.ContinueOnError)
	days := fs.Int("days", 0, "only sessions updated in the last N days (0 = all time)")
	byModel := fs.Bool("models", false, "add a per-model breakdown")
	costOnly := fs.Bool("cost", false, "report money only")
	jsonOut := fs.Bool("json", false, "machine-readable JSON")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *days < 0 {
		return fmt.Errorf("-days must be >= 0")
	}
	cfg, _, err := sessionConfig()
	if err != nil {
		return err
	}
	metas, err := store.List(cfg.SessionsDir())
	if err != nil {
		return err
	}
	var cutoff int64
	if *days > 0 {
		cutoff = time.Now().Add(-time.Duration(*days) * 24 * time.Hour).UnixMilli()
	}
	total := statsRow{}
	byModelMap := map[string]*statsRow{}
	for _, m := range metas {
		if cutoff > 0 && m.Updated < cutoff {
			continue
		}
		msgs := countMessages(filepath.Join(cfg.SessionsDir(), m.ID))
		total.Sessions++
		total.Messages += msgs
		total.TokensIn += m.TokensIn
		total.TokensOut += m.TokensOut
		total.Cost += m.Cost

		key := m.Model
		if key == "" {
			key = "-"
		}
		b := byModelMap[key]
		if b == nil {
			b = &statsRow{Model: key}
			byModelMap[key] = b
		}
		b.Sessions++
		b.Messages += msgs
		b.TokensIn += m.TokensIn
		b.TokensOut += m.TokensOut
		b.Cost += m.Cost
	}
	models := make([]*statsRow, 0, len(byModelMap))
	for _, b := range byModelMap {
		models = append(models, b)
	}
	sort.Slice(models, func(i, j int) bool {
		if models[i].Cost != models[j].Cost {
			return models[i].Cost > models[j].Cost
		}
		return models[i].Model < models[j].Model
	})

	if *jsonOut {
		payload := struct {
			Days int `json:"days"`
			statsRow
			Models []*statsRow `json:"models"`
		}{Days: *days, statsRow: total, Models: models}
		b, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(b))
		return nil
	}
	if total.Sessions == 0 {
		fmt.Println("no sessions in range")
		return nil
	}
	if *costOnly {
		fmt.Printf("cost       $%.4f\n", total.Cost)
		if total.Sessions > 0 && *days > 0 {
			fmt.Printf("per day    $%.6f (last %d days)\n", total.Cost/float64(*days), *days)
		}
		printModelMoney(models)
		return nil
	}
	fmt.Printf("sessions   %d\n", total.Sessions)
	fmt.Printf("messages   %d\n", total.Messages)
	fmt.Printf("tokens     %d in / %d out\n", total.TokensIn, total.TokensOut)
	fmt.Printf("cost       $%.4f\n", total.Cost)
	if *days > 0 {
		fmt.Printf("window     last %d day(s), from %s\n",
			*days, time.Now().Add(-time.Duration(*days)*24*time.Hour).Format("2006-01-02"))
	}
	if *byModel {
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "MODEL\tSESSIONS\tMSGS\tTOKENS IN\tTOKENS OUT\tCOST")
		for _, b := range models {
			fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%d\t$%.4f\n",
				b.Model, b.Sessions, b.Messages, b.TokensIn, b.TokensOut, b.Cost)
		}
		if err := w.Flush(); err != nil {
			return err
		}
	}
	return nil
}

func printModelMoney(models []*statsRow) {
	if len(models) == 0 {
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "MODEL\tSESSIONS\tCOST")
	for _, b := range models {
		fmt.Fprintf(w, "%s\t%d\t$%.4f\n", b.Model, b.Sessions, b.Cost)
	}
	_ = w.Flush()
}
