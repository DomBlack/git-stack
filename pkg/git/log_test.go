package git_test

import (
	"context"
	"slices"
	"testing"

	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/git/gittest"
)

func TestCommits(t *testing.T) {
	c, repo, dir := objectsFixture(t)
	ctx := context.Background()
	gittest.Run(t, dir, "switch", "-q", "-c", "a")
	a1 := gittest.Commit(t, dir, "a1.txt", "1", "First on a")
	odd := "Odd \x1b[31mred\x1b[0m\ttab \"quoted\" \\ #12 ünïcode 漢字"
	a2 := gittest.Commit(t, dir, "a2.txt", "2", odd)
	gittest.Run(t, dir, "switch", "-q", "-c", "b")
	b1 := gittest.Commit(t, dir, "b1.txt", "1", "Only on b")
	gittest.Run(t, dir, "commit", "-q", "--allow-empty", "--allow-empty-message", "-m", "")
	empty := gittest.Run(t, dir, "rev-parse", "HEAD")

	tests := []struct {
		name    string
		tip     string
		exclude []string
		want    []git.Commit
	}{
		{"bottom branch against trunk", "refs/heads/a", []string{"refs/heads/main"},
			[]git.Commit{{SHA: a2, Subject: odd}, {SHA: a1, Subject: "First on a"}}},
		{"branch against its parent, empty subject last", "refs/heads/b", []string{"refs/heads/a"},
			[]git.Commit{{SHA: empty, Subject: ""}, {SHA: b1, Subject: "Only on b"}}},
		{"also excluding an older base", "refs/heads/b", []string{"refs/heads/a", a1},
			[]git.Commit{{SHA: empty, Subject: ""}, {SHA: b1, Subject: "Only on b"}}},
		{"nothing of its own", "refs/heads/a", []string{"refs/heads/b"}, nil},
		{"missing exclusions are ignored", "refs/heads/a", []string{"refs/heads/main", "refs/heads/gone", "0123456789012345678901234567890123456789"},
			[]git.Commit{{SHA: a2, Subject: odd}, {SHA: a1, Subject: "First on a"}}},
		{"missing tip lists nothing", "refs/heads/gone", []string{"refs/heads/main"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := c.Commits(ctx, repo, tt.tip, tt.exclude...)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("Commits = %q\nwant %q", got, tt.want)
			}
		})
	}
}
