package ai

import (
	"fmt"
	"strings"
)

// MaxDiffBytes bounds the diff sent to the model.
const MaxDiffBytes = 100_000

// TruncateDiff cuts a diff at a line boundary near max bytes and reports
// whether it did.
func TruncateDiff(diff string, maxBytes int) (string, bool) {
	if len(diff) <= maxBytes {
		return diff, false
	}
	cut := diff[:maxBytes]
	if i := strings.LastIndex(cut, "\n"); i > 0 {
		cut = cut[:i]
	}
	return cut + "\n[diff truncated]\n", true
}

// CommitSchema is the JSON schema for Commit.
const CommitSchema = `{"type":"object","properties":{"branch":{"type":"string","description":"kebab-case branch name without prefix"},"message":{"type":"string","description":"full commit message: subject line, blank line, optional body"}},"required":["branch","message"],"additionalProperties":false}`

// PRSchema is the JSON schema for PullRequest.
const PRSchema = `{"type":"object","properties":{"title":{"type":"string"},"body":{"type":"string","description":"markdown body"}},"required":["title","body"],"additionalProperties":false}`

// CommitPrompt renders the instruction and the stdin context for
// DraftCommit.
func CommitPrompt(in CommitInput) (instruction, context string) {
	var b strings.Builder
	b.WriteString("You name git branches and write commit messages for a developer. ")
	b.WriteString("The staged diff and repository context are provided on standard input; do not run any tools.\n\n")
	b.WriteString("Respond with JSON matching the schema: {\"branch\": string, \"message\": string}.\n\n")
	b.WriteString("Branch name rules: lowercase kebab-case (a-z, 0-9, '-' and '/'), at most 40 characters, ")
	b.WriteString("descriptive of the change, no dates or ticket noise.")
	if in.BranchPrefix != "" {
		fmt.Fprintf(&b, " The prefix %q is added automatically; do not include it.", in.BranchPrefix)
	}
	if len(in.TakenBranches) > 0 {
		b.WriteString(" These names are taken: " + strings.Join(in.TakenBranches, ", ") + ".")
	}
	b.WriteString("\n\n")
	if in.Message != "" {
		b.WriteString("The commit message is already decided and is provided under '## Commit message'. ")
		b.WriteString("Return it unchanged in the \"message\" field and only generate the branch name.\n")
	} else {
		b.WriteString("Commit message rules: an imperative subject line of at most 72 characters, then a blank line, ")
		b.WriteString("then an optional body wrapped at 72 columns explaining why the change was made. ")
		b.WriteString("Match the style of the recent commit subjects (prefixes, capitalisation, punctuation).\n")
	}
	if in.ExtraPrompt != "" {
		b.WriteString("\nHouse rules:\n" + in.ExtraPrompt + "\n")
	}

	var c strings.Builder
	if in.Message != "" {
		c.WriteString("## Commit message\n" + in.Message + "\n\n")
	}
	if len(in.RecentSubjects) > 0 {
		c.WriteString("## Recent commit subjects on trunk\n")
		for _, s := range in.RecentSubjects {
			c.WriteString("- " + s + "\n")
		}
		c.WriteString("\n")
	}
	c.WriteString("## Staged diff\n")
	c.WriteString(in.Diff)
	if in.DiffTruncated && !strings.Contains(in.Diff, "[diff truncated]") {
		c.WriteString("\n[diff truncated]\n")
	}
	return b.String(), c.String()
}

// PRPrompt renders the instruction and stdin context for DraftPR.
func PRPrompt(in PRInput) (instruction, context string) {
	var b strings.Builder
	b.WriteString("You write pull request titles and descriptions. ")
	fmt.Fprintf(&b, "The PR is for branch %q against %q; its diff, commit messages and context are on standard input. Do not run any tools.\n\n", in.Branch, in.Parent)
	b.WriteString("Respond with JSON matching the schema: {\"title\": string, \"body\": string}.\n\n")
	b.WriteString("Title: at most 70 characters, imperative, no trailing period, in the style of the recent commit subjects. ")
	b.WriteString("Body: markdown explaining what changed and why, for a reviewer who has not seen the diff; keep it concise; ")
	b.WriteString("do not restate the diff line by line. ")
	if in.Template != "" {
		b.WriteString("A pull request template is provided under '## Template': fill it in, keeping its headings and structure, ")
		b.WriteString("and remove instructions or placeholder comments that no longer apply. ")
	}
	b.WriteString("\n")
	if in.ExtraPrompt != "" {
		b.WriteString("\nHouse rules:\n" + in.ExtraPrompt + "\n")
	}

	var c strings.Builder
	if in.Template != "" {
		c.WriteString("## Template\n" + in.Template + "\n\n")
	}
	if len(in.Commits) > 0 {
		c.WriteString("## Commits on the branch (oldest first)\n")
		for _, m := range in.Commits {
			c.WriteString("---\n" + m + "\n")
		}
		c.WriteString("\n")
	}
	if len(in.RecentSubjects) > 0 {
		c.WriteString("## Recent commit subjects on trunk\n")
		for _, s := range in.RecentSubjects {
			c.WriteString("- " + s + "\n")
		}
		c.WriteString("\n")
	}
	fmt.Fprintf(&c, "## Diff (%s..%s)\n", in.Parent, in.Branch)
	c.WriteString(in.Diff)
	if in.DiffTruncated && !strings.Contains(in.Diff, "[diff truncated]") {
		c.WriteString("\n[diff truncated]\n")
	}
	return b.String(), c.String()
}

// ValidateCommit checks a drafted commit for the obvious failure modes.
func ValidateCommit(c Commit) error {
	if strings.TrimSpace(c.BranchName) == "" {
		return fmt.Errorf("empty branch name")
	}
	if strings.ContainsAny(c.BranchName, " \t\n") {
		return fmt.Errorf("branch name %q contains whitespace", c.BranchName)
	}
	if strings.TrimSpace(c.Message) == "" {
		return fmt.Errorf("empty commit message")
	}
	return nil
}

// ValidatePR checks a drafted pull request.
func ValidatePR(p PullRequest) error {
	if strings.TrimSpace(p.Title) == "" {
		return fmt.Errorf("empty title")
	}
	if strings.Contains(strings.TrimSpace(p.Title), "\n") {
		return fmt.Errorf("title spans several lines")
	}
	return nil
}
