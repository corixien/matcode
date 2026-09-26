// Package index maintains index.db: a bbolt sidecar of every session's
// title and message text so search does not have to re-read JSONL files
// (spec §5 line 284). It is REBUILDABLE — never the truth: `mtc index
// rebuild` regenerates it from the session folders, and deleting the
// file loses nothing but search speed.
package index

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"go.etcd.io/bbolt"
)

// buckets: "meta" id → session record, "post" token → JSON []id.
var (
	bktMeta = []byte("meta")
	bktPost = []byte("post")
)

// maxText bounds how much of one session's transcript is indexed, so a
// 100k-line session cannot bloat index.db without bound.
const maxText = 200 << 10 // 200 KiB

// maxTokens bounds the posting list of one token (anti-spam guard).
const maxTokens = 5000

// Hit is one search result.
type Hit struct {
	ID      string `json:"id"`
	Title   string `json:"title,omitempty"`
	Updated int64  `json:"updated"`
	Score   int    `json:"score"`
}

// Path is index.db inside a data tree.
func Path(dataDir string) string { return filepath.Join(dataDir, "index.db") }

// record is the indexed summary of one session.
type record struct {
	ID      string `json:"id"`
	Title   string `json:"title,omitempty"`
	Model   string `json:"model,omitempty"`
	Agent   string `json:"agent,omitempty"`
	Updated int64  `json:"updated"`
	Text    string `json:"text,omitempty"` // lowercased transcript excerpt
}

// Rebuild regenerates index.db from the session folders under dataDir.
// The new db is written to a temp file and renamed into place, so a
// rebuild never leaves a half-written index behind. Returns indexed count.
func Rebuild(dataDir string) (int, error) {
	sessions := filepath.Join(dataDir, "sessions")
	entries, err := os.ReadDir(sessions)
	if err != nil && !os.IsNotExist(err) {
		return 0, err
	}
	recs := make([]record, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if r, err := readSession(filepath.Join(sessions, e.Name())); err == nil {
			recs = append(recs, r)
		}
	}
	sort.Slice(recs, func(i, j int) bool { return recs[i].ID < recs[j].ID })

	db := Path(dataDir)
	tmp := db + ".tmp"
	_ = os.Remove(tmp)
	bdb, err := bbolt.Open(tmp, 0o644, &bbolt.Options{Timeout: 2 * time.Second})
	if err != nil {
		return 0, err
	}
	err = bdb.Update(func(tx *bbolt.Tx) error {
		meta, err := tx.CreateBucketIfNotExists(bktMeta)
		if err != nil {
			return err
		}
		post, err := tx.CreateBucketIfNotExists(bktPost)
		if err != nil {
			return err
		}
		for _, r := range recs {
			raw, err := json.Marshal(r)
			if err != nil {
				return err
			}
			if err := meta.Put([]byte(r.ID), raw); err != nil {
				return err
			}
			for tok := range tokens(r.Title + "\n" + r.Text) {
				list := append(postings(post.Get([]byte(tok))), r.ID)
				if len(list) > maxTokens {
					list = list[:maxTokens]
				}
				raw, err := json.Marshal(list)
				if err != nil {
					return err
				}
				if err := post.Put([]byte(tok), raw); err != nil {
					return err
				}
			}
		}
		return nil
	})
	closeErr := bdb.Close()
	if err != nil {
		_ = os.Remove(tmp)
		return 0, err
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return 0, closeErr
	}
	if err := os.Rename(tmp, db); err != nil {
		_ = os.Remove(tmp)
		return 0, err
	}
	return len(recs), nil
}

// Search queries the index. A missing index.db is an error naming the
// rebuild command — never a silent fallback to scanning JSONL, or the
// index would drift from being "the fast path" to being optional.
func Search(dataDir, query string, limit int) ([]Hit, error) {
	if limit <= 0 {
		limit = 20
	}
	q := strings.TrimSpace(strings.ToLower(query))
	if q == "" {
		return nil, nil
	}
	bdb, err := bbolt.Open(Path(dataDir), 0o644, &bbolt.Options{ReadOnly: true, Timeout: time.Second})
	if err != nil {
		return nil, err
	}
	defer bdb.Close()

	hits := map[string]int{}
	err = bdb.View(func(tx *bbolt.Tx) error {
		meta := tx.Bucket(bktMeta)
		post := tx.Bucket(bktPost)
		if meta == nil || post == nil {
			return nil
		}
		toks := tokens(q)
		if len(toks) == 0 {
			// Not tokenizable (e.g. a path): substring over titles.
			return meta.ForEach(func(k, v []byte) error {
				var r record
				if json.Unmarshal(v, &r) == nil &&
					strings.Contains(strings.ToLower(r.Title), q) {
					hits[r.ID]++
				}
				return nil
			})
		}
		for tok := range toks {
			for _, id := range postings(post.Get([]byte(tok))) {
				hits[id]++
			}
		}
		// Title substring is a strong signal the token index misses
		// ("fix login bug" vs a session titled "login page fix").
		return meta.ForEach(func(k, v []byte) error {
			var r record
			if json.Unmarshal(v, &r) == nil && strings.Contains(strings.ToLower(r.Title), q) {
				hits[r.ID] += 2
			}
			return nil
		})
	})
	if err != nil {
		return nil, err
	}

	out := make([]Hit, 0, len(hits))
	err = bdb.View(func(tx *bbolt.Tx) error {
		meta := tx.Bucket(bktMeta)
		if meta == nil {
			return nil
		}
		for id, score := range hits {
			var r record
			if raw := meta.Get([]byte(id)); raw != nil {
				_ = json.Unmarshal(raw, &r)
			}
			out = append(out, Hit{ID: id, Title: r.Title, Updated: r.Updated, Score: score})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].Updated > out[j].Updated
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// readSession loads one session folder's meta and transcript excerpt.
// A folder without session.json is skipped (not an error): rebuild must
// survive stray directories.
func readSession(dir string) (record, error) {
	b, err := os.ReadFile(filepath.Join(dir, "session.json"))
	if err != nil {
		return record{}, err
	}
	var r struct {
		ID      string `json:"id"`
		Title   string `json:"title"`
		Model   string `json:"model"`
		Agent   string `json:"agent"`
		Updated int64  `json:"updated"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return record{}, err
	}
	if r.ID == "" {
		r.ID = filepath.Base(dir)
	}
	rec := record{ID: r.ID, Title: r.Title, Model: r.Model, Agent: r.Agent, Updated: r.Updated}
	if f, err := os.Open(filepath.Join(dir, "messages.jsonl")); err == nil {
		defer f.Close()
		buf := make([]byte, maxText)
		n, _ := f.Read(buf)
		rec.Text = strings.ToLower(string(buf[:n]))
	}
	return rec, nil
}

// tokens splits text into lowercased index terms (alnum runs ≥ 2 chars).
func tokens(s string) map[string]struct{} {
	out := map[string]struct{}{}
	var b strings.Builder
	flush := func() {
		if b.Len() >= 2 {
			out[strings.ToLower(b.String())] = struct{}{}
		}
		b.Reset()
	}
	for _, r := range s {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		default:
			flush()
		}
	}
	flush()
	return out
}

// postings decodes a posting list; corrupt/missing values yield nil.
func postings(raw []byte) []string {
	var out []string
	if raw != nil {
		_ = json.Unmarshal(raw, &out)
	}
	return out
}
