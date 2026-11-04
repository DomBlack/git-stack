package ghstack

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
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

// LockName is the file gh stack flocks while it edits its catalog.
const LockName = "gh-stack.lock"

// OperationLockName is the file gh stack flocks for the whole of a command
// that changes stacks. We take it only while moving per-worktree files into
// the shared catalog, which gh stack does under it too.
const OperationLockName = "gh-stack-operation.lock"

// migrationJournal is the file gh stack writes while it moves per-worktree
// files into the shared catalog itself, and removes once it is done. While
// it exists the catalog must be left alone for gh stack to finish.
const migrationJournal = "gh-stack-migration"

// BackupSuffix names the copy of a per-worktree file kept when it is moved
// into the shared catalog, the same name gh stack uses.
const BackupSuffix = ".pre-worktree-migration"

// recoveryFiles are gh stack's records of an interrupted rebase or modify.
// One left in a worktree's git dir needs that worktree's own file, so its
// file is not moved while one is there.
var recoveryFiles = []string{"gh-stack-rebase-state", "gh-stack-modify-state"}

// lockWait is how long Update waits for the metadata lock.
var lockWait = 5 * time.Second

// catalogDir is where gh stack keeps its catalog: the common git dir, so
// every worktree of the repository shares one file.
func catalogDir(repo git.Repo) string {
	if repo.CommonDir != "" {
		return repo.CommonDir
	}
	return repo.GitDir
}

// catalogPath is the shared catalog file.
func catalogPath(repo git.Repo) string { return filepath.Join(catalogDir(repo), FileName) }

// LegacyFiles lists the per-worktree files gh stack 0.2 and older kept in
// each linked worktree's git dir (and git-stack wrote there too), the
// current worktree's first. gh stack 0.3 moved to one shared catalog; these
// are merged into it by the next Update.
func LegacyFiles(repo git.Repo) []string {
	var out []string
	common := catalogDir(repo)
	if repo.GitDir != "" && repo.GitDir != common {
		if _, err := os.Stat(filepath.Join(repo.GitDir, FileName)); err == nil {
			out = append(out, filepath.Join(repo.GitDir, FileName))
		}
	}
	entries, err := os.ReadDir(filepath.Join(common, "worktrees"))
	if err != nil {
		return out
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		path := filepath.Join(common, "worktrees", e.Name(), FileName)
		if slices.Contains(out, path) {
			continue
		}
		if fi, err := os.Stat(path); err == nil && fi.Mode().IsRegular() {
			out = append(out, path)
		}
	}
	return out
}

// MigrationPending reports whether gh stack itself is part way through
// moving per-worktree files into the shared catalog.
func MigrationPending(repo git.Repo) bool {
	_, err := os.Lstat(filepath.Join(catalogDir(repo), migrationJournal))
	return err == nil
}

// catalog is the shared catalog with any per-worktree files merged in, as
// one tree whose stack records match graph's stacks one for one.
type catalog struct {
	root  *node
	graph *stack.Graph
	// legacy are the per-worktree files merged in.
	legacy []string
}

// readCatalog reads the shared catalog, then each per-worktree file, and
// merges them: a stack record joins when none of its branches is in a
// record already taken, so the shared catalog wins over the per-worktree
// files and the current worktree's file over the others. A record left
// out is not lost; the file it was in is kept as a backup when it is moved.
// The members of the shared catalog's root are kept, with the repository
// taken from a per-worktree file when the shared one has none.
func readCatalog(repo git.Repo) (*catalog, error) {
	c := &catalog{}
	paths := append([]string{catalogPath(repo)}, LegacyFiles(repo)...)
	var recs []*node
	seen := map[string]bool{}
	for i, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, err
		}
		if _, err := parseFile(data); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		root, err := parseTree(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if i == 0 {
			c.root = root
		} else {
			c.legacy = append(c.legacy, path)
			if c.root == nil {
				c.root = emptyRoot()
			}
			if c.root.get("repository") == nil && root.get("repository") != nil {
				c.root.set("repository", root.get("repository"))
			}
		}
		stacksNode := root.get("stacks")
		if stacksNode == nil || stacksNode.kind != '[' {
			continue
		}
	next:
		for _, rec := range stacksNode.items {
			var names []string
			if bn := rec.get("branches"); bn != nil && bn.kind == '[' {
				for _, item := range bn.items {
					names = append(names, item.str("branch"))
				}
			}
			for _, n := range names {
				if seen[n] {
					continue next
				}
			}
			for _, n := range names {
				seen[n] = true
			}
			recs = append(recs, rec)
		}
	}
	if c.root == nil {
		c.root = emptyRoot()
	}
	c.root.set("stacks", arrayNode(recs...))
	if err := c.regraph(); err != nil {
		return nil, err
	}
	return c, nil
}

