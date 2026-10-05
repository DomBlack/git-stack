package cmd

import (
	"strings"
	"testing"
)

// TestMergeCommand merges up to the bottom branch of main -> a (#41) -> b -> c
// through a faked GitHub and checks the result lines and that nothing else
// was asked of gh once the merge was refused.
func TestMergeCommand(t *testing.T) {
	dir := stackRepo(t)
	f := fakeGh(t, dir)
	f.On("gh", "pr", "list").Reply(`[{"number":41,"url":"https://example.com/41","title":"A","state":"OPEN","isDraft":false,"headRefName":"a","baseRefName":"main"},
		{"number":42,"url":"https://example.com/42","title":"B","state":"OPEN","isDraft":true,"headRefName":"b","baseRefName":"a"}]`)
	f.On("gh", "api", "--method", "PUT", "repos/{owner}/{repo}/pulls/41/merge-async").Reply(`{"status":"merged","details":{"sha":"0123456789abcdef"}}`)

	out, errOut, err := runWith(t, f, "--cwd", dir, "merge", "a", "--rebase", "--no-sync")
	if err != nil {
		t.Fatalf("merge a: %v\n%s", err, errOut)
	}
	if !strings.Contains(out, "ok: Merged 1 pull request into main at 0123456") || !strings.Contains(out, "  a  #41") {
		t.Errorf("stdout = %q", out)
	}
	if !strings.Contains(errOut, "Merging 1 pull request into main...") {
		t.Errorf("stderr = %q", errOut)
	}
	var put []string
	for _, c := range f.CallsTo("gh") {
		if len(c.Args) > 2 && c.Args[0] == "api" && c.Args[1] == "--method" {
			put = c.Args
		}
	}
	if strings.Join(put, " ") != "api --method PUT repos/{owner}/{repo}/pulls/41/merge-async -f merge_method=rebase" {
		t.Errorf("PUT = %q", put)
	}

	// b's PR is a draft, so merging up to the current branch (b) is refused
	// before GitHub is asked.
	before := len(f.CallsTo("gh"))
	_, errOut, err = runWith(t, f, "--cwd", dir, "merge")
	if err == nil || !strings.Contains(err.Error(), "draft") {
		t.Errorf("merge up to a draft: %v\n%s", err, errOut)
	}
	for _, c := range f.CallsTo("gh")[before:] {
		if c.Args[0] == "api" {
			t.Errorf("GitHub was asked to merge despite the draft: %v", c.Args)
		}
	}
}
