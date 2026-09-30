package stack

import (
	"fmt"
	"slices"
)

// Direction of a navigation request.
type Direction int

const (
	Up Direction = iota
	Down
	Top
	Bottom
)

func (d Direction) String() string {
	switch d {
	case Up:
		return "up"
	case Down:
		return "down"
	case Top:
		return "top"
	case Bottom:
		return "bottom"
	default:
		return fmt.Sprintf("Direction(%d)", int(d))
	}
}

// NavRequest asks to move from a branch along the stack.
type NavRequest struct {
	From string
	Dir  Direction
	// Steps for Up/Down; 0 means 1.
	Steps int
	// To optionally names a branch that must lie in the chosen direction. It
	// selects between several children today only where the graph branches
	// (a trunk with several stacks); stacks themselves are linear.
	To string
}

// NavResult is the computed destination.
type NavResult struct {
	Target string
	// Moved is the number of levels actually traversed.
	Moved int
	// Clamped is true when the request hit the end of the stack.
	Clamped bool
	// Message explains a clamp in Graphite's words.
	Message string
}

// Graphite's clamp messages.
const (
	MsgAlreadyTop    = "Already at the top of the stack."
	MsgAlreadyBottom = "Already at the bottom most branch in the stack."
)

// Navigate computes the destination of req without touching git. Merged
// branches are skipped as destinations (they are no longer real layers)
// unless the request starts on one. Moving down from the bottom branch lands
// on the trunk, as in Graphite.
func Navigate(g *Graph, req NavRequest) (NavResult, error) {
	steps := max(req.Steps, 1)
	if req.Dir == Top || req.Dir == Bottom {
		steps = 0
	}

	if g.IsTrunk(req.From) {
		return navigateFromTrunk(g, req, steps)
	}

	s, i, ok := g.StackOf(req.From)
	if !ok {
		return NavResult{}, Newf(KindNotInStack, "%s is not in a stack", req.From).
			WithSteps("run `git stack checkout` to pick a stacked branch", "or `git stack create` from trunk to start a stack")
	}
	if req.To != "" {
		if s.Index(req.To) < 0 {
			return NavResult{}, Newf(KindInvalidArgs, "%s is not in the same stack as %s", req.To, req.From)
		}
		if req.Dir == Up && s.Index(req.To) <= i {
			return NavResult{}, Newf(KindInvalidArgs, "%s is not upstack of %s", req.To, req.From)
		}
	}

	// Candidate positions: active branches plus the starting branch itself.
	path := make([]int, 0, len(s.Branches))
	for j, b := range s.Branches {
		if j == i || !b.Merged() {
			path = append(path, j)
		}
	}
	pos := slices.Index(path, i)

	switch req.Dir {
	case Up:
		target := min(pos+steps, len(path)-1)
		if target == pos {
			return NavResult{Target: req.From, Clamped: true, Message: MsgAlreadyTop}, nil
		}
		return NavResult{Target: s.Branches[path[target]].Name, Moved: target - pos, Clamped: pos+steps > len(path)-1}, nil
	case Down:
		target := pos - steps
		if target < 0 {
			// Below the bottom branch is the trunk.
			return NavResult{Target: s.Trunk, Moved: pos + 1, Clamped: target < -1}, nil
		}
		return NavResult{Target: s.Branches[path[target]].Name, Moved: steps}, nil
	case Top:
		last := len(path) - 1
		if last == pos {
			return NavResult{Target: req.From, Clamped: true, Message: MsgAlreadyTop}, nil
		}
		return NavResult{Target: s.Branches[path[last]].Name, Moved: last - pos}, nil
	case Bottom:
		if pos == 0 {
			return NavResult{Target: req.From, Clamped: true, Message: "Already at the bottom of the stack."}, nil
		}
		return NavResult{Target: s.Branches[path[0]].Name, Moved: pos}, nil
	default:
		return NavResult{}, Newf(KindInvalidArgs, "unknown direction %v", req.Dir)
	}
}

func navigateFromTrunk(g *Graph, req NavRequest, steps int) (NavResult, error) {
	if req.Dir == Down {
		return NavResult{Target: req.From, Clamped: true, Message: MsgAlreadyBottom}, nil
	}
	s, err := chooseStack(g, req.From, req.To)
	if err != nil {
		return NavResult{}, err
	}
	var active []Branch
	for _, b := range s.Branches {
		if !b.Merged() {
			active = append(active, b)
		}
	}
	if len(active) == 0 {
		return NavResult{Target: req.From, Clamped: true, Message: MsgAlreadyTop}, nil
	}
	switch req.Dir {
	case Up:
		target := min(steps, len(active)) - 1
		return NavResult{Target: active[target].Name, Moved: target + 1, Clamped: steps > len(active)}, nil
	case Top:
		return NavResult{Target: active[len(active)-1].Name, Moved: len(active)}, nil
	default: // Bottom
		return NavResult{Target: active[0].Name, Moved: 1}, nil
	}
}

// chooseStack picks the stack to enter from a trunk. With several stacks the
// caller must name a branch in the wanted one via To.
func chooseStack(g *Graph, trunk, to string) (*Stack, error) {
	stacks := g.StacksOn(trunk)
	if to != "" {
		for _, s := range stacks {
			if s.Index(to) >= 0 {
				return s, nil
			}
		}
		return nil, Newf(KindInvalidArgs, "%s is not in a stack on %s", to, trunk)
	}
	switch len(stacks) {
	case 0:
		return nil, Newf(KindNotInStack, "no stacks on %s", trunk).
			WithSteps("run `git stack create` to start one")
	case 1:
		return stacks[0], nil
	default:
		names := make([]string, 0, len(stacks))
		for _, s := range stacks {
			names = append(names, s.Bottom())
		}
		return nil, Newf(KindDisambiguate, "%d stacks start at %s (%s)", len(stacks), trunk, joinNames(names)).
			WithSteps("pass --to <branch> to choose one", "or use `git stack checkout <branch>`")
	}
}

func joinNames(names []string) string {
	out := ""
	for i, n := range names {
		if i > 0 {
			out += ", "
		}
		out += n
	}
	return out
}
