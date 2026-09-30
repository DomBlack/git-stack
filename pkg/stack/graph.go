package stack

import "slices"

// IsTrunk reports whether name is the trunk of any stack.
func (g *Graph) IsTrunk(name string) bool {
	return slices.Contains(g.Trunks, name)
}

// StackOf returns the stack containing the (non-trunk) branch and its index.
func (g *Graph) StackOf(name string) (*Stack, int, bool) {
	for i := range g.Stacks {
		if idx := g.Stacks[i].Index(name); idx >= 0 {
			return &g.Stacks[i], idx, true
		}
	}
	return nil, -1, false
}

// StacksOn returns the stacks rooted at trunk.
func (g *Graph) StacksOn(trunk string) []*Stack {
	var out []*Stack
	for i := range g.Stacks {
		if g.Stacks[i].Trunk == trunk {
			out = append(out, &g.Stacks[i])
		}
	}
	return out
}

// Tracked reports whether name is a trunk or a stacked branch.
func (g *Graph) Tracked(name string) bool {
	if g.IsTrunk(name) {
		return true
	}
	_, _, ok := g.StackOf(name)
	return ok
}

// Parent returns the branch below name: the previous branch in its stack, or
// the trunk for the bottom branch. Trunks and untracked branches have none.
func (g *Graph) Parent(name string) (string, bool) {
	s, i, ok := g.StackOf(name)
	if !ok {
		return "", false
	}
	if i == 0 {
		return s.Trunk, true
	}
	return s.Branches[i-1].Name, true
}

// Children returns the branches directly above name. For a stacked branch
// that is at most one branch today (stacks are linear); for a trunk it is the
// bottom branch of every stack rooted there.
func (g *Graph) Children(name string) []string {
	if g.IsTrunk(name) {
		var out []string
		for _, s := range g.StacksOn(name) {
			if b := s.Bottom(); b != "" {
				out = append(out, b)
			}
		}
		return out
	}
	s, i, ok := g.StackOf(name)
	if !ok || i+1 >= len(s.Branches) {
		return nil
	}
	return []string{s.Branches[i+1].Name}
}

// Branches returns every stacked (non-trunk) branch name.
func (g *Graph) Branches() []string {
	var out []string
	for _, s := range g.Stacks {
		out = append(out, s.Names()...)
	}
	return out
}
