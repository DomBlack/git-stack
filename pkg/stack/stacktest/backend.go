// Package stacktest provides an in-memory stack backend for tests. Branch
// creation and checkout happen in the real temporary repository; the graph
// lives in memory.
package stacktest

import (
	"context"
	"slices"
	"sync"

	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/stack"
)

// Backend implements every stack port in memory.
type Backend struct {
	mu    sync.Mutex
	git   *git.Client
	graph *stack.Graph

	// Restacks records the scopes requested. RestackErr, when set, is
	// returned by Restack.
	Restacks   []stack.Scope
	RestackErr error
	Continued  int
	Aborted    int
	// Submits records submit options; SubmitFn, when set, runs on submit.
	Submits  []stack.SubmitOptions
	SubmitFn func(o stack.SubmitOptions) error
}

// New returns a Backend over g with the given initial graph.
func New(g *git.Client, graph *stack.Graph) *Backend {
	if graph == nil {
		graph = stack.NewGraph(nil)
	}
	return &Backend{git: g, graph: graph}
}

// Graph returns the current graph.
func (b *Backend) Graph() *stack.Graph {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.graph
}

// SetGraph replaces the graph.
func (b *Backend) SetGraph(g *stack.Graph) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.graph = g
}

func (b *Backend) Load(context.Context, git.Repo) (*stack.Graph, error) {
	return b.Graph(), nil
}

func (b *Backend) Update(_ context.Context, _ git.Repo, fn func(*stack.Graph) error) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := fn(b.graph); err != nil {
		return err
	}
	b.graph = stack.NewGraph(slices.DeleteFunc(slices.Clone(b.graph.Stacks), func(s stack.Stack) bool { return len(s.Branches) == 0 }))
	return nil
}

func (b *Backend) InitStack(ctx context.Context, repo git.Repo, trunk string, branches []string) error {
	s := stack.Stack{Trunk: trunk}
	prev := trunk
	for _, name := range branches {
		exists, err := b.git.BranchExists(ctx, repo, name)
		if err != nil {
			return err
		}
		if !exists {
			if err := b.git.CreateBranch(ctx, repo, name, prev); err != nil {
				return err
			}
		}
		s.Branches = append(s.Branches, stack.Branch{Name: name})
		prev = name
	}
	b.mu.Lock()
	b.graph = stack.NewGraph(append(slices.Clone(b.graph.Stacks), s))
	b.mu.Unlock()
	return b.git.Switch(ctx, repo, prev)
}

func (b *Backend) AddTop(ctx context.Context, repo git.Repo, name string) error {
	cur, err := b.git.CurrentBranch(ctx, repo)
	if err != nil {
		return err
	}
	b.mu.Lock()
	s, i, ok := b.graph.StackOf(cur)
	if !ok || i != len(s.Branches)-1 {
		b.mu.Unlock()
		return stack.New(stack.KindNotAtTop, "gh stack can only add branches at the top of the stack")
	}
	s.Branches = append(s.Branches, stack.Branch{Name: name})
	b.mu.Unlock()
	if err := b.git.CreateBranch(ctx, repo, name, cur); err != nil {
		return err
	}
	return b.git.Switch(ctx, repo, name)
}

func (b *Backend) Restack(_ context.Context, _ git.Repo, scope stack.Scope) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.Restacks = append(b.Restacks, scope)
	return b.RestackErr
}

func (b *Backend) Continue(context.Context, git.Repo) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.Continued++
	return nil
}

func (b *Backend) Abort(context.Context, git.Repo) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.Aborted++
	return nil
}

func (b *Backend) Submit(_ context.Context, _ git.Repo, o stack.SubmitOptions) (stack.SubmitResult, error) {
	b.mu.Lock()
	b.Submits = append(b.Submits, o)
	fn := b.SubmitFn
	b.mu.Unlock()
	if fn != nil {
		if err := fn(o); err != nil {
			return stack.SubmitResult{}, err
		}
	}
	return stack.SubmitResult{Output: "submitted"}, nil
}

// Compile-time checks.
var (
	_ stack.Metadata  = (*Backend)(nil)
	_ stack.Tracker   = (*Backend)(nil)
	_ stack.Restacker = (*Backend)(nil)
	_ stack.Submitter = (*Backend)(nil)
)
