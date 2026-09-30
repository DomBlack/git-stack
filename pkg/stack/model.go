// Package stack is the domain model: stacks of branches rooted at a trunk,
// pure navigation over them, and the ports a backend must implement.
package stack

import "slices"

// PRRef is the backend's snapshot of a branch's pull request.
type PRRef struct {
	Number int
	URL    string
	Merged bool
}

// Branch is one layer of a stack.
type Branch struct {
	Name string
	// Head is the tip commit id known to the backend (may be empty).
	Head string
	// Base is the parent's tip at the last restack (may be empty).
	Base string
	PR   *PRRef
}

// Merged reports whether the branch's PR has been merged.
func (b Branch) Merged() bool { return b.PR != nil && b.PR.Merged }

// Stack is an ordered, linear list of branches on top of a trunk.
type Stack struct {
	// ID is the backend's remote identifier, if any.
	ID string
	// Number is the backend's repo-scoped stack number, if any.
	Number int
	Trunk  string
	// Branches run bottom (closest to trunk) to top.
	Branches []Branch
}

// Index returns the position of name in the stack, or -1.
func (s *Stack) Index(name string) int {
	return slices.IndexFunc(s.Branches, func(b Branch) bool { return b.Name == name })
}

// Bottom is the branch directly above trunk ("" for an empty stack).
func (s *Stack) Bottom() string {
	if len(s.Branches) == 0 {
		return ""
	}
	return s.Branches[0].Name
}

// Top is the branch furthest from trunk ("" for an empty stack).
func (s *Stack) Top() string {
	if len(s.Branches) == 0 {
		return ""
	}
	return s.Branches[len(s.Branches)-1].Name
}

// Names returns the branch names bottom to top.
func (s *Stack) Names() []string {
	out := make([]string, len(s.Branches))
	for i, b := range s.Branches {
		out[i] = b.Name
	}
	return out
}

// Graph holds every stack in a repository.
type Graph struct {
	Stacks []Stack
	// Trunks are the distinct trunk branches in first-seen order.
	Trunks []string
}

// NewGraph builds a Graph and derives Trunks.
func NewGraph(stacks []Stack) *Graph {
	g := &Graph{Stacks: stacks}
	for _, s := range stacks {
		if !slices.Contains(g.Trunks, s.Trunk) {
			g.Trunks = append(g.Trunks, s.Trunk)
		}
	}
	return g
}
