package app

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/DomBlack/git-stack/pkg/git"
)

// remoteStack identifies a stack GitHub knows, as recorded by gh stack.
type remoteStack struct {
	ID     string `json:"id"`
	Number int    `json:"number"`
	// Stack is the bottom branch, for messages.
	Stack string `json:"stack"`
}

// pendingClose is a pull request a pop or fold made redundant: the next
// submit closes it with Comment, then deletes its remote branch.
type pendingClose struct {
	Number  int    `json:"number"`
	Branch  string `json:"branch"`
	Comment string `json:"comment"`
	// Tip is the branch's tip when it was queued, the lease for deleting
	// the remote branch; empty deletes it without one.
	Tip string `json:"tip,omitempty"`
	// Closed is set once the pull request is closed and only the delete is
	// left.
	Closed bool `json:"closed,omitempty"`
}

// pendingRemote is what a reshape left for the next submit to do on
// GitHub. Dissolve lists stacks made stale: their order or members changed
// and the stacks API can only create or append, so they are dissolved
// before gh stack creates them again. Close lists pull requests whose
// branch a pop or fold took away.
type pendingRemote struct {
	Dissolve []remoteStack  `json:"dissolve"`
	Close    []pendingClose `json:"close,omitempty"`
}

// remoteStacksPath is <common git dir>/git-stack/remote-stacks.json; it is
// repository wide, unlike the per worktree restack state.
func remoteStacksPath(repo git.Repo) string {
	return filepath.Join(repo.CommonDir, "git-stack", "remote-stacks.json")
}

// loadPendingRemote reads the list, or returns nil when there is none.
func loadPendingRemote(repo git.Repo) (*pendingRemote, error) {
	data, err := os.ReadFile(remoteStacksPath(repo))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var p pendingRemote
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("%s: %w", remoteStacksPath(repo), err)
	}
	return &p, nil
}

// savePendingRemote writes the list atomically, or removes the file when
// nothing is left.
func savePendingRemote(repo git.Repo, p *pendingRemote) error {
	path := remoteStacksPath(repo)
	if p == nil || (len(p.Dissolve) == 0 && len(p.Close) == 0) {
		err := os.Remove(path)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// addPendingRemote records stacks to dissolve, once each by id.
func addPendingRemote(repo git.Repo, stacks []remoteStack) error {
	p, err := loadPendingRemote(repo)
	if err != nil {
		return err
	}
	if p == nil {
		p = &pendingRemote{}
	}
	for _, s := range stacks {
		if !slices.ContainsFunc(p.Dissolve, func(r remoteStack) bool { return r.ID == s.ID }) {
			p.Dissolve = append(p.Dissolve, s)
		}
	}
	return savePendingRemote(repo, p)
}

// dropPendingRemote forgets the stacks with the given ids.
func dropPendingRemote(repo git.Repo, ids []string) error {
	p, err := loadPendingRemote(repo)
	if err != nil || p == nil {
		return err
	}
	p.Dissolve = slices.DeleteFunc(p.Dissolve, func(r remoteStack) bool { return slices.Contains(ids, r.ID) })
	return savePendingRemote(repo, p)
}

// addPendingClose records pull requests to close, once each by number.
func addPendingClose(repo git.Repo, closes []pendingClose) error {
	p, err := loadPendingRemote(repo)
	if err != nil {
		return err
	}
	if p == nil {
		p = &pendingRemote{}
	}
	for _, c := range closes {
		if !slices.ContainsFunc(p.Close, func(q pendingClose) bool { return q.Number == c.Number }) {
			p.Close = append(p.Close, c)
		}
	}
	return savePendingRemote(repo, p)
}

// dropPendingClose forgets the pull requests with the given numbers.
func dropPendingClose(repo git.Repo, numbers []int) error {
	p, err := loadPendingRemote(repo)
	if err != nil || p == nil {
		return err
	}
	p.Close = slices.DeleteFunc(p.Close, func(c pendingClose) bool { return slices.Contains(numbers, c.Number) })
	return savePendingRemote(repo, p)
}
