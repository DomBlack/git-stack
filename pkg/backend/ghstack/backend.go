package ghstack

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/DomBlack/git-stack/pkg/exec"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/stack"
)

// Backend implements the stack ports via gh-stack.
type Backend struct {
	out io.Writer // live output for submit/sync, see WithOutput
	run exec.Runner
	git *git.Client
}

// Option configures a Backend.
type Option func(*Backend)

// WithOutput relays the output of long running gh stack commands (submit,
// sync) to w as it is produced instead of only returning it afterwards.
// The CLI sets this to stderr; the MCP server never does.
func WithOutput(w io.Writer) Option {
	return func(b *Backend) { b.out = w }
}

// New returns a Backend that runs gh through r.
func New(r exec.Runner, g *git.Client, opts ...Option) *Backend {
	b := &Backend{run: r, git: g}
	for _, o := range opts {
		o(b)
	}
	return b
}

// Compile-time port checks.
var _ stack.Metadata = (*Backend)(nil)

// LockName is the file gh stack flocks while it edits its metadata.
const LockName = "gh-stack.lock"

// lockWait is how long Update waits for the metadata lock.
var lockWait = 5 * time.Second

// source is one metadata file: the git dir holding it and the working tree
// it belongs to.
type source struct{ gitDir, worktree string }

// sources lists the current worktree's git dir first, then the main
// checkout's, then every linked worktree's.
func sources(repo git.Repo) []source {
	out := []source{{repo.GitDir, repo.TopLevel}}
	if repo.CommonDir != repo.GitDir {
		out = append(out, source{repo.CommonDir, filepath.Dir(repo.CommonDir)})
	}
	if entries, err := os.ReadDir(filepath.Join(repo.CommonDir, "worktrees")); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			gitDir := filepath.Join(repo.CommonDir, "worktrees", e.Name())
			if gitDir == repo.GitDir {
				continue
			}
			out = append(out, source{gitDir, worktreePath(gitDir)})
		}
	}
	return out
}

// loaded is one metadata file as read.
type loaded struct {
	src  source
	data []byte
	f    *file
}

// recRef locates a stack record: which loaded file and which index in it.
type recRef struct{ file, rec int }

// reading is every metadata file plus the merged graph and, for each graph
// stack, where its record lives.
type reading struct {
	files []loaded
	graph *stack.Graph
	where []recRef
}

func (b *Backend) read(repo git.Repo) (*reading, error) {
	r := &reading{}
	var stacks []stack.Stack
	seen := map[string]bool{}
	for _, src := range sources(repo) {
		data, err := os.ReadFile(filepath.Join(src.gitDir, FileName))
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, err
		}
		f, err := parseFile(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.Join(src.gitDir, FileName), err)
		}
		fi := len(r.files)
		r.files = append(r.files, loaded{src: src, data: data, f: f})
	next:
		for ri, s := range f.toGraph().Stacks {
			for _, br := range s.Branches {
				if seen[br.Name] {
					continue next
				}
			}
			for _, br := range s.Branches {
				seen[br.Name] = true
			}
			s.Worktree = src.worktree
			stacks = append(stacks, s)
			r.where = append(r.where, recRef{fi, ri})
		}
	}
	r.graph = stack.NewGraph(stacks)
	return r, nil
}

// Load implements stack.Metadata. gh stack keeps its file per worktree
// (<git-dir>/gh-stack), so this reads the current worktree's file first, then
// the main checkout's and every linked worktree's, and merges them so one view
// covers everything checked out on the machine. A missing file means no
// stacks; it never runs gh. A branch that appears in two files keeps its first
// stack.
func (b *Backend) Load(_ context.Context, repo git.Repo) (*stack.Graph, error) {
	r, err := b.read(repo)
	if err != nil {
		return nil, err
	}
	return r.graph, nil
}

// Update implements stack.Metadata. It takes gh stack's lock in every git
// dir that has a metadata file, re-reads them, runs fn on the merged graph
// and writes each file back, editing the JSON in place so members we don't
// model, PR records and ordering survive. A file whose stacks fn didn't
// change is not rewritten.
func (b *Backend) Update(ctx context.Context, repo git.Repo, fn func(*stack.Graph) error) error {
	srcs := sources(repo)
	slices.SortFunc(srcs, func(a, b source) int { return strings.Compare(a.gitDir, b.gitDir) })
	var unlocks []func()
	defer func() {
		for _, u := range unlocks {
			u()
		}
	}()
	for _, src := range srcs {
		if _, err := os.Stat(filepath.Join(src.gitDir, FileName)); err != nil {
			continue
		}
		unlock, err := lock(ctx, filepath.Join(src.gitDir, LockName), lockWait)
		if err != nil {
			return err
		}
		unlocks = append(unlocks, unlock)
	}
	r, err := b.read(repo)
	if err != nil {
		return err
	}
	count := len(r.graph.Stacks)
	if err := fn(r.graph); err != nil {
		return err
	}
	if len(r.graph.Stacks) != count {
		return fmt.Errorf("%s: Update may not add or remove stacks", FileName)
	}
	for fi := range r.files {
		if err := b.writeBack(r, fi); err != nil {
			return err
		}
	}
	return nil
}

