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
	f.On("gh", "api", "graphql").Reply(`{"data":{"repository":{"p41":{"commits":{"nodes":[{"commit":{"statusCheckRollup":null}}]}}}}}`)

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

// TestMergeCommandChecks merges up to b of main -> a (#41) -> b (#42) -> c
// while GitHub reports checks failing, then running, then unreadable, and
// finally with --force.
func TestMergeCommandChecks(t *testing.T) {
	dir := stackRepo(t)
	f := fakeGh(t, dir)
	f.On("gh", "pr", "list").Reply(`[{"number":41,"url":"https://example.com/41","title":"A","state":"OPEN","isDraft":false,"headRefName":"a","baseRefName":"main"},
		{"number":42,"url":"https://example.com/42","title":"B","state":"OPEN","isDraft":false,"headRefName":"b","baseRefName":"a"}]`)
	f.On("gh", "api", "--method", "PUT", "repos/{owner}/{repo}/pulls/42/merge-async").Reply(`{"status":"merged","details":{"sha":"0123456789abcdef"}}`)
	rollup := func(contexts string) string {
		return `{"commits":{"nodes":[{"commit":{"statusCheckRollup":{"contexts":{"pageInfo":{"hasNextPage":false},"nodes":[` + contexts + `]}}}}]}}`
	}
	const (
		lintFailed = `{"__typename":"CheckRun","name":"lint","status":"COMPLETED","conclusion":"FAILURE"}`
		testFailed = `{"__typename":"CheckRun","name":"test (ubuntu-latest)","status":"COMPLETED","conclusion":"FAILURE"}`
		passed     = `{"__typename":"CheckRun","name":"vet","status":"COMPLETED","conclusion":"SUCCESS"}`
		running    = `{"__typename":"CheckRun","name":"test (macos-latest)","status":"IN_PROGRESS","conclusion":null}`
		expected   = `{"__typename":"StatusContext","context":"deploy/preview","state":"EXPECTED"}`
	)
	merges := func() int {
		n := 0
		for _, c := range f.CallsTo("gh") {
			if len(c.Args) > 2 && c.Args[1] == "--method" {
				n++
			}
		}
		return n
	}
	// What the user sees: the headline, then the error as root prints it.
	shown := func(errOut string, err error) string {
		var b strings.Builder
		printError(&b, err)
		return errOut + b.String()
	}

	f.On("gh", "api", "graphql").Reply(`{"data":{"repository":{"p41":` + rollup(lintFailed+","+testFailed+","+passed) + `,"p42":` + rollup(running) + `}}}`)
	_, errOut, err := runWith(t, f, "--cwd", dir, "merge", "b", "--no-sync")
	want := "Merging 2 pull requests into main...\n" +
		"error: 1 pull request has failing checks; nothing was merged\n" +
		"  #41 a: lint, test (ubuntu-latest)\n" +
		"  #42 b: test (macos-latest) still running\n" +
		"  - fix them and run git stack submit, or git stack merge --force to merge anyway\n"
	if got := shown(errOut, err); err == nil || got != want {
		t.Errorf("failing: stderr =\n%s\nwant\n%s", got, want)
	}
	if exitCode(err) != 1 {
		t.Errorf("exit code %d", exitCode(err))
	}
	t.Logf("failing:\n%s", shown(errOut, err))

	f.On("gh", "api", "graphql").Reply(`{"data":{"repository":{"p41":` + rollup(passed) + `,"p42":` + rollup(running+","+expected) + `}}}`)
	_, errOut, err = runWith(t, f, "--cwd", dir, "merge", "b", "--no-sync")
	want = "Merging 2 pull requests into main...\n" +
		"error: 1 pull request still has checks running; nothing was merged\n" +
		"  #42 b: test (macos-latest), deploy/preview\n" +
		"  - wait for them to finish and run git stack merge again, or git stack merge --force to merge anyway\n"
	if got := shown(errOut, err); err == nil || got != want {
		t.Errorf("pending: stderr =\n%s\nwant\n%s", got, want)
	}
	t.Logf("pending:\n%s", shown(errOut, err))
	if n := merges(); n != 0 {
		t.Fatalf("GitHub was asked to merge %d times despite the checks", n)
	}

	// -f skips the checks altogether.
	before := len(f.CallsTo("gh"))
	out, errOut, err := runWith(t, f, "--cwd", dir, "merge", "b", "--no-sync", "-f")
	if err != nil || !strings.Contains(out, "ok: Merged 2 pull requests into main at 0123456") {
		t.Errorf("force: %v\n%s%s", err, out, errOut)
	}
	for _, c := range f.CallsTo("gh")[before:] {
		if len(c.Args) > 1 && c.Args[1] == "graphql" {
			t.Errorf("--force should not ask for checks: %v", c.Args)
		}
	}

	// Checks that can't be read don't stop the merge; a notice says so.
	f.On("gh", "api", "graphql").Fail(1, "gh: HTTP 502: Bad Gateway (https://api.github.com/graphql)")
	out, errOut, err = runWith(t, f, "--cwd", dir, "merge", "b", "--no-sync")
	if err != nil || !strings.Contains(out, "ok: Merged 2 pull requests") ||
		!strings.Contains(errOut, "note: couldn't verify the pull requests' checks (HTTP 502: Bad Gateway (https://api.github.com/graphql)), so went ahead anyway; the repository's own branch rules still apply\n") {
		t.Errorf("unreadable checks: %v\nstdout %q\nstderr %q", err, out, errOut)
	}
}
