package commands

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/tabwriter"
	"time"

	"matcode/internal/config"
	"matcode/internal/store"
)

// sessionConfig loads config for `mtc session …` and returns it with cwd.
func sessionConfig() (*config.Config, string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, "", err
	}
	cfg, err := config.Load(cwd)
	if err != nil {
		return nil, "", err
	}
	return cfg, cwd, nil
}

// Session implements `mtc session list|delete|export|import`: the folders
// under SessionsDir are the truth, so every verb is a thin file operation.
func Session(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: mtc session list|delete|export|import …")
	}
	var err error
	switch args[0] {
	case "list":
		err = sessionList(args[1:])
	case "delete":
		err = sessionDelete(args[1:])
	case "export":
		err = sessionExport(args[1:])
	case "import":
		err = sessionImport(args[1:])
	default:
		err = fmt.Errorf("unknown session command %q (want list, delete, export, import)", args[0])
	}
	return err
}

// splitFlags separates dash-flags (plus the values they consume) from
// positional arguments, so `session export <id> -o file` and
// `session export -o file <id>` both parse. Booleans are detected by their
// declared default value.
func splitFlags(fs *flag.FlagSet, args []string) (flags, positional []string) {
	bools := map[string]bool{}
	fs.VisitAll(func(f *flag.Flag) {
		if f.DefValue == "true" || f.DefValue == "false" {
			bools[f.Name] = true
		}
	})
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") || a == "-" {
			positional = append(positional, a)
			continue
		}
		flags = append(flags, a)
		name := strings.TrimLeft(a, "-")
		if eq := strings.Index(name, "="); eq >= 0 {
			continue
		}
		if !bools[name] && i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	return flags, positional
}

// parse reorders args so flags may follow positionals, then parses them.
func parse(fs *flag.FlagSet, args []string) error {
	flags, positional := splitFlags(fs, args)
	return fs.Parse(append(flags, positional...))
}

// --- list ---------------------------------------------------------------

func sessionList(args []string) error {
	fs := flag.NewFlagSet("session list", flag.ContinueOnError)
	n := fs.Int("n", 0, "show at most N sessions (0 = all)")
	format := fs.String("format", "table", "table or json")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *format != "table" && *format != "json" {
		return fmt.Errorf("unknown -format %q (want table or json)", *format)
	}
	cfg, _, err := sessionConfig()
	if err != nil {
		return err
	}
	metas, err := store.List(cfg.SessionsDir())
	if err != nil {
		return err
	}
	if *n > 0 && len(metas) > *n {
		metas = metas[:*n]
	}
	type row struct {
		ID     string  `json:"id"`
		Title  string  `json:"title,omitempty"`
		Agent  string  `json:"agent,omitempty"`
		Model  string  `json:"model"`
		Msgs   int     `json:"msgs"`
		Tokens int64   `json:"tokens"`
		Cost   float64 `json:"cost,omitempty"`
		Age    string  `json:"updated"`
	}
	rows := make([]row, 0, len(metas))
	for _, m := range metas {
		rows = append(rows, row{
			ID: m.ID, Title: m.Title, Agent: m.Agent, Model: m.Model,
			Msgs:   countMessages(filepath.Join(cfg.SessionsDir(), m.ID)),
			Tokens: m.TokensIn + m.TokensOut,
			Cost:   m.Cost,
			Age:    time.UnixMilli(m.Updated).Format("2006-01-02 15:04"),
		})
	}
	if *format == "json" {
		b, err := json.MarshalIndent(rows, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(b))
		return nil
	}
	if len(rows) == 0 {
		fmt.Println("no sessions")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tUPDATED\tAGENT\tMODEL\tMSGS\tTOKENS\tCOST\tTITLE")
	for _, r := range rows {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%d\t$%.4f\t%s\n",
			r.ID, r.Age, r.Agent, r.Model, r.Msgs, r.Tokens, r.Cost, r.Title)
	}
	return w.Flush()
}

// countMessages counts transcript lines without parsing them.
func countMessages(dir string) int {
	b, err := os.ReadFile(filepath.Join(dir, "messages.jsonl"))
	if err != nil || len(b) == 0 {
		return 0
	}
	n := 0
	for _, c := range b {
		if c == '\n' {
			n++
		}
	}
	if b[len(b)-1] != '\n' {
		n++
	}
	return n
}

// --- delete -------------------------------------------------------------

func sessionDelete(args []string) error {
	fs := flag.NewFlagSet("session delete", flag.ContinueOnError)
	if err := parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: mtc session delete <session-id>")
	}
	cfg, _, err := sessionConfig()
	if err != nil {
		return err
	}
	dir, err := sessionPath(cfg, fs.Arg(0))
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(dir, "session.json")); err != nil {
		return fmt.Errorf("no session %q under %s", fs.Arg(0), cfg.SessionsDir())
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	fmt.Printf("deleted %s\n", fs.Arg(0))
	return nil
}

