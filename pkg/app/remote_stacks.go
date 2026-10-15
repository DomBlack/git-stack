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

// pendingRemote lists the stacks on GitHub that a reshape made stale: their
// order or members changed and the stacks API can only create or append,
// so the next submit dissolves them before gh stack creates them again.
type pendingRemote struct {
	Dissolve []remoteStack `json:"dissolve"`
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
	if p == nil || len(p.Dissolve) == 0 {
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
