package git

import (
	"context"
	"errors"
	"strings"

	"github.com/DomBlack/git-stack/pkg/exec"
)

// Scope selects which git config file an operation targets.
type Scope int

const (
	// ScopeMerged reads the effective value across all files (reads only).
	ScopeMerged Scope = iota
	// ScopeGlobal targets the user's global config.
	ScopeGlobal
	// ScopeLocal targets the repository config.
	ScopeLocal
)

func (s Scope) flag() []string {
	switch s {
	case ScopeGlobal:
		return []string{"--global"}
	case ScopeLocal:
		return []string{"--local"}
	default:
		return nil
	}
}

// ConfigEntry is one key/value pair.
type ConfigEntry struct {
	Key   string
	Value string
}

// config runs `git config` with the scope flags. Global-scope calls do not
// need a repository; repo may be the zero value.
func (c *Client) config(ctx context.Context, repo Repo, scope Scope, args ...string) (exec.Result, error) {
	full := append([]string{"config"}, scope.flag()...)
	full = append(full, args...)
	return c.git(ctx, repo.TopLevel, full...)
}

// ConfigGet returns the value of key. The boolean reports whether it was set.
func (c *Client) ConfigGet(ctx context.Context, repo Repo, scope Scope, key string) (string, bool, error) {
	res, err := c.config(ctx, repo, scope, "--get", key)
	if err != nil {
		if ee, ok := errors.AsType[*exec.ExitError](err); ok && ee.Result.ExitCode == 1 {
			return "", false, nil
		}
		return "", false, err
	}
	return res.Out(), true, nil
}

// ConfigGetAll returns every value of a multi-valued key.
func (c *Client) ConfigGetAll(ctx context.Context, repo Repo, scope Scope, key string) ([]string, error) {
	res, err := c.config(ctx, repo, scope, "-z", "--get-all", key)
	if err != nil {
		if ee, ok := errors.AsType[*exec.ExitError](err); ok && ee.Result.ExitCode == 1 {
			return nil, nil
		}
		return nil, err
	}
	return splitNUL(string(res.Stdout)), nil
}

// ConfigGetRegexp returns every entry whose key matches the regexp.
func (c *Client) ConfigGetRegexp(ctx context.Context, repo Repo, scope Scope, re string) ([]ConfigEntry, error) {
	res, err := c.config(ctx, repo, scope, "-z", "--get-regexp", re)
	if err != nil {
		if ee, ok := errors.AsType[*exec.ExitError](err); ok && ee.Result.ExitCode == 1 {
			return nil, nil
		}
		return nil, err
	}
	var out []ConfigEntry
	for rec := range strings.SplitSeq(string(res.Stdout), "\x00") {
		if rec == "" {
			continue
		}
		key, value, _ := strings.Cut(rec, "\n")
		out = append(out, ConfigEntry{Key: key, Value: value})
	}
	return out, nil
}

// ConfigSet sets key to value, replacing existing values.
func (c *Client) ConfigSet(ctx context.Context, repo Repo, scope Scope, key, value string) error {
	_, err := c.config(ctx, repo, scope, key, value)
	return err
}

// ConfigAdd appends a value to a multi-valued key.
func (c *Client) ConfigAdd(ctx context.Context, repo Repo, scope Scope, key, value string) error {
	_, err := c.config(ctx, repo, scope, "--add", key, value)
	return err
}

// ConfigUnset removes every value of key. A missing key is not an error.
func (c *Client) ConfigUnset(ctx context.Context, repo Repo, scope Scope, key string) error {
	_, err := c.config(ctx, repo, scope, "--unset-all", key)
	if err != nil {
		if ee, ok := errors.AsType[*exec.ExitError](err); ok && ee.Result.ExitCode == 5 {
			return nil
		}
		return err
	}
	return nil
}

// ConfigUnsetValue removes the values of key that match the given value
// (exact match). A missing key or value is not an error.
func (c *Client) ConfigUnsetValue(ctx context.Context, repo Repo, scope Scope, key, value string) error {
	_, err := c.config(ctx, repo, scope, "--fixed-value", "--unset-all", key, value)
	if err != nil {
		if ee, ok := errors.AsType[*exec.ExitError](err); ok && ee.Result.ExitCode == 5 {
			return nil
		}
		return err
	}
	return nil
}

func nonEmptyLines(s string) []string {
	var out []string
	for line := range strings.SplitSeq(s, "\n") {
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

// RemoteURL is the URL git uses to fetch from the named remote, with
// url.<base>.insteadOf rewriting applied (git remote get-url). It only reads
// config; nothing touches the network.
func (c *Client) RemoteURL(ctx context.Context, repo Repo, name string) (string, error) {
	res, err := c.gitIn(ctx, repo, "remote", "get-url", "--", name)
	if err != nil {
		return "", err
	}
	return res.Out(), nil
}
