package github

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/DomBlack/git-stack/pkg/forge"
	"github.com/DomBlack/git-stack/pkg/git"
)

// checksPageSize is how many status check contexts are fetched per pull
// request per request; GitHub's limit for a connection.
const checksPageSize = 100

// checksMaxRounds bounds the follow up requests for pull requests with more
// than checksPageSize checks, so a forge that never stops saying there is
// another page can't keep us here.
const checksMaxRounds = 20

// rollupJSON is one aliased pullRequest in the checks query.
type rollupJSON struct {
	Commits struct {
		Nodes []struct {
			Commit struct {
				StatusCheckRollup *struct {
					Contexts struct {
						PageInfo struct {
							HasNextPage bool   `json:"hasNextPage"`
							EndCursor   string `json:"endCursor"`
						} `json:"pageInfo"`
						Nodes []checkContext `json:"nodes"`
					} `json:"contexts"`
				} `json:"statusCheckRollup"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"commits"`
}

// checkContext is a CheckRun (a GitHub Actions job or a check app's run)
// or a StatusContext (a commit status set through the statuses API).
type checkContext struct {
	Typename string `json:"__typename"`
	// CheckRun
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	// StatusContext
	Context string `json:"context"`
	State   string `json:"state"`
}

// checkVerdict is what one context means for a merge.
type checkVerdict int

const (
	checkOK checkVerdict = iota
	checkFailing
	checkPending
)

// classify says whether a context blocks a merge. A finished check run
// fails on FAILURE, TIMED_OUT, CANCELLED, ACTION_REQUIRED or
// STARTUP_FAILURE and passes on anything else (SUCCESS, NEUTRAL, SKIPPED,
// and STALE, which GitHub sets when a newer run replaced this one); one
// that hasn't finished (QUEUED, IN_PROGRESS, WAITING, PENDING, REQUESTED)
// is pending. A commit status fails on FAILURE or ERROR and is pending on
// PENDING or EXPECTED (a required status nothing has reported yet).
func classify(c checkContext) checkVerdict {
	switch c.Typename {
	case "CheckRun":
		if !strings.EqualFold(c.Status, "COMPLETED") {
			return checkPending
		}
		switch strings.ToUpper(c.Conclusion) {
		case "FAILURE", "TIMED_OUT", "CANCELLED", "ACTION_REQUIRED", "STARTUP_FAILURE":
			return checkFailing
		}
	case "StatusContext":
		switch strings.ToUpper(c.State) {
		case "FAILURE", "ERROR":
			return checkFailing
		case "PENDING", "EXPECTED":
			return checkPending
		}
	}
	return checkOK
}

// summarise adds contexts to a summary, naming each blocking check once.
func summarise(sum forge.CheckSummary, contexts []checkContext) forge.CheckSummary {
	for _, c := range contexts {
		name := c.Name
		if c.Typename == "StatusContext" {
			name = c.Context
		}
		switch classify(c) {
		case checkFailing:
			if !slices.Contains(sum.Failing, name) {
				sum.Failing = append(sum.Failing, name)
			}
		case checkPending:
			if !slices.Contains(sum.Pending, name) {
				sum.Pending = append(sum.Pending, name)
			}
		}
	}
	return sum
}

// Checks asks GitHub for the status check rollup of each pull request's
// head commit in one GraphQL request, with an aliased pullRequest field per
// number. The repository is the one gh resolves for the checkout ({owner}
// and {repo}), the same one MergeStack merges in. A pull request with more
// than checksPageSize checks has the rest read in follow up requests, one
// per page for all the pull requests that still have more.
func (f *Forge) Checks(ctx context.Context, repo git.Repo, numbers []int) (map[int]forge.CheckSummary, error) {
	out := map[int]forge.CheckSummary{}
	cursors := map[int]string{} // number -> where its next page starts
	todo := slices.Clone(numbers)
	slices.Sort(todo)
	todo = slices.Compact(todo)
	for round := 0; len(todo) > 0; round++ {
		if round == checksMaxRounds {
			return nil, fmt.Errorf("GitHub was still reporting more checks after %d pages", checksMaxRounds)
		}
		query, vars := checksQuery(todo, cursors)
		args := append([]string{"api", "graphql", "-f", "query=" + query, "-F", "owner={owner}", "-F", "name={repo}"}, vars...)
		res, err := f.gh(ctx, repo, "", args...)
		if err != nil {
			return nil, err
		}
		var body struct {
			Data struct {
				Repository map[string]*rollupJSON `json:"repository"`
			} `json:"data"`
		}
		if err := json.Unmarshal(res.Stdout, &body); err != nil {
			return nil, fmt.Errorf("parse the checks response: %w", err)
		}
		var next []int
		for _, n := range todo {
			pr := body.Data.Repository[alias(n)]
			if pr == nil || len(pr.Commits.Nodes) == 0 {
				continue
			}
			rollup := pr.Commits.Nodes[0].Commit.StatusCheckRollup
			if rollup == nil {
				continue // no checks at all
			}
			out[n] = summarise(out[n], rollup.Contexts.Nodes)
			if page := rollup.Contexts.PageInfo; page.HasNextPage && page.EndCursor != "" {
				cursors[n] = page.EndCursor
				next = append(next, n)
			}
		}
		todo = next
	}
	return out, nil
}

// alias names a pull request's field in the query: p<number>.
func alias(n int) string { return "p" + strconv.Itoa(n) }

// checksQuery builds the GraphQL query for numbers, with a cursor variable
// (c<number>) for each one past its first page, and the gh flags that set
// those variables.
func checksQuery(numbers []int, cursors map[int]string) (string, []string) {
	var (
		b     strings.Builder
		decls = []string{"$owner: String!", "$name: String!"}
		vars  []string
	)
	for _, n := range numbers {
		after := ""
		if c := cursors[n]; c != "" {
			v := "c" + strconv.Itoa(n)
			decls = append(decls, "$"+v+": String")
			vars = append(vars, "-f", v+"="+c)
			after = ", after: $" + v
		}
		fmt.Fprintf(&b, " %s: pullRequest(number: %d) { commits(last: 1) { nodes { commit { statusCheckRollup { contexts(first: %d%s) {"+
			" pageInfo { hasNextPage endCursor }"+
			" nodes { __typename ... on CheckRun { name status conclusion } ... on StatusContext { context state } } } } } } } }",
			alias(n), n, checksPageSize, after)
	}
	return "query(" + strings.Join(decls, ", ") + ") { repository(owner: $owner, name: $name) {" + b.String() + " } }", vars
}