// regraph rebuilds graph from root.
func (c *catalog) regraph() error {
	data, err := c.root.encode()
	if err != nil {
		return err
	}
	f, err := parseFile(data)
	if err != nil {
		return err
	}
	c.graph = f.toGraph()
	return nil
}

func emptyRoot() *node {
	return objectNode(member{"schemaVersion", intNode(SchemaVersion)}, member{"stacks", arrayNode()})
}

// Load implements stack.Metadata. It reads gh stack's shared catalog
// (<common-dir>/gh-stack) and merges in any per-worktree file left from gh
// stack 0.2 (see readCatalog), so stacks show up the same before and after
// they are moved. A missing file means no stacks. It never writes and never
// runs gh.
func (b *Backend) Load(_ context.Context, repo git.Repo) (*stack.Graph, error) {
	c, err := readCatalog(repo)
	if err != nil {
		return nil, err
	}
	return c.graph, nil
}

// Update implements stack.Metadata. It takes gh stack's lock, re-reads the
// catalog, runs fn on the graph and writes the catalog back, editing the
// JSON in place so members we don't model, PR records and ordering
// survive. Nothing is written when nothing changed. fn may also append
// stacks (each with a trunk and at least one branch) and may set or clear
// a stack's ID and Number (clearing removes them from the record). It must
// not remove or reorder existing stacks; a stack left with no branches is
// removed.
//
// Any per-worktree file left from gh stack 0.2 is merged into the catalog
// and then moved aside (see Migrate), under gh stack's operation lock too.
func (b *Backend) Update(ctx context.Context, repo git.Repo, fn func(*stack.Graph) error) error {
	unlock, err := lockCatalog(ctx, repo)
	if err != nil {
		return err
	}
	defer unlock()
	c, err := readCatalog(repo)
	if err != nil {
		return err
	}
	if err := checkMovable(repo, c.legacy); err != nil {
		return err
	}
	before, err := c.root.encode()
	if err != nil {
		return err
	}
	count := len(c.graph.Stacks)
	if err := fn(c.graph); err != nil {
		return err
	}
	if len(c.graph.Stacks) < count {
		return fmt.Errorf("%s: Update may not remove stacks", FileName)
	}
	added := c.graph.Stacks[count:]
	for _, s := range added {
		if s.Trunk == "" || len(s.Branches) == 0 {
			return fmt.Errorf("%s: a new stack needs a trunk and at least one branch", FileName)
		}
	}
	c.apply(added)
	after, err := c.root.encode()
	if err != nil {
		return err
	}
	if !bytes.Equal(before, after) || len(c.legacy) > 0 {
		if _, err := os.Stat(catalogPath(repo)); err == nil || len(c.graph.Stacks) > 0 || len(c.legacy) > 0 {
			if err := writeBytes(catalogPath(repo), after); err != nil {
				return err
			}
		}
	}
	return retire(c.legacy)
}

// Migrate merges any per-worktree file left from gh stack 0.2 into the
// shared catalog and moves each aside to <file>.pre-worktree-migration
// (numbered when gh stack already left a backup by that name, so a stale
// one never blocks it), and reports the files it moved. gh stack 0.3 does
// the same on its next command but refuses when a backup it made earlier
// differs from the file, which git-stack's own writes to these files used
// to cause. It is a no-op when there is nothing to move.
func (b *Backend) Migrate(ctx context.Context, repo git.Repo) ([]string, error) {
	legacy := LegacyFiles(repo)
	if len(legacy) == 0 {
		return nil, nil
	}
	if err := b.Update(ctx, repo, func(*stack.Graph) error { return nil }); err != nil {
		return nil, err
	}
	return legacy, nil
}