// writeBack applies the graph's edits to the fi'th file and rewrites it
// when anything changed.
func (b *Backend) writeBack(r *reading, fi int) error {
	root, err := parseTree(r.files[fi].data)
	if err != nil {
		return err
	}
	stacksNode := root.get("stacks")
	if stacksNode == nil || stacksNode.kind != '[' {
		return nil
	}
	changed := false
	var keep []*node
	for ri, rec := range stacksNode.items {
		gi := slices.Index(r.where, recRef{fi, ri})
		if gi < 0 {
			keep = append(keep, rec) // a duplicate Load skipped; leave it be
			continue
		}
		gs := r.graph.Stacks[gi]
		if len(gs.Branches) == 0 {
			changed = true
			continue
		}
		branchesNode := rec.get("branches")
		if branchesNode == nil || branchesNode.kind != '[' {
			keep = append(keep, rec)
			continue
		}
		byName := map[string]*node{}
		for _, item := range branchesNode.items {
			byName[item.str("branch")] = item
		}
		var kept []*node
		for _, gb := range gs.Branches {
			n := byName[gb.Name]
			if n == nil {
				n = objectNode(member{"branch", stringNode(gb.Name)})
				changed = true
			}
			if gb.Head != "" && n.str("head") != gb.Head {
				n.set("head", stringNode(gb.Head))
				changed = true
			}
			if gb.Base != "" && n.str("base") != gb.Base {
				n.set("base", stringNode(gb.Base))
				changed = true
			}
			kept = append(kept, n)
		}
		if len(kept) != len(branchesNode.items) {
			changed = true
		}
		branchesNode.items = kept
		keep = append(keep, rec)
	}
	if !changed {
		return nil
	}
	stacksNode.items = keep
	out, err := root.encode()
	if err != nil {
		return err
	}
	path := filepath.Join(r.files[fi].src.gitDir, FileName)
	tmp, err := os.CreateTemp(filepath.Dir(path), FileName+".*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(out); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if fi, err := os.Stat(path); err == nil {
		if err := os.Chmod(tmp.Name(), fi.Mode().Perm()); err != nil {
			_ = os.Remove(tmp.Name())
			return err
		}
	}
	return os.Rename(tmp.Name(), path)
}

// worktreePath resolves a linked worktree's working tree from its git dir:
// <common>/worktrees/<name>/gitdir holds the path of the worktree's .git file.
func worktreePath(gitDir string) string {
	data, err := os.ReadFile(filepath.Join(gitDir, "gitdir"))
	if err != nil {
		return ""
	}
	return filepath.Dir(strings.TrimSpace(string(data)))
}

// ghEnv keeps gh-stack non-interactive and its output plain when captured.
var ghEnv = []string{
	"GH_NO_UPDATE_NOTIFIER=1",
	"GH_PROMPT_DISABLED=1",
	"NO_COLOR=1",
	"CLICOLOR=0",
}

// ghUnset removes variables that would re-enable prompts or TUIs.
var ghUnset = []string{"GH_FORCE_TTY", "CLICOLOR_FORCE"}

// gh runs `gh stack <args>` captured, mapping failures to *stack.Error.
// The result carries gh-stack's output for commands that relay it.
// gh runs `gh stack <args>` captured and maps failures to stack.Error.
func (b *Backend) gh(ctx context.Context, repo git.Repo, args ...string) error {
	_, err := b.ghRun(ctx, repo, nil, args...)
	return err
}

// ghRun is gh with the captured stderr also relayed live to stream when it
// is non-nil (the long submit and sync commands pass the WithOutput writer).
func (b *Backend) ghRun(ctx context.Context, repo git.Repo, stream io.Writer, args ...string) (exec.Result, error) {
	res, err := b.run.Run(ctx, exec.Cmd{
		Name:   "gh",
		Args:   append([]string{"stack"}, args...),
		Dir:    repo.TopLevel,
		Env:    ghEnv,
		Unset:  ghUnset,
		Stream: stream,
	})
	if err != nil {
		return res, mapError(args, err)
	}
	return res, nil
}
