package commands

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"matcode/internal/store"
)

// captureStdout runs fn and returns everything it printed.
func captureStdout(t *testing.T, fn func() error) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	runErr := fn()
	os.Stdout = old
	w.Close()
	var b strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		b.Write(buf[:n])
		if err != nil {
			break
		}
	}
	r.Close()
	if runErr != nil {
		t.Fatalf("command failed: %v", runErr)
	}
	return b.String()
}

// statsModel mirrors one -json models[] entry.
type statsModel struct {
	Model    string  `json:"model"`
	Sessions int     `json:"sessions"`
	Messages int     `json:"messages"`
	TokensIn int64   `json:"tokens_in"`
	Cost     float64 `json:"cost"`
}

// statsPayload mirrors the -json document.
type statsPayload struct {
	Days      int          `json:"days"`
	Sessions  int          `json:"sessions"`
	Messages  int          `json:"messages"`
	TokensIn  int64        `json:"tokens_in"`
	TokensOut int64        `json:"tokens_out"`
	Cost      float64      `json:"cost"`
	Models    []statsModel `json:"models"`
}

func decodeStats(t *testing.T, out string) statsPayload {
	t.Helper()
	var p statsPayload
	if err := json.Unmarshal([]byte(out), &p); err != nil {
		t.Fatalf("bad JSON: %v\n%s", err, out)
	}
	return p
}

func TestStatsJSONTotalsAndDays(t *testing.T) {
	cfg := withProject(t)
	// Session A: recent, mock/t, 100/50 tokens, $0.02, 2 messages.
	a, err := store.Create(cfg.SessionsDir(), "mock/t")
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"user", "assistant"} {
		if err := a.Append(&store.Message{Role: role, Content: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	a.Meta.TokensIn, a.Meta.TokensOut, a.Meta.Cost = 100, 50, 0.02
	if err := a.Save(); err != nil {
		t.Fatal(err)
	}
	// Session B: 48h old, other/t, 300/70, $0.04, 1 message.
	b, err := store.Create(cfg.SessionsDir(), "other/t")
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Append(&store.Message{Role: "user", Content: "x"}); err != nil {
		t.Fatal(err)
	}
	b.Meta.TokensIn, b.Meta.TokensOut, b.Meta.Cost = 300, 70, 0.04
	b.Meta.Updated = time.Now().Add(-48 * time.Hour).UnixMilli()
	if err := b.Save(); err != nil {
		t.Fatal(err)
	}

	all := decodeStats(t, captureStdout(t, func() error { return Stats([]string{"-json"}) }))
	if all.Sessions != 2 || all.Messages != 3 || all.TokensIn != 400 || all.TokensOut != 120 {
		t.Fatalf("all-time totals: %+v", all)
	}
	if all.Cost < 0.059 || all.Cost > 0.061 {
		t.Fatalf("cost = %v, want 0.06", all.Cost)
	}
	if len(all.Models) != 2 {
		t.Fatalf("want 2 model rows, got %d", len(all.Models))
	}
	// Cost-descending order: other/t ($0.04) first.
	if all.Models[0].Model != "other/t" || all.Models[0].Sessions != 1 || all.Models[0].Messages != 1 {
		t.Fatalf("model row: %+v", all.Models[0])
	}

	window := decodeStats(t, captureStdout(t, func() error {
		return Stats([]string{"-json", "-days", "1"})
	}))
	if window.Sessions != 1 || window.TokensIn != 100 || window.Days != 1 ||
		window.TokensOut != 50 || window.Messages != 2 {
		t.Fatalf("24h totals: %+v", window)
	}
	if len(window.Models) != 1 || window.Models[0].Model != "mock/t" {
		t.Fatalf("24h models: %+v", window.Models)
	}
	// Model rows add up to the totals.
	var sum int64
	for _, m := range window.Models {
		sum += m.TokensIn
	}
	if sum != window.TokensIn {
		t.Fatalf("model rows sum to %d, totals say %d", sum, window.TokensIn)
	}
}

func TestStatsTextOutputs(t *testing.T) {
	cfg := withProject(t)
	s, err := store.Create(cfg.SessionsDir(), "mock/t")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Append(&store.Message{Role: "user", Content: "x"}); err != nil {
		t.Fatal(err)
	}
	s.Meta.TokensIn, s.Meta.TokensOut, s.Meta.Cost = 10, 5, 0.005
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}

	table := captureStdout(t, func() error { return Stats(nil) })
	for _, want := range []string{"sessions   1", "messages   1", "tokens     10 in / 5 out", "cost       $0.0050"} {
		if !strings.Contains(table, want) {
			t.Errorf("table missing %q:\n%s", want, table)
		}
	}

	models := captureStdout(t, func() error { return Stats([]string{"-models"}) })
	if !strings.Contains(models, "MODEL") || !strings.Contains(models, "mock/t") {
		t.Errorf("-models output:\n%s", models)
	}

	cost := captureStdout(t, func() error { return Stats([]string{"-cost", "-models"}) })
	if strings.Contains(cost, "messages") {
		t.Errorf("-cost must not print message counts:\n%s", cost)
	}
	if !strings.Contains(cost, "$0.0050") {
		t.Errorf("-cost output:\n%s", cost)
	}

	// Age the session out of the window: an empty range must say so.
	s.Meta.Updated = time.Now().Add(-72 * time.Hour).UnixMilli()
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	empty := captureStdout(t, func() error { return Stats([]string{"-days", "1"}) })
	if !strings.Contains(empty, "no sessions in range") {
		t.Errorf("empty range message missing: %q", empty)
	}
}

func TestStatsErrors(t *testing.T) {
	withProject(t)
	if err := Stats([]string{"-days", "-1"}); err == nil {
		t.Error("negative -days must fail")
	}
	if err := Stats([]string{"-nope"}); err == nil {
		t.Error("unknown flag must fail")
	}
	if err := Stats(nil); err != nil {
		t.Errorf("stats on empty project: %v", err)
	}
}