// lockCatalog takes gh stack's catalog lock, and before it the operation
// lock when per-worktree files are waiting to be moved, the order gh stack
// takes them in.
func lockCatalog(ctx context.Context, repo git.Repo) (func(), error) {
	dir := catalogDir(repo)
	var unlocks []func()
	release := func() {
		for _, u := range slices.Backward(unlocks) {
			u()
		}
	}
	names := []string{LockName}
	if len(LegacyFiles(repo)) > 0 {
		names = []string{OperationLockName, LockName}
	}
	for _, name := range names {
		unlock, err := lock(ctx, filepath.Join(dir, name), lockWait)
		if err != nil {
			release()
			return nil, err
		}
		unlocks = append(unlocks, unlock)
	}
	return release, nil
}

// checkMovable refuses to move per-worktree files while gh stack is part
// way through moving them itself, or while one of their worktrees has an
// interrupted gh stack rebase or modify that still needs its own file.
func checkMovable(repo git.Repo, legacy []string) error {
	if MigrationPending(repo) {
		return stack.New(stack.KindStateMigration, "gh stack is part way through moving its stacks into one shared file").
			WithSteps("run `gh stack view` to let it finish, then try again",
				"or run `git stack doctor` to see what is in the way")
	}
	for _, path := range legacy {
		dir := filepath.Dir(path)
		for _, name := range recoveryFiles {
			if _, err := os.Lstat(filepath.Join(dir, name)); err == nil {
				return stack.Newf(stack.KindStateMigration, "a gh stack rebase or modify was interrupted in the worktree of %s, so its stacks can't be moved into the shared file yet", dir).
					WithSteps("in that worktree, run `gh stack rebase --continue` (or `--abort`), or `gh stack modify --continue` (or `--abort`)")
			}
		}
	}
	return nil
}

// retire moves each per-worktree file aside, once the shared catalog holds
// its stacks: to <file>.pre-worktree-migration, or the first free
// <file>.pre-worktree-migration.<n> when that name is taken by a backup that
// differs.
func retire(legacy []string) error {
	for _, path := range legacy {
		data, err := os.ReadFile(path)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return err
		}
		backup := path + BackupSuffix
		for n := 1; ; n++ {
			old, err := os.ReadFile(backup)
			if errors.Is(err, fs.ErrNotExist) {
				break
			}
			if err != nil {
				return err
			}
			if bytes.Equal(old, data) {
				break
			}
			backup = fmt.Sprintf("%s%s.%d", path, BackupSuffix, n)
		}
		if err := os.Rename(path, backup); err != nil {
			return err
		}
	}
	return nil
}

// apply writes the graph's edits into root: each record takes its stack's
// id, number, trunk and branches (each branch from its node wherever it was
// before, so its pull request record and members we don't model travel with
// it), a stack left with no branches goes, and added stacks are appended.
func (c *catalog) apply(added []stack.Stack) {
	stacksNode := c.root.get("stacks")
	pool := map[string]*node{}
	for _, rec := range stacksNode.items {
		if bn := rec.get("branches"); bn != nil && bn.kind == '[' {
			for _, item := range bn.items {
				pool[item.str("branch")] = item
			}
		}
	}
	var keep []*node
	for ri, rec := range stacksNode.items {
		gs := c.graph.Stacks[ri]
		if len(gs.Branches) == 0 {
			continue
		}
		// A zero id or number is removed rather than written as "" or 0,
		// so a record never carries an empty value.
		switch {
		case gs.ID == "" && rec.get("id") != nil:
			rec.del("id")
		case gs.ID != "" && rec.str("id") != gs.ID:
			rec.set("id", stringNode(gs.ID))
		}
		switch {
		case gs.Number == 0 && rec.get("number") != nil:
			rec.del("number")
		case gs.Number != 0 && rec.num("number") != gs.Number:
			rec.set("number", intNode(gs.Number))
		}
		if gs.Trunk != "" {
			if tn := rec.get("trunk"); tn == nil || tn.kind != '{' {
				rec.set("trunk", objectNode(member{"branch", stringNode(gs.Trunk)}))
			} else if tn.str("branch") != gs.Trunk {
				tn.set("branch", stringNode(gs.Trunk)) // keeps the recorded head
			}
		}
		rec.set("branches", arrayNode(branchNodes(gs.Branches, pool)...))
		keep = append(keep, rec)
	}
	for _, s := range added {
		keep = append(keep, stackNode(s, pool))
	}
	stacksNode.items = keep
}

