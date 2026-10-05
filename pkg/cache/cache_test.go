package cache

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DomBlack/git-stack/pkg/git"
)

type doc struct {
	Names []string `json:"names"`
}

func TestAt(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "user-cache")
	s := At(dir)
	if s.Dir() != dir {
		t.Errorf("Dir = %q, want %q", s.Dir(), dir)
	}
	if err := Write(s, "latest", doc{Names: []string{"v1"}}); err != nil {
		t.Fatal(err)
	}
	if got, st, _, err := Read[doc](s, "latest", time.Minute); err != nil || st != Fresh || len(got.Names) != 1 {
		t.Errorf("read back: %+v %v %v", got, st, err)
	}
}

func TestReadWriteTTL(t *testing.T) {
	common := t.TempDir()
	s := New(git.Repo{CommonDir: common})
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }

	if _, st, _, err := Read[doc](s, "prs", time.Minute); st != Missing || err != nil {
		t.Fatalf("empty store: %v %v", st, err)
	}
	if err := Write(s, "prs", doc{Names: []string{"a"}}); err != nil {
		t.Fatal(err)
	}
	if s.Dir() != filepath.Join(common, "git-stack") {
		t.Errorf("Dir = %q", s.Dir())
	}

	got, st, saved, err := Read[doc](s, "prs", time.Minute)
	if err != nil || st != Fresh || !saved.Equal(now) || len(got.Names) != 1 || got.Names[0] != "a" {
		t.Fatalf("fresh read: %+v %v %v %v", got, st, saved, err)
	}

	now = now.Add(2 * time.Minute)
	got, st, _, _ = Read[doc](s, "prs", time.Minute)
	if st != Stale || len(got.Names) != 1 {
		t.Fatalf("stale read should still return data: %+v %v", got, st)
	}

	if err := os.WriteFile(s.Path("prs"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, st, _, err := Read[doc](s, "prs", time.Minute); st != Missing || err != nil {
		t.Fatalf("corrupt cache: %v %v", st, err)
	}

	if err := s.Remove("prs"); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove("prs"); err != nil {
		t.Errorf("removing twice: %v", err)
	}
	if entries, _ := os.ReadDir(s.Dir()); len(entries) != 0 {
		t.Errorf("temp files left behind: %v", entries)
	}
}
