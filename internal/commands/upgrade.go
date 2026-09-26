package commands

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Upgrade implements `mtc upgrade`: rebuild this checkout and reinstall the
// running binary in place. The source tree is $MATCODE_SRC, else
// ~/.matcode/src. The prefix defaults to the directory of the running
// binary (so an upgrade lands where you installed), falling back to
// install.sh's own default; $PREFIX overrides. The old binary is replaced
// atomically by install.sh (mktemp + mv), so upgrading a running `mtc` is
// safe on Unix.
func Upgrade(args []string) error {
	for _, a := range args {
		switch a {
		case "-h", "--help":
			fmt.Println(`usage: mtc upgrade

Rebuild matcode from source and reinstall the running binary in place.
Source tree: $MATCODE_SRC, else ~/.matcode/src.
Prefix:      directory of this binary; $PREFIX overrides.
Run ./install.sh --uninstall to remove.`)
			return nil
		default:
			return fmt.Errorf("usage: mtc upgrade [-h]")
		}
	}

	src := os.Getenv("MATCODE_SRC")
	if src == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		src = filepath.Join(home, ".matcode", "src")
	}
	src, err := filepath.Abs(src)
	if err != nil {
		return err
	}
	script := filepath.Join(src, "install.sh")
	if st, err := os.Stat(script); err != nil || st.IsDir() {
		return fmt.Errorf("matcode source not found at %s (set MATCODE_SRC to your checkout; it must contain install.sh)", src)
	}

	prefix := os.Getenv("PREFIX")
	if prefix == "" {
		if exe, err := os.Executable(); err == nil {
			base := filepath.Base(exe)
			if base == "mtc" || base == "matcode" {
				if dir := filepath.Dir(exe); writableDir(dir) {
					prefix = dir
				}
			}
		}
	}

	fmt.Printf("source  %s\n", src)
	if prefix != "" {
		fmt.Printf("prefix  %s\n", prefix)
	} else {
		fmt.Printf("prefix  (install.sh default)\n")
	}
	before := versionOf(exePath())

	cmd := exec.Command("sh", script, "--prefix="+prefix)
	if prefix == "" {
		cmd = exec.Command("sh", script)
	}
	cmd.Dir = src
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("upgrade failed: %w", err)
	}
	target := prefix
	if target == "" {
		target = filepath.Dir(exePath())
	}
	after := versionOf(filepath.Join(target, "mtc"))
	if before != "" || after != "" {
		fmt.Printf("upgraded %s → %s\n", orNone(before), orNone(after))
	}
	return nil
}

func orNone(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

func exePath() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return exe
}

// versionOf runs `mtc version` and returns the trimmed output ("" on any
// failure — a missing or non-runnable binary is not an upgrade error).
func versionOf(bin string) string {
	if bin == "" {
		return ""
	}
	out, err := exec.Command(bin, "version").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func writableDir(dir string) bool {
	if dir == "" {
		return false
	}
	st, err := os.Stat(dir)
	if err != nil || !st.IsDir() {
		return false
	}
	f, err := os.CreateTemp(dir, ".mtc-write-probe")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return true
}
