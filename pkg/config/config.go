// Package config reads git-stack settings from `git config stack.*`.
package config

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/DomBlack/git-stack/pkg/git"
)

// Config keys (git config is case-insensitive; these are the canonical forms).
const (
	KeyBranchPrefix   = "stack.branchPrefix"
	KeyAICommand      = "stack.ai.command"
	KeyAIModel        = "stack.ai.model"
	KeyAIExtraPrompt  = "stack.ai.extraPrompt"
	KeyAITimeout      = "stack.ai.timeout"
	KeyAIAuto         = "stack.ai.auto"
	KeyCacheTTL       = "stack.cacheTTL"
	KeySubmitDefault  = "stack.submit.default"
	KeyManagedAliases = "stack.managedAliases"
	KeyManagedFiles   = "stack.managedFiles"
)

// SubmitDefault values.
const (
	SubmitAsk     = "ask"
	SubmitDraft   = "draft"
	SubmitPublish = "publish"
)

// Config holds the effective settings.
type Config struct {
	BranchPrefix  string
	AICommand     string
	AIModel       string
	AIExtraPrompt string
	AITimeout     time.Duration
	// AIAuto makes create and submit behave as if --ai was passed, unless
	// --no-ai is given.
	AIAuto   bool
	CacheTTL time.Duration
	// SubmitDefault is ask, draft or publish.
	SubmitDefault  string
	ManagedAliases []string
	ManagedFiles   []string
}

// Defaults returns the built-in defaults.
func Defaults() *Config {
	return &Config{
		AICommand:     "claude",
		AIModel:       "haiku",
		AITimeout:     60 * time.Second,
		CacheTTL:      5 * time.Minute,
		SubmitDefault: SubmitAsk,
	}
}

// Load reads every stack.* key visible from repo (merged scopes) on top of
// the defaults. Invalid values are reported as errors naming the key.
func Load(ctx context.Context, g *git.Client, repo git.Repo) (*Config, error) {
	entries, err := g.ConfigGetRegexp(ctx, repo, git.ScopeMerged, `^stack\.`)
	if err != nil {
		return nil, fmt.Errorf("read git config: %w", err)
	}
	return FromEntries(entries)
}

// FromEntries builds a Config from raw git config entries.
func FromEntries(entries []git.ConfigEntry) (*Config, error) {
	c := Defaults()
	for _, e := range entries {
		key := strings.ToLower(e.Key)
		v := e.Value
		switch key {
		case strings.ToLower(KeyBranchPrefix):
			c.BranchPrefix = v
		case strings.ToLower(KeyAICommand):
			c.AICommand = v
		case strings.ToLower(KeyAIModel):
			c.AIModel = v
		case strings.ToLower(KeyAIExtraPrompt):
			c.AIExtraPrompt = v
		case strings.ToLower(KeyAITimeout):
			d, err := time.ParseDuration(v)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", KeyAITimeout, err)
			}
			c.AITimeout = d
		case strings.ToLower(KeyAIAuto):
			b, err := parseBool(v)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", KeyAIAuto, err)
			}
			c.AIAuto = b
		case strings.ToLower(KeyCacheTTL):
			d, err := time.ParseDuration(v)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", KeyCacheTTL, err)
			}
			c.CacheTTL = d
		case strings.ToLower(KeySubmitDefault):
			switch v {
			case SubmitAsk, SubmitDraft, SubmitPublish:
				c.SubmitDefault = v
			default:
				return nil, fmt.Errorf("%s: %q is not one of ask, draft, publish", KeySubmitDefault, v)
			}
		case strings.ToLower(KeyManagedAliases):
			c.ManagedAliases = append(c.ManagedAliases, v)
		case strings.ToLower(KeyManagedFiles):
			c.ManagedFiles = append(c.ManagedFiles, v)
		}
	}
	return c, nil
}

// parseBool accepts what git itself accepts for a boolean.
func parseBool(v string) (bool, error) {
	switch strings.ToLower(v) {
	case "true", "yes", "on", "1":
		return true, nil
	case "false", "no", "off", "0", "":
		return false, nil
	}
	return false, fmt.Errorf("%q is not a boolean (true/false)", v)
}
