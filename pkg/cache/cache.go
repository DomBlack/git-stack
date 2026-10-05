// Package cache stores small JSON documents, per repository under
// <git-common-dir>/git-stack/ or in a directory of the caller's choosing, so
// that commands (and completion) can render instantly from the last known
// state.
package cache

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/DomBlack/git-stack/pkg/git"
)

// State describes the freshness of a cached document.
type State int

const (
	// Missing: no document exists (or it could not be decoded).
	Missing State = iota
	// Stale: the document exists but is older than the TTL.
	Stale
	// Fresh: the document is within the TTL.
	Fresh
)

func (s State) String() string {
	switch s {
	case Missing:
		return "missing"
	case Stale:
		return "stale"
	case Fresh:
		return "fresh"
	default:
		return fmt.Sprintf("State(%d)", int(s))
	}
}

// Store is the cache directory of one repository.
type Store struct {
	dir string
	now func() time.Time
}

// New returns the store for repo. Nothing is created until Write.
func New(repo git.Repo) *Store {
	return At(filepath.Join(repo.CommonDir, "git-stack"))
}

// At returns a store rooted at dir, for documents that belong to the user
// rather than to one repository. Nothing is created until Write.
func At(dir string) *Store {
	return &Store{dir: dir, now: time.Now}
}

// Dir returns the cache directory.
func (s *Store) Dir() string { return s.dir }

// Path returns the file backing name.
func (s *Store) Path(name string) string { return filepath.Join(s.dir, name+".json") }

type envelope[T any] struct {
	SavedAt time.Time `json:"savedAt"`
	Data    T         `json:"data"`
}

// Read loads name. A missing or undecodable file yields the zero value and
// Missing; a decodable one yields Stale or Fresh depending on ttl. The
// returned time is when the document was saved.
func Read[T any](s *Store, name string, ttl time.Duration) (T, State, time.Time, error) {
	var env envelope[T]
	b, err := os.ReadFile(s.Path(name))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return env.Data, Missing, time.Time{}, nil
		}
		return env.Data, Missing, time.Time{}, err
	}
	if err := json.Unmarshal(b, &env); err != nil {
		// A corrupt cache is treated as absent; the next Write repairs it.
		var zero T
		return zero, Missing, time.Time{}, nil
	}
	if s.now().Sub(env.SavedAt) > ttl {
		return env.Data, Stale, env.SavedAt, nil
	}
	return env.Data, Fresh, env.SavedAt, nil
}

// Write stores v under name atomically (temp file + rename).
func Write[T any](s *Store, name string, v T) error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(envelope[T]{SavedAt: s.now(), Data: v})
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.dir, name+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, s.Path(name))
}

// Remove deletes name; a missing document is not an error.
func (s *Store) Remove(name string) error {
	err := os.Remove(s.Path(name))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}
