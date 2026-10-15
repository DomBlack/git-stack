package app

import (
	"os"
	"testing"

	"github.com/DomBlack/git-stack/pkg/git"
)

func TestPendingRemoteStacksRoundTrip(t *testing.T) {
	repo := git.Repo{CommonDir: t.TempDir()}
	if p, err := loadPendingRemote(repo); err != nil || p != nil {
		t.Fatalf("empty = %+v, %v", p, err)
	}
	if err := addPendingRemote(repo, []remoteStack{{ID: "S1", Number: 7, Stack: "a"}}); err != nil {
		t.Fatal(err)
	}
	if err := addPendingRemote(repo, []remoteStack{{ID: "S2", Number: 8, Stack: "x"}, {ID: "S1", Number: 7, Stack: "a"}}); err != nil {
		t.Fatal(err)
	}
	p, err := loadPendingRemote(repo)
	if err != nil || len(p.Dissolve) != 2 || p.Dissolve[0].Number != 7 || p.Dissolve[1].Number != 8 {
		t.Fatalf("after adds = %+v, %v", p, err)
	}
	if err := dropPendingRemote(repo, []string{"S1"}); err != nil {
		t.Fatal(err)
	}
	p, _ = loadPendingRemote(repo)
	if len(p.Dissolve) != 1 || p.Dissolve[0].ID != "S2" {
		t.Errorf("after drop = %+v", p)
	}
	if err := dropPendingRemote(repo, []string{"S2"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(remoteStacksPath(repo)); err == nil {
		t.Error("an empty list should remove the file")
	}
}
