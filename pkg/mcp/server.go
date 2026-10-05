// Package mcp exposes the stack use cases as MCP tools over stdio so coding
// agents can drive git-stack. Stdout belongs to JSON-RPC: everything here
// logs to stderr and every subprocess is captured.
package mcp

import (
	"context"
	"encoding/json/v2"
	"errors"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DomBlack/git-stack/pkg/app"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/stack"
)

// Options wires the server.
type Options struct {
	Version string
	// Cwd is the fallback repository location.
	Cwd string
	Git *git.Client
	// NewApp builds the use cases for a repository. It must never produce
	// an App that prompts or hands over a terminal.
	NewApp func(ctx context.Context, repo git.Repo) (*app.App, error)
	Log    *slog.Logger
}

// Server is the MCP server.
type Server struct {
	opts  Options
	mcp   *mcp.Server
	roots sync.Map // *mcp.ServerSession -> *rootEntry
}

// rootEntry holds a session's first root once the async lookup finishes.
type rootEntry struct {
	ready chan struct{}
	once  sync.Once
	path  string
}

func (e *rootEntry) done(path string) {
	e.once.Do(func() {
		e.path = path
		close(e.ready)
	})
}

// entryFor returns the session's root entry, creating it if needed. Either
// the initialised notification or the first tool call may arrive first.
func (s *Server) entryFor(sess *mcp.ServerSession) *rootEntry {
	v, _ := s.roots.LoadOrStore(sess, &rootEntry{ready: make(chan struct{})})
	return v.(*rootEntry)
}

// rootsTimeout bounds the roots/list round trip.
const rootsTimeout = 2 * time.Second

// New builds the server and registers every tool.
func New(o Options) *Server {
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Version == "" {
		o.Version = "dev"
	}
	s := &Server{opts: o}
	s.mcp = mcp.NewServer(
		&mcp.Implementation{Name: "git-stack", Title: "git-stack", Version: o.Version},
		&mcp.ServerOptions{
			Instructions: Instructions,
			Logger:       o.Log,
			// roots/list cannot be sent while a request is being served, so
			// fetch the roots once the session is initialised.
			InitializedHandler: s.onInitialized,
		},
	)
	s.registerTools()
	return s
}

// MCP exposes the underlying server (tests connect it to in-memory transports).
func (s *Server) MCP() *mcp.Server { return s.mcp }

// Run serves over stdio until the client disconnects or ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	return s.mcp.Run(ctx, &mcp.StdioTransport{})
}

// repo resolves the repository for a call: repo_path, then the client's
// first root, then the working directory.
func (s *Server) repo(ctx context.Context, req *mcp.CallToolRequest, repoPath string) (git.Repo, error) {
	dir := repoPath
	if dir == "" {
		dir = s.firstRoot(ctx, req)
	}
	if dir == "" {
		dir = s.opts.Cwd
	}
	repo, err := s.opts.Git.Discover(ctx, dir)
	if err != nil {
		if errors.Is(err, git.ErrNotRepo) {
			return git.Repo{}, stack.Newf(stack.KindNotRepo, "%s is not inside a git repository", dir).
				WithSteps("pass repo_path with an absolute path inside the repository")
		}
		return git.Repo{}, err
	}
	return repo, nil
}

// onInitialized starts the roots lookup for a new session. The protocol has
// retired roots (SEP-2577) but clients still send them during the
// deprecation window; the lookup runs in the background so initialisation is
// never blocked.
func (s *Server) onInitialized(_ context.Context, req *mcp.InitializedRequest) {
	if req == nil || req.Session == nil {
		return
	}
	s.opts.Log.Debug("session initialised; fetching roots")
	entry := s.entryFor(req.Session)
	go func() {
		defer entry.done("")
		ctx, cancel := context.WithTimeout(context.Background(), rootsTimeout)
		defer cancel()
		res, err := req.Session.ListRoots(ctx, nil) //nolint:staticcheck // deprecated upstream, still honoured during the deprecation window
		if err != nil {
			s.opts.Log.Debug("roots/list unavailable", "err", err)
			return
		}
		for _, r := range res.Roots {
			if p := rootPath(r.URI); p != "" {
				entry.done(p)
				return
			}
		}
	}()
}

// protocolWithoutRoots is the first protocol version with no initialize
// handshake and no roots (SEP-2577): nothing to wait for there.
const protocolWithoutRoots = "2026-07-28"

// firstRoot returns the session's first local root directory, or "".
func (s *Server) firstRoot(ctx context.Context, req *mcp.CallToolRequest) string {
	if req == nil || req.Session == nil {
		return ""
	}
	if req.ProtocolVersion() >= protocolWithoutRoots {
		return ""
	}
	entry := s.entryFor(req.Session)
	select {
	case <-entry.ready:
		return entry.path
	case <-ctx.Done():
		return ""
	case <-time.After(rootsTimeout):
		s.opts.Log.Debug("roots/list still pending; falling back to the working directory")
		entry.done("")
		return ""
	}
}

// rootPath converts a file:// root URI to a path.
func rootPath(uri string) string {
	u, err := url.Parse(uri)
	if err != nil || (u.Scheme != "file" && u.Scheme != "") {
		return ""
	}
	if u.Path != "" {
		return u.Path
	}
	return strings.TrimPrefix(uri, "file://")
}

// toolError is the JSON body of every tool error.
type toolError struct {
	Code      string   `json:"code"`
	Message   string   `json:"message"`
	Branch    string   `json:"branch,omitempty"`
	Files     []string `json:"files,omitempty"`
	NextSteps []string `json:"next_steps,omitempty"`
	Detail    string   `json:"detail,omitempty"`
}

// jsonError renders as JSON so agents can parse tool errors.
type jsonError struct {
	body  toolError
	cause error
}

func (e *jsonError) Error() string {
	b, err := json.Marshal(e.body)
	if err != nil {
		return e.body.Message
	}
	return string(b)
}

func (e *jsonError) Unwrap() error { return e.cause }

// cliToTool rewrites CLI next steps into tool calls.
var cliToTool = []struct{ from, to string }{
	{"git stack restack --continue", "call stack_restack with continue: true"},
	{"git stack restack --abort", "call stack_restack with abort: true"},
	{"git stack modify --continue", "call stack_modify with continue: true"},
	{"git stack modify --abort", "call stack_modify with abort: true"},
	{"git stack continue", "call stack_continue"},
	{"git stack abort", "call stack_abort"},
	{"git stack top", "stack_navigate {direction: \"top\"}"},
	{"git stack checkout --trunk", "stack_navigate {direction: \"bottom\"} then {direction: \"down\"}"},
	{"git stack checkout", "stack_navigate {branch: ...}"},
	{"git stack create", "stack_create"},
	{"git stack sync", "stack_sync"},
}

// wrapErr converts any error into a *jsonError.
func wrapErr(err error) error {
	if err == nil {
		return nil
	}
	var je *jsonError
	if errors.As(err, &je) {
		return err
	}
	body := toolError{Code: stack.KindUnknown.Code(), Message: err.Error()}
	if se, ok := errors.AsType[*stack.Error](err); ok {
		body = toolError{Code: se.Kind.Code(), Message: se.Msg, Branch: se.Branch, Files: se.Files, Detail: se.Detail}
		for _, step := range se.NextSteps {
			for _, r := range cliToTool {
				step = strings.ReplaceAll(step, r.from, r.to)
			}
			body.NextSteps = append(body.NextSteps, step)
		}
	}
	return &jsonError{body: body, cause: err}
}
