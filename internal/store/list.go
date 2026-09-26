package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// List reads every session folder under root, most recently updated first.
// A folder without a readable session.json (half-created, hand-edited) is
// skipped rather than failing the whole listing.
func List(root string) ([]Meta, error) {
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Meta
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		s, err := Open(filepath.Join(root, e.Name()))
		if err != nil {
			continue
		}
		out = append(out, s.Meta)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Updated != out[j].Updated {
			return out[i].Updated > out[j].Updated
		}
		return out[i].ID > out[j].ID
	})
	return out, nil
}

// Latest opens the most recently updated session — what `run --continue`
// resumes when no id is given.
func Latest(root string) (*Session, error) {
	metas, err := List(root)
	if err != nil {
		return nil, err
	}
	if len(metas) == 0 {
		return nil, fmt.Errorf("no sessions in %s", root)
	}
	return Open(filepath.Join(root, metas[0].ID))
}
