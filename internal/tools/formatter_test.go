package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// formatterFake drops an executable `name` into dir that appends its
// arguments to logPath, one per line.
func formatterFake(t *testing.T, dir, name, logPath string, fail bool) {
	t.Helper()
	body := "#!/bin/sh\n"
	if fail {
		body += "exit 1\n"
	} else {
		body += "printf '%s\\n' \"$@\" >> '" + logPath + "'\n"
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestFormatTableShape: every registered extension maps to candidate argv
// lists that name a binary and carry the $FILE placeholder.
func TestFormatTableShape(t *testing.T) {
	if got := formatters[".go"]; len(got) != 1 || strings.Join(got[0], " ") != "gofmt -w $FILE" {
		t.Errorf(".go = %v", got)
	}
	if got := formatters[".py"]; len(got) != 2 || got[0][0] != "ruff" || got[1][0] != "black" {
		t.Errorf(".py = %v, want ruff then black", got)
	}
	if _, ok := formatters[".unknownext"]; ok {
		t.Error("unregistered extensions must have no entry")
	}
	for ext, cands := range formatters {
		if len(cands) == 0 {
			t.Errorf("%s has no candidates", ext)
		}
		for _, argv := range cands {
			if argv[0] == "" {
				t.Errorf("%s: empty binary name", ext)
			}
			placed := false
			for _, a := range argv {
				if strings.Contains(a, "$FILE") {
					placed = true
				}
			}
			if !placed {
				t.Errorf("%s: %v never references $FILE", ext, argv)
			}
		}
	}
}

// TestFormatGatingAndFileArg: off by default; on, the extension picks the
// binary and $FILE becomes the absolute target path.
func TestFormatGatingAndFileArg(t *testing.T) {
	wd := t.TempDir()
	bin := t.TempDir()
	log := filepath.Join(t.TempDir(), "gofmt.log")
	formatterFake(t, bin, "gofmt", log, false)
	t.Setenv("PATH", bin)

	target := filepath.Join(wd, "x.go")
	if err := os.WriteFile(target, []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	format(wd, false, target)
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatalf("disabled formatting must not run a binary (log: %v)", err)
	}

	format(wd, true, filepath.Join(wd, "x.unknownext"))
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatal("an unregistered extension must not run a binary")
	}

	format(wd, true, target)
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "-w\n"+target+"\n" {
		t.Errorf("argv = %q, want -w and the absolute path", b)
	}
}

// TestFormatMissingBinaryIsNoop: no candidate on PATH means nothing runs
// and nothing fails.
func TestFormatMissingBinaryIsNoop(t *testing.T) {
	wd := t.TempDir()
	log := filepath.Join(t.TempDir(), "log")
	t.Setenv("PATH", t.TempDir()) // empty: no gofmt, no prettier, nothing

	target := filepath.Join(wd, "x.go")
	if err := os.WriteFile(target, []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	format(wd, true, target)
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatal("nothing may run when no formatter is installed")
	}
}

// TestFormatCandidateFallback: candidates run in order — a missing or
// failing binary falls through to the next one, a success stops the scan.
func TestFormatCandidateFallback(t *testing.T) {
	cases := []struct {
		name     string
		ruff     bool
		fail     bool
		wantRuff bool // the ruff binary produced the output
	}{
		{name: "ruff missing"},
		{name: "ruff fails", ruff: true, fail: true},
		{name: "ruff wins", ruff: true, wantRuff: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wd := t.TempDir()
			bin := t.TempDir()
			ruffLog := filepath.Join(t.TempDir(), "ruff.log")
			blackLog := filepath.Join(t.TempDir(), "black.log")
			if tc.ruff {
				formatterFake(t, bin, "ruff", ruffLog, tc.fail)
			}
			formatterFake(t, bin, "black", blackLog, false)
			t.Setenv("PATH", bin)

			target := filepath.Join(wd, "x.py")
			if err := os.WriteFile(target, []byte("x = 1\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			format(wd, true, target)

			which, log := "black", blackLog
			want := target + "\n"
			if tc.wantRuff {
				which, log = "ruff", ruffLog
				want = "format\n" + target + "\n"
			}
			b, err := os.ReadFile(log)
			if err != nil {
				t.Fatalf("%s never ran: %v", which, err)
			}
			if string(b) != want {
				t.Errorf("%s argv = %q, want %q", which, b, want)
			}
			if tc.wantRuff {
				if _, err := os.Stat(blackLog); !os.IsNotExist(err) {
					t.Error("a successful first candidate must stop the scan")
				}
			} else if _, err := os.Stat(ruffLog); !os.IsNotExist(err) {
				t.Errorf("ruff log exists although black produced the output (err=%v)", err)
			}
		})
	}
}
