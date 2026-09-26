// Package store persists sessions as folders of JSONL. A folder is the truth:
// deleting it deletes the session.
package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Meta is the contents of session.json.
type Meta struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Title   string `json:"title,omitempty"`
	Agent   string `json:"agent,omitempty"`
	Created int64  `json:"created"` // unix milliseconds
	Updated int64  `json:"updated"`
	// Token and cost totals across every turn in this session.
	TokensIn  int64   `json:"tokens_in,omitempty"`
	TokensOut int64   `json:"tokens_out,omitempty"`
	Cost      float64 `json:"cost,omitempty"` // USD; 0 until a catalog prices it
}

// Session is one folder under the sessions root.
type Session struct {
	Meta Meta
	Dir  string
}

// Create makes a new session folder under root.
func Create(root string, model string) (*Session, error) {
	now := nowMS()
	s := &Session{
		Dir: filepath.Join(root, id("ses_", now)),
		Meta: Meta{
			Model:   model,
			Created: now,
			Updated: now,
		},
	}
	s.Meta.ID = filepath.Base(s.Dir)
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return nil, err
	}
	if err := s.saveMeta(); err != nil {
		return nil, err
	}
	return s, nil
}

// Open loads an existing session folder.
func Open(dir string) (*Session, error) {
	b, err := os.ReadFile(filepath.Join(dir, "session.json"))
	if err != nil {
		return nil, err
	}
	var m Meta
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", dir, err)
	}
	return &Session{Meta: m, Dir: dir}, nil
}

// Append writes one message as a single JSONL line and refreshes session.json.
// The message is passed by pointer: Append fills in ID and Time when unset,
// so callers can key side data (snapshot entries) to the stored identity.
func (s *Session) Append(m *Message) error {
	now := nowMS()
	if m.ID == "" {
		m.ID = id("msg_", now)
	}
	if m.Time == 0 {
		m.Time = now
	}
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(s.Dir, "messages.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(b, '\n')); err != nil {
		return err
	}
	s.Meta.Updated = m.Time
	return s.saveMeta()
}

// Messages reads the full transcript. A truncated final line (crash mid-write)
// is skipped rather than treated as an error.
func (s *Session) Messages() ([]Message, error) {
	b, err := os.ReadFile(filepath.Join(s.Dir, "messages.jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Message
	for _, line := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var m Message
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			continue
		}
		out = append(out, m)
	}
	return out, nil
}

// AddUsage folds one turn's token accounting into the session totals.
func (s *Session) AddUsage(in, out int, costUSD float64) error {
	s.Meta.TokensIn += int64(in)
	s.Meta.TokensOut += int64(out)
	s.Meta.Cost += costUSD
	return s.saveMeta()
}

// Save persists metadata edits (title, agent) without touching messages.
func (s *Session) Save() error {
	return s.saveMeta()
}

func (s *Session) saveMeta() error {
	b, err := json.MarshalIndent(s.Meta, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.Dir, "session.json"), append(b, '\n'), 0o644)
}

// SetTitle stores the generated title in session.json and title.txt
// (row 40).
func (s *Session) SetTitle(title string) error {
	s.Meta.Title = title
	s.Meta.Updated = nowMS()
	if err := os.WriteFile(filepath.Join(s.Dir, "title.txt"), []byte(title+"\n"), 0o644); err != nil {
		return err
	}
	return s.saveMeta()
}

// SetSummary writes the generated summary to summary.txt (row 40).
func (s *Session) SetSummary(text string) error {
	return os.WriteFile(filepath.Join(s.Dir, "summary.txt"), []byte(text+"\n"), 0o644)
}

func nowMS() int64 { return time.Now().UnixMilli() }

// id builds a lexicographically sortable identifier: prefix, timestamp, random.
func id(prefix string, t int64) string {
	var b [6]byte
	rand.Read(b[:])
	return fmt.Sprintf("%s%013x%s", prefix, t, hex.EncodeToString(b[:]))
}
