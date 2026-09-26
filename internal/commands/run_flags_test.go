package commands

import (
	"context"
	"strings"
	"testing"
)

// Flag validation runs before config, providers, or the store are touched,
// so these paths must fail fast with a precise message.
func TestRunFlagValidation(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"bad format", []string{"-format", "xml", "p"}, "unknown -format"},
		{"both resume modes", []string{"-continue", "-session", "ses_x", "p"}, "mutually exclusive"},
		{"empty prompt", nil, "usage: mtc run"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Run(context.Background(), tc.args)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not contain %q", err, tc.want)
			}
		})
	}
}

// jsonOut emits one object per line, and text events carry the delta as-is.
func TestJSONOutLines(t *testing.T) {
	var b strings.Builder
	j := newJSONOut(&b)
	j.text("hello")
	j.event(map[string]string{"type": "done"})
	lines := strings.Split(strings.TrimSpace(b.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 NDJSON lines, got %d: %q", len(lines), b.String())
	}
	if lines[0] != `{"text":"hello","type":"text"}` {
		t.Fatalf("text line = %s", lines[0])
	}
	if lines[1] != `{"type":"done"}` {
		t.Fatalf("done line = %s", lines[1])
	}
}
