package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// loadDotEnv reads one KEY=value file into the process environment.
// An already-set variable always wins — the shell outranks the file —
// so a persisted key (SetKey) never shadows an explicit export.
// Supported line shapes: KEY=value, export KEY=value, quoted values,
// blank lines, and `#` comments.
func loadDotEnv(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		i := strings.IndexByte(line, '=')
		if i <= 0 {
			continue
		}
		k := strings.TrimSpace(line[:i])
		v := strings.TrimSpace(line[i+1:])
		if len(v) >= 2 &&
			((v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'')) {
			v = v[1 : len(v)-1]
		}
		if k == "" || v == "" {
			continue
		}
		if os.Getenv(k) != "" {
			continue // shell wins
		}
		if err := os.Setenv(k, v); err != nil {
			return err
		}
	}
	return sc.Err()
}

// loadDotEnvDirs loads <dir>/.env for each dir in order (global, then
// project, so a project key wins over the global one for a fresh shell).
func loadDotEnvDirs(dirs ...string) error {
	for _, d := range dirs {
		if d == "" {
			continue
		}
		if err := loadDotEnv(filepath.Join(d, ".env")); err != nil {
			return fmt.Errorf("%s: %w", filepath.Join(d, ".env"), err)
		}
	}
	return nil
}

// SetKey stores envName=value in <dir>/.env (mode 0600, replacing any
// existing assignment for that name) and exports it for this process.
// It backs the TUI's /key: keys live in one file per data tree, never
// in config.toml.
func SetKey(dir, envName, value string) error {
	if envName == "" {
		return fmt.Errorf("provider declares no api_key env var")
	}
	if value == "" {
		return fmt.Errorf("empty key")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(dir, ".env")
	var lines []string
	if b, err := os.ReadFile(path); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			t := strings.TrimSpace(l)
			if t == "" || strings.HasPrefix(t, "#") {
				lines = append(lines, l)
				continue
			}
			s := strings.TrimPrefix(t, "export ")
			if i := strings.IndexByte(s, '='); i > 0 && strings.TrimSpace(s[:i]) == envName {
				continue // replaced below
			}
			lines = append(lines, l)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	lines = append(lines, envName+"="+value)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return os.Setenv(envName, value)
}
