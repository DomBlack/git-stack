package github

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/DomBlack/git-stack/pkg/exec"
	"github.com/DomBlack/git-stack/pkg/exec/exectest"
	"github.com/DomBlack/git-stack/pkg/forge"
	"github.com/DomBlack/git-stack/pkg/git"
	"github.com/DomBlack/git-stack/pkg/stack"
)

func run(name, status, conclusion string) checkContext {
	return checkContext{Typename: "CheckRun", Name: name, Status: status, Conclusion: conclusion}
}

func status(name, state string) checkContext {
	return checkContext{Typename: "StatusContext", Context: name, State: state}
}

func TestClassifyChecks(t *testing.T) {
	tests := []struct {
		in   checkContext
		want checkVerdict
	}{
		{run("t", "COMPLETED", "SUCCESS"), checkOK},
		{run("t", "COMPLETED", "NEUTRAL"), checkOK},
		{run("t", "COMPLETED", "SKIPPED"), checkOK},
		{run("t", "COMPLETED", "STALE"), checkOK},
		{run("t", "COMPLETED", "FAILURE"), checkFailing},
		{run("t", "COMPLETED", "TIMED_OUT"), checkFailing},
		{run("t", "COMPLETED", "CANCELLED"), checkFailing},
		{run("t", "COMPLETED", "ACTION_REQUIRED"), checkFailing},
		{run("t", "COMPLETED", "STARTUP_FAILURE"), checkFailing},
		{run("t", "QUEUED", ""), checkPending},
		{run("t", "IN_PROGRESS", ""), checkPending},
		{run("t", "WAITING", ""), checkPending},
		{run("t", "PENDING", ""), checkPending},
		{run("t", "REQUESTED", ""), checkPending},
		{status("ci/x", "SUCCESS"), checkOK},
		{status("ci/x", "FAILURE"), checkFailing},
		{status("ci/x", "ERROR"), checkFailing},
		{status("ci/x", "PENDING"), checkPending},
		{status("ci/x", "EXPECTED"), checkPending},
	}
	for _, tt := range tests {
		name := tt.in.Typename + "/" + tt.in.Status + tt.in.Conclusion + tt.in.State
		t.Run(name, func(t *testing.T) {
			if got := classify(tt.in); got != tt.want {
				t.Errorf("classify(%+v) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

func TestSummariseChecks(t *testing.T) {
	tests := []struct {
		name string
		in   []checkContext
		want forge.CheckSummary
	}{
		{"none", nil, forge.CheckSummary{}},
		{"all passing", []checkContext{run("lint", "COMPLETED", "SUCCESS"), status("ci/x", "SUCCESS"), run("docs", "COMPLETED", "SKIPPED")}, forge.CheckSummary{}},
		{"a mix", []checkContext{
			run("lint", "COMPLETED", "FAILURE"),
			run("test (ubuntu-latest)", "COMPLETED", "SUCCESS"),
			run("test (macos-latest)", "IN_PROGRESS", ""),
			status("ci/legacy", "ERROR"),
			status("deploy", "EXPECTED"),
			run("lint", "COMPLETED", "FAILURE"), // the same check twice is named once
		}, forge.CheckSummary{Failing: []string{"lint", "ci/legacy"}, Pending: []string{"test (macos-latest)", "deploy"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := summarise(forge.CheckSummary{}, tt.in)
			if !slices.Equal(got.Failing, tt.want.Failing) || !slices.Equal(got.Pending, tt.want.Pending) {
				t.Errorf("summarise = %+v, want %+v", got, tt.want)
			}
			if got.Clean() != (len(tt.want.Failing)+len(tt.want.Pending) == 0) {
				t.Errorf("Clean = %v for %+v", got.Clean(), got)
			}
		})
	}
}

// rollup renders one aliased pull request's part of a checks response.
func rollup(contexts string, next string) string {
	page := `{"hasNextPage":false,"endCursor":"X"}`
	if next != "" {
		page = fmt.Sprintf(`{"hasNextPage":true,"endCursor":%q}`, next)
	}
	return fmt.Sprintf(`{"commits":{"nodes":[{"commit":{"statusCheckRollup":{"contexts":{"pageInfo":%s,"nodes":[%s]}}}}]}}`, page, contexts)
}

// queryOf is the GraphQL query a gh api graphql call sent.
func queryOf(c exectest.Call) string {
	for _, a := range c.Args {
		if q, ok := strings.CutPrefix(a, "query="); ok {
			return q
		}
	}
	return ""
}

func TestChecksAsksForEveryPullRequestInOneQuery(t *testing.T) {
	f := exectest.New()
	f.On("gh", "api", "graphql").Reply(`{"data":{"repository":{` +
		`"p12":` + rollup(`{"__typename":"CheckRun","name":"lint","status":"COMPLETED","conclusion":"FAILURE"},{"__typename":"CheckRun","name":"test","status":"COMPLETED","conclusion":"SUCCESS"}`, "") + `,` +
		`"p13":` + rollup(`{"__typename":"StatusContext","context":"ci/build","state":"PENDING"}`, "") + `,` +
		`"p14":{"commits":{"nodes":[{"commit":{"statusCheckRollup":null}}]}}}}}`)
	got, err := New(f).Checks(context.Background(), git.Repo{TopLevel: "/r"}, []int{13, 12, 14})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got[12].Failing, []string{"lint"}) || len(got[12].Pending) != 0 {
		t.Errorf("#12 = %+v", got[12])
	}
	if !slices.Equal(got[13].Pending, []string{"ci/build"}) || len(got[13].Failing) != 0 {
		t.Errorf("#13 = %+v", got[13])
	}
	if !got[14].Clean() {
		t.Errorf("#14 has no checks and should pass: %+v", got[14])
	}
	calls := f.CallsTo("gh")
	if len(calls) != 1 {
		t.Fatalf("want one request, got %d: %v", len(calls), calls)
	}
	c := calls[0]
	if c.Dir != "/r" || !slices.Contains(c.Args, "owner={owner}") || !slices.Contains(c.Args, "name={repo}") {
		t.Errorf("call = %+v", c)
	}
	q := queryOf(c)
	for _, want := range []string{
		"repository(owner: $owner, name: $name)",
		"p12: pullRequest(number: 12)", "p13: pullRequest(number: 13)", "p14: pullRequest(number: 14)",
		"commits(last: 1)", "contexts(first: 100)", "pageInfo { hasNextPage endCursor }",
		"... on CheckRun { name status conclusion }", "... on StatusContext { context state }",
	} {
		if !strings.Contains(q, want) {
			t.Errorf("query lacks %q:\n%s", want, q)
		}
	}
}

func TestChecksReadsEveryPage(t *testing.T) {
	f := exectest.New()
	calls := 0
	f.On("gh", "api", "graphql").Do(func(c exec.Cmd) (exec.Result, error) {
		calls++
		var body string
		switch calls {
		case 1:
			// #12 has more than a page; #13 fits in one.
			body = `"p12":` + rollup(`{"__typename":"CheckRun","name":"a","status":"COMPLETED","conclusion":"SUCCESS"}`, "cur-1") +
				`,"p13":` + rollup(`{"__typename":"CheckRun","name":"b","status":"COMPLETED","conclusion":"FAILURE"}`, "")
		case 2:
			body = `"p12":` + rollup(`{"__typename":"CheckRun","name":"c","status":"QUEUED","conclusion":""}`, "cur-2")
		default:
			body = `"p12":` + rollup(`{"__typename":"CheckRun","name":"d","status":"COMPLETED","conclusion":"TIMED_OUT"}`, "")
		}
		return exec.Result{Stdout: []byte(`{"data":{"repository":{` + body + `}}}`)}, nil
	})
	got, err := New(f).Checks(context.Background(), git.Repo{TopLevel: "/r"}, []int{12, 13})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got[12].Failing, []string{"d"}) || !slices.Equal(got[12].Pending, []string{"c"}) || !slices.Equal(got[13].Failing, []string{"b"}) {
		t.Errorf("checks = %+v", got)
	}
	gh := f.CallsTo("gh")
	if len(gh) != 3 {
		t.Fatalf("want 3 requests, got %d", len(gh))
	}
	// Later pages only ask for the pull request that has more, from its cursor.
	for i, cur := range []string{"cur-1", "cur-2"} {
		c := gh[i+1]
		q := queryOf(c)
		if strings.Contains(q, "p13:") || !strings.Contains(q, "contexts(first: 100, after: $c12)") || !strings.Contains(q, "$c12: String") {
			t.Errorf("page %d query = %s", i+2, q)
		}
		if !slices.Contains(c.Args, "c12="+cur) {
			t.Errorf("page %d args = %q, want c12=%s", i+2, c.Args, cur)
		}
	}
}

func TestChecksFailure(t *testing.T) {
	f := exectest.New()
	f.On("gh", "api", "graphql").Fail(1, "gh: Could not resolve to a PullRequest with the number of 19.")
	_, err := New(f).Checks(context.Background(), git.Repo{TopLevel: "/r"}, []int{19})
	if se, ok := errors.AsType[*stack.Error](err); !ok || se.Kind != stack.KindAPIFailure || !strings.Contains(se.Detail, "number of 19") {
		t.Errorf("err = %v", err)
	}
}
