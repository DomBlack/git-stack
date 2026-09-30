package ai

import (
	"strings"
	"testing"
)

func TestTruncateDiff(t *testing.T) {
	diff := "line1\nline2\nline3\n"
	got, cut := TruncateDiff(diff, 8)
	if !cut || got != "line1\n[diff truncated]\n" {
		t.Errorf("got %q cut=%v", got, cut)
	}
	if got, cut := TruncateDiff(diff, 100); cut || got != diff {
		t.Errorf("short diff altered: %q %v", got, cut)
	}
}

func TestCommitPrompt(t *testing.T) {
	instr, ctx := CommitPrompt(CommitInput{
		Diff: "+x", RecentSubjects: []string{"feat: a"}, BranchPrefix: "dom/", TakenBranches: []string{"dom/x"},
		ExtraPrompt: "Use British spelling.",
	})
	for _, want := range []string{`"dom/"`, "dom/x", "British spelling", "at most 72 characters"} {
		if !strings.Contains(instr, want) {
			t.Errorf("instruction missing %q", want)
		}
	}
	if !strings.Contains(ctx, "## Staged diff\n+x") || !strings.Contains(ctx, "- feat: a") {
		t.Errorf("context = %q", ctx)
	}
	if strings.Contains(instr, "+x") {
		t.Error("diff must go on stdin, not in the instruction")
	}

	instr, ctx = CommitPrompt(CommitInput{Diff: "+y", Message: "fixed", DiffTruncated: true})
	if !strings.Contains(instr, "only generate the branch name") || !strings.Contains(ctx, "## Commit message\nfixed") || !strings.Contains(ctx, "[diff truncated]") {
		t.Errorf("fixed message prompt: %q / %q", instr, ctx)
	}
}

func TestPRPrompt(t *testing.T) {
	instr, ctx := PRPrompt(PRInput{Branch: "b", Parent: "a", Diff: "+z", Commits: []string{"one", "two"}, Template: "## Why\n"})
	if !strings.Contains(instr, `"b" against "a"`) || !strings.Contains(instr, "template") {
		t.Errorf("instruction = %q", instr)
	}
	if !strings.Contains(ctx, "## Template\n## Why") || !strings.Contains(ctx, "---\none\n---\ntwo") || !strings.Contains(ctx, "## Diff (a..b)\n+z") {
		t.Errorf("context = %q", ctx)
	}
}

func TestValidate(t *testing.T) {
	if err := ValidateCommit(Commit{BranchName: "ok", Message: "m"}); err != nil {
		t.Error(err)
	}
	for _, bad := range []Commit{{}, {BranchName: "a b", Message: "m"}, {BranchName: "x", Message: " "}} {
		if err := ValidateCommit(bad); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
	if err := ValidatePR(PullRequest{Title: "t", Body: ""}); err != nil {
		t.Error(err)
	}
	for _, bad := range []PullRequest{{}, {Title: "a\nb"}} {
		if err := ValidatePR(bad); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
}