// sessionPath maps a session id onto its folder, refusing ids that would
// escape the sessions root (separators, "..", absolute paths).
func sessionPath(cfg *config.Config, id string) (string, error) {
	if id == "" || strings.ContainsAny(id, `/\`) || id == "." || id == ".." {
		return "", fmt.Errorf("invalid session id %q", id)
	}
	root := cfg.SessionsDir()
	dir := filepath.Join(root, id)
	rel, err := filepath.Rel(root, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid session id %q", id)
	}
	return dir, nil
}

// --- export / import ----------------------------------------------------

// exportDoc is the `mtc session export` document: one self-contained JSON
// file that `mtc session import` can replay into a fresh session folder.
type exportDoc struct {
	Version  int               `json:"version"`
	Session  store.Meta        `json:"session"`
	Messages []store.Message   `json:"messages"`
	Files    map[string]string `json:"files,omitempty"`
}

// exportFiles are the side files that travel with a session.
var exportFiles = []string{"title.txt", "summary.txt", "checkpoint.md", filepath.Join("compaction", "archive.md")}

func sessionExport(args []string) error {
	fs := flag.NewFlagSet("session export", flag.ContinueOnError)
	out := fs.String("o", "", "write to file instead of stdout")
	sanitize := fs.Bool("sanitize", false, "redact secret-looking values and home paths")
	if err := parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return fmt.Errorf("usage: mtc session export [session-id] [-o file] [-sanitize]")
	}
	cfg, _, err := sessionConfig()
	if err != nil {
		return err
	}
	var s *store.Session
	if fs.NArg() == 1 {
		dir, err := sessionPath(cfg, fs.Arg(0))
		if err != nil {
			return err
		}
		s, err = store.Open(dir)
		if err != nil {
			return fmt.Errorf("no session %q under %s", fs.Arg(0), cfg.SessionsDir())
		}
	} else {
		if s, err = store.Latest(cfg.SessionsDir()); err != nil {
			return err
		}
	}
	msgs, err := s.Messages()
	if err != nil {
		return err
	}
	doc := exportDoc{Version: 1, Session: s.Meta, Messages: msgs, Files: map[string]string{}}
	for _, name := range exportFiles {
		b, err := os.ReadFile(filepath.Join(s.Dir, name))
		if err != nil {
			continue
		}
		doc.Files[name] = string(b)
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	text := string(b) + "\n"
	if *sanitize {
		text = Sanitize(text)
	}
	if *out == "" {
		_, err = io.WriteString(os.Stdout, text)
	} else {
		err = os.WriteFile(*out, []byte(text), 0o644)
		if err == nil {
			fmt.Fprintf(os.Stderr, "wrote %s\n", *out)
		}
	}
	return err
}

func sessionImport(args []string) error {
	fs := flag.NewFlagSet("session import", flag.ContinueOnError)
	if err := parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: mtc session import <file|->")
	}
	var raw []byte
	var err error
	if fs.Arg(0) == "-" {
		raw, err = io.ReadAll(os.Stdin)
	} else {
		raw, err = os.ReadFile(fs.Arg(0))
	}
	if err != nil {
		return err
	}
	var doc exportDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("%s: %w", fs.Arg(0), err)
	}
	if doc.Version != 1 {
		return fmt.Errorf("unsupported export version %d (want 1)", doc.Version)
	}
	cfg, _, err := sessionConfig()
	if err != nil {
		return err
	}
	s, err := store.Create(cfg.SessionsDir(), doc.Session.Model)
	if err != nil {
		return err
	}
	// Side files first: only the names export knows, never a traversal.
	for name, body := range doc.Files {
		if !knownExportFile(name) {
			continue
		}
		path := filepath.Join(s.Dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			return err
		}
	}
	for i := range doc.Messages {
		m := doc.Messages[i]
		if err := s.Append(&m); err != nil {
			return err
		}
	}
	// Original identity and totals survive; only the id is new.
	s.Meta.Created = doc.Session.Created
	s.Meta.Title = doc.Session.Title
	s.Meta.Agent = doc.Session.Agent
	s.Meta.TokensIn = doc.Session.TokensIn
	s.Meta.TokensOut = doc.Session.TokensOut
	s.Meta.Cost = doc.Session.Cost
	if err := s.Save(); err != nil {
		return err
	}
	fmt.Printf("imported %s (from %s)\n", s.Meta.ID, orDash(doc.Session.ID))
	return nil
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func knownExportFile(name string) bool {
	for _, f := range exportFiles {
		if name == f {
			return true
		}
	}
	return false
}

// --- sanitize -----------------------------------------------------------

var (
	rePEM  = regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.+?-----END [A-Z ]*PRIVATE KEY-----`)
	reKV   = regexp.MustCompile(`(?i)("?\b(?:api[_-]?key|apikey|access[_-]?key|secret|token|password|passwd|authorization|auth)\b"?\s*[:=]\s*)"?[^"\s,}]+`)
	reBare = regexp.MustCompile(
		`\bsk-[A-Za-z0-9_-]{16,}` +
			`|\bghp_[A-Za-z0-9]{20,}` +
			`|\bgithub_pat_[A-Za-z0-9_]{20,}` +
			`|\bAKIA[0-9A-Z]{16}` +
			`|\bxox[baprs]-[A-Za-z0-9-]{10,}` +
			`|\bBearer\s+[A-Za-z0-9._~+/=-]{16,}`)
)

// Sanitize redacts secret-looking values and the user's home directory from
// exported transcript text (`mtc session export --sanitize`).
func Sanitize(s string) string {
	home, err := os.UserHomeDir()
	if err == nil && home != "" && home != "/" {
		s = strings.ReplaceAll(s, home, "~")
	}
	s = rePEM.ReplaceAllString(s, "***redacted private key***")
	s = reBare.ReplaceAllString(s, "***redacted***")
	s = reKV.ReplaceAllString(s, `${1}***redacted***`)
	return s
}
