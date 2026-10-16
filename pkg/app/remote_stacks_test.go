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

func TestPendingClosesRoundTrip(t *testing.T) {
	repo := git.Repo{CommonDir: t.TempDir()}
	if err := addPendingClose(repo, []pendingClose{{Number: 12, Branch: "c", Comment: "Folded into #11 (b)", Tip: "abc"}}); err != nil {
		t.Fatal(err)
	}
	if err := addPendingClose(repo, []pendingClose{{Number: 13, Branch: "d"}, {Number: 12, Branch: "c"}}); err != nil {
		t.Fatal(err)
	}
	p, err := loadPendingRemote(repo)
	if err != nil || p == nil || len(p.Close) != 2 || p.Close[0].Comment != "Folded into #11 (b)" || p.Close[0].Tip != "abc" {
		t.Fatalf("closes = %+v, %v", p, err)
	}
	// A stack to dissolve keeps the file when the closes are gone, and the
	// file goes once both lists are empty.
	if err := addPendingRemote(repo, []remoteStack{{ID: "S1", Number: 7, Stack: "a"}}); err != nil {
		t.Fatal(err)
	}
	if err := dropPendingClose(repo, []int{12, 13}); err != nil {
		t.Fatal(err)
	}
	if p, err := loadPendingRemote(repo); err != nil || p == nil || len(p.Close) != 0 || len(p.Dissolve) != 1 {
		t.Fatalf("after dropping the closes = %+v, %v", p, err)
	}
	if err := dropPendingRemote(repo, []string{"S1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(remoteStacksPath(repo)); !os.IsNotExist(err) {
		t.Errorf("the file should be gone: %v", err)
	}
}
