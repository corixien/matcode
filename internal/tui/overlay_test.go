package tui

import (
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
