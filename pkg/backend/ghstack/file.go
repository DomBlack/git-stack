// Package ghstack implements the stack ports on top of GitHub's gh-stack
// extension. Stack metadata is read directly from the extension's
// <git-dir>/gh-stack file (documented, schema version 1). The file is written
// only by Update, under gh stack's lock; every other mutation still goes
// through `gh stack` commands.
package ghstack

import (
	"encoding/json/v2"
	"fmt"

	"github.com/DomBlack/git-stack/pkg/stack"
)

// FileName is the gh-stack metadata file inside the git directory.
const FileName = "gh-stack"

// SchemaVersion is the only version this adapter understands.
const SchemaVersion = 1

// file mirrors gh-stack's on-disk structure (see testdata/schema.json).
type file struct {
	SchemaVersion int        `json:"schemaVersion"`
	Repository    string     `json:"repository,omitzero"`
	Stacks        []stackRec `json:"stacks"`
}

type stackRec struct {
	ID       string      `json:"id,omitzero"`
	Number   int         `json:"number,omitzero"`
	Trunk    branchRef   `json:"trunk"`
	Branches []branchRef `json:"branches"`
}

type branchRef struct {
	Branch      string `json:"branch"`
	Head        string `json:"head,omitzero"`
	Base        string `json:"base,omitzero"`
	PullRequest *prRef `json:"pullRequest,omitzero"`
}

type prRef struct {
	Number int    `json:"number"`
	ID     string `json:"id,omitzero"`
	URL    string `json:"url,omitzero"`
	Merged bool   `json:"merged,omitzero"`
}

// parseFile decodes and validates the metadata file.
func parseFile(b []byte) (*file, error) {
	var f file
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", FileName, err)
	}
	if f.SchemaVersion != SchemaVersion {
		return nil, stack.Newf(stack.KindUnsupported,
			"%s has schema version %d; this git-stack understands version %d", FileName, f.SchemaVersion, SchemaVersion).
			WithSteps("upgrade git-stack (go install github.com/DomBlack/git-stack@latest)")
	}
	for i, s := range f.Stacks {
		if s.Trunk.Branch == "" {
			return nil, fmt.Errorf("parse %s: stack %d has no trunk", FileName, i)
		}
		for j, b := range s.Branches {
			if b.Branch == "" {
				return nil, fmt.Errorf("parse %s: stack %d branch %d has no name", FileName, i, j)
			}
		}
	}
	return &f, nil
}

// toGraph converts the file into the domain model.
func (f *file) toGraph() *stack.Graph {
	stacks := make([]stack.Stack, 0, len(f.Stacks))
	for _, s := range f.Stacks {
		st := stack.Stack{ID: s.ID, Number: s.Number, Trunk: s.Trunk.Branch}
		for _, b := range s.Branches {
			br := stack.Branch{Name: b.Branch, Head: b.Head, Base: b.Base}
			if b.PullRequest != nil {
				br.PR = &stack.PRRef{Number: b.PullRequest.Number, URL: b.PullRequest.URL, Merged: b.PullRequest.Merged}
			}
			st.Branches = append(st.Branches, br)
		}
		stacks = append(stacks, st)
	}
	return stack.NewGraph(stacks)
}
