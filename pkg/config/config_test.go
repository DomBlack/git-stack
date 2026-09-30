package config_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/DomBlack/git-stack/pkg/config"
	"github.com/DomBlack/git-stack/pkg/exec"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/git/gittest"
)

func TestFromEntries(t *testing.T) {
	c, err := config.FromEntries([]git.ConfigEntry{
		{Key: "stack.branchprefix", Value: "dom/"},
		{Key: "stack.ai.model", Value: "sonnet"},
		{Key: "stack.ai.timeout", Value: "2m"},
		{Key: "stack.cachettl", Value: "30s"},
		{Key: "stack.submit.default", Value: "publish"},
		{Key: "stack.managedaliases", Value: "c"},
		{Key: "stack.managedaliases", Value: "co"},
		{Key: "stack.unknownkey", Value: "ignored"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if c.BranchPrefix != "dom/" || c.AIModel != "sonnet" || c.AICommand != "claude" {
		t.Errorf("unexpected: %+v", c)
	}
	if c.AITimeout != 2*time.Minute || c.CacheTTL != 30*time.Second {
		t.Errorf("durations: %+v", c)
	}
	if c.SubmitDefault != config.SubmitPublish || !slices.Equal(c.ManagedAliases, []string{"c", "co"}) {
		t.Errorf("submit/aliases: %+v", c)
	}
}

func TestFromEntriesRejectsBadValues(t *testing.T) {
	for _, e := range []git.ConfigEntry{
		{Key: "stack.ai.timeout", Value: "soon"},
		{Key: "stack.cachettl", Value: "5"},
		{Key: "stack.submit.default", Value: "maybe"},
	} {
		_, err := config.FromEntries([]git.ConfigEntry{e})
		if err == nil || !strings.Contains(err.Error(), "stack.") {
			t.Errorf("%v: err = %v, want error naming the key", e, err)
		}
	}
}

func TestLoadFromRepo(t *testing.T) {
	gittest.Isolate(t)
	dir := gittest.InitRepo(t)
	gittest.Run(t, dir, "config", "--global", "stack.branchPrefix", "dom/")
	gittest.Run(t, dir, "config", "stack.ai.model", "opus")

	g := git.New(exec.New())
	repo, err := g.Discover(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	c, err := config.Load(context.Background(), g, repo)
	if err != nil {
		t.Fatal(err)
	}
	if c.BranchPrefix != "dom/" || c.AIModel != "opus" || c.SubmitDefault != config.SubmitAsk {
		t.Errorf("Load = %+v", c)
	}
}
