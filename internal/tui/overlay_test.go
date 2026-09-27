package tui

import (
	"fmt"
	"strings"
	"testing"

	"matcode/internal/tui/theme"
)

func TestOverlayFilterIgnoresLineRange(t *testing.T) {
	o := newOverlay("mention", "attach file", []string{"range.txt", "other.md"}, theme.Default)
	o.query = "range.txt#1-2"
	if got := o.filtered(); len(got) != 1 || got[0] != "range.txt" {
		t.Fatalf("filtered() = %v, want [range.txt]", got)
	}
	if got := o.chosen(); got != "range.txt" {
		t.Fatalf("chosen() = %q, want range.txt", got)
	}
	// An empty base (only a range typed) shows everything.
	o.query = "#1-2"
	if got := o.filtered(); len(got) != 2 {
		t.Fatalf("range-only query filtered() = %v, want all items", got)
	}
	// The range still shows in the query line.
	if line := o.queryLine(); line != "@range.txt#1-2" && line != "@#1-2" {
		t.Fatalf("queryLine() = %q", line)
	}
}

func TestOverlayFilterPlain(t *testing.T) {
	o := newOverlay("mention", "attach file", []string{"a.txt", "b.txt"}, theme.Default)
	o.query = "A.TXT"
	if got := o.filtered(); len(got) != 1 || got[0] != "a.txt" {
		t.Fatalf("case-insensitive filter = %v, want [a.txt]", got)
	}
	o.query = "zzz"
	if got := o.filtered(); len(got) != 0 {
		t.Fatalf("no-match filter = %v, want empty", got)
	}
	if got := o.chosen(); got != "" {
		t.Fatalf("chosen() with no match = %q, want \"\"", got)
	}
}

// TestOverlayFuzzyRanking pins the ranking contract of filtered():
// exact beats prefix beats substring beats scattered subsequence, and
// inside a rank the shorter item wins. "cpt" therefore surfaces the
// file actually called that instead of any a-c-p-t scatter.
func TestOverlayFuzzyRanking(t *testing.T) {
	o := newOverlay("mention", "attach", []string{
		"a-x-b-c",  // subsequence only
		"xxabcxx",  // substring
		"abcdefgh", // prefix, longer
		"abc",      // exact
		"abcdef",   // prefix, shorter
	}, theme.Default)
	o.query = "abc"
	got := o.filtered()

	want := []string{"abc", "abcdef", "abcdefgh", "xxabcxx", "a-x-b-c"}
	if len(got) != len(want) {
		t.Fatalf("filtered() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("filtered() = %v, want %v", got, want)
		}
	}
	// An exact match outranks a shorter prefix match.
	o.items = []string{"abcd", "abc"}
	o.query = "abc"
	if got := o.filtered(); got[0] != "abc" {
		t.Fatalf("exact match ranked second: %v", got)
	}
}

// TestOverlayAnchoredCentersQuery proves a config menu puts the typed
// query on the frame's centre line with the options flowing below it,
// and keeps its hint on the title row so the option list can run all
// the way down to the prompt field. optRoom caps how many options are
// drawn, which is how the list ends at the prompt field.
func TestOverlayAnchoredCentersQuery(t *testing.T) {
	items := []string{"anthropic/claude-fable-5"}
	for i := 1; i <= 10; i++ {
		items = append(items, fmt.Sprintf("openai/gpt-%d", i))
	}
	o := newOverlay("model", "switch model", items, theme.Default)
	o.query = "gpt"

	lines := strings.Split(o.view(40, 80, 8), "\n")
	if len(lines) != 10 { // title + query + 8 options
		t.Fatalf("view = %d rows, want 10 (title, query, 8 options)", len(lines))
	}
	if !strings.Contains(lines[0], "enter select") {
		t.Errorf("hint not on the title row: %q", lines[0])
	}
	if !strings.Contains(lines[0], "switch model") {
		t.Errorf("title missing: %q", lines[0])
	}
	q := lines[1]
	lead := len(q) - len(strings.TrimLeft(q, " "))
	if lead < 16 {
		t.Errorf("query line not centred (%d leading spaces): %q", lead, q)
	}
	if !strings.Contains(lines[2], "gpt") {
		t.Errorf("options do not start below the query: %q", lines[2])
	}
	if last := lines[len(lines)-1]; strings.Contains(last, "enter select") {
		t.Errorf("hint repeated under the options: %q", last)
	}
	// A tighter room stops the list earlier — that is the mechanism
	// letting options disappear behind the prompt field.
	if short := strings.Split(o.view(40, 80, 3), "\n"); len(short) != 5 {
		t.Errorf("optRoom=3 rendered %d rows, want 5", len(short))
	}
}

// TestOverlayPanelAnchorsQuery pins the box so the query row lands on
// the frame's middle row, and clips a too-tall list at the bottom edge
// rather than letting solid() trim the frame from the top (which would
// lose the title).
func TestOverlayPanelAnchorsQuery(t *testing.T) {
	const height, width = 20, 60
	items := make([]string, 15)
	for i := range items {
		items[i] = fmt.Sprintf("prov/zebra-%02d", i)
	}
	o := newOverlay("model", "switch model", items, theme.Default)
	o.query = "zebra"

	dialog := o.view(height-4, width-6, height) // oversized room on purpose
	body := strings.TrimRight(strings.Repeat("x\n", height-2), "\n")
	out := overlayPanel(strings.Split(body, "\n"), dialog,
		height, width, theme.Default, true)

	if len(out) > height {
		t.Fatalf("frame grew to %d rows, want <= %d", len(out), height)
	}
	if len(out) != height {
		t.Fatalf("frame has %d rows, want the bottom edge reached (%d)", len(out), height)
	}
	found := -1
	for i, l := range out {
		if strings.Contains(l, "zebra") {
			found = i
			break // first hit is the query row, the options follow it
		}
	}
	if found != height/2 {
		t.Fatalf("query row = %d, want %d (middle of the frame)", found, height/2)
	}
	if !strings.Contains(strings.Join(out, "\n"), "switch model") {
		t.Fatal("title lost when the box was clipped")
	}

	// A short box stays anchored to the middle too, not re-centred.
	o2 := newOverlay("model", "switch model", []string{"solo-item"}, theme.Default)
	o2.query = "sol"
	out2 := overlayPanel(strings.Split(body, "\n"), o2.view(height-4, width-6, 1),
		height, width, theme.Default, true)
	found2 := -1
	for i, l := range out2 {
		if strings.Contains(l, "sol") && !strings.Contains(l, "solo-item") {
			found2 = i
		}
	}
	if found2 != height/2 {
		t.Fatalf("short box query row = %d, want %d", found2, height/2)
	}
}