// writeBytes replaces path with out atomically, keeping the mode of the
// file it replaces.
func writeBytes(path string, out []byte) error {
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

// branchNodes renders a stack's branches, reusing each branch's node from
// pool so its pull request record and unmodelled members travel with it
// (under its old name for a renamed branch).
func branchNodes(branches []stack.Branch, pool map[string]*node) []*node {
	out := make([]*node, 0, len(branches))
	for _, gb := range branches {
		n := pool[gb.Name]
		if n == nil && gb.RenamedFrom != "" {
			if n = pool[gb.RenamedFrom]; n != nil {
				n.set("branch", stringNode(gb.Name))
			}
		}
		if n == nil {
			n = objectNode(member{"branch", stringNode(gb.Name)})
		}
		if gb.Head != "" && n.str("head") != gb.Head {
			n.set("head", stringNode(gb.Head))
		}
		if gb.Base != "" && n.str("base") != gb.Base {
			n.set("base", stringNode(gb.Base))
		}
		out = append(out, n)
	}
	return out
}

// stackNode is a new stack record: its id and number when it has them (a
// stack put back by an abort), its trunk and its branches, each from its
// existing node when it had one. gh stack fills in the id and number of a
// new stack when it first submits it.
func stackNode(s stack.Stack, pool map[string]*node) *node {
	rec := objectNode()
	if s.ID != "" {
		rec.set("id", stringNode(s.ID))
	}
	if s.Number != 0 {
		rec.set("number", intNode(s.Number))
	}
	rec.set("trunk", objectNode(member{"branch", stringNode(s.Trunk)}))
	rec.set("branches", arrayNode(branchNodes(s.Branches, pool)...))
	return rec
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

// gh runs `gh stack <args>` captured and maps failures to stack.Error.
func (b *Backend) gh(ctx context.Context, repo git.Repo, args ...string) error {
	_, err := b.ghRun(ctx, repo, nil, args...)
	return err
}

// ghRun is gh with the captured stderr also relayed live to stream when it
// is non-nil (the long submit and sync commands pass the WithOutput writer).
//
// gh stack 0.3 moves per-worktree files into its shared catalog itself, but
// stops for good when a backup it made earlier differs from the file. So
// git-stack moves them first (see Migrate), unless gh stack is part way
// through doing it, which it is left to finish.
func (b *Backend) ghRun(ctx context.Context, repo git.Repo, stream io.Writer, args ...string) (exec.Result, error) {
	if !MigrationPending(repo) {
		if _, err := b.Migrate(ctx, repo); err != nil {
			return exec.Result{}, err
		}
	}
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

// Snapshot implements stack.Metadata. It moves any per-worktree file into
// the shared catalog first (see Migrate), so a snapshot is always the one
// file, then reads it under the lock, recording a missing file as empty so
// Restore can remove it again.
func (b *Backend) Snapshot(ctx context.Context, repo git.Repo) (stack.Snapshot, error) {
	if !MigrationPending(repo) {
		if _, err := b.Migrate(ctx, repo); err != nil {
			return stack.Snapshot{}, err
		}
	}
	unlock, err := lock(ctx, filepath.Join(catalogDir(repo), LockName), lockWait)
	if err != nil {
		return stack.Snapshot{}, err
	}
	defer unlock()
	path := catalogPath(repo)
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return stack.Snapshot{}, err
	}
	return stack.Snapshot{Files: map[string][]byte{path: data}}, nil
}

// Restore implements stack.Metadata. It writes each file in the snapshot
// back exactly, under the lock, and removes the ones that were empty (did
// not exist) when it was taken. A snapshot taken by an older git-stack may
// name per-worktree files too; they are put back as they were and moved
// into the shared catalog again by the next command. A file whose git dir
// has gone since (a removed worktree) is skipped.
func (b *Backend) Restore(ctx context.Context, repo git.Repo, s stack.Snapshot) error {
	unlock, err := lock(ctx, filepath.Join(catalogDir(repo), LockName), lockWait)
	if err != nil {
		return err
	}
	defer unlock()
	for _, path := range slices.Sorted(maps.Keys(s.Files)) {
		if _, err := os.Stat(filepath.Dir(path)); err != nil {
			continue
		}
		data := s.Files[path]
		if len(data) == 0 {
			if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			continue
		}
		if cur, err := os.ReadFile(path); err == nil && bytes.Equal(cur, data) {
			continue
		}
		if err := writeBytes(path, data); err != nil {
			return err
		}
	}
	return nil
}
