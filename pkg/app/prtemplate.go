package app

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var rePRTemplate = regexp.MustCompile(`(?i)^pull[_-]request[_-]template(\.|$)`)

// prTemplateDirs are searched in order, like gh's FindLegacy.
var prTemplateDirs = []string{".github", "", "docs"}

// FindPRTemplate returns the repository's pull request template, if any,
// with YAML front matter removed. The lookup mirrors gh: a regular file
// named pull_request_template.* in .github/, the root, or docs/.
func FindPRTemplate(topLevel string) (string, bool) {
	for _, dir := range prTemplateDirs {
		entries, err := os.ReadDir(filepath.Join(topLevel, dir))
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				continue
			}
			continue
		}
		for _, e := range entries {
			if e.Type()&fs.ModeSymlink != 0 || !e.Type().IsRegular() || !rePRTemplate.MatchString(e.Name()) {
				continue
			}
			b, err := os.ReadFile(filepath.Join(topLevel, dir, e.Name()))
			if err != nil {
				continue
			}
			return stripFrontMatter(string(b)), true
		}
	}
	return "", false
}

// stripFrontMatter removes a leading --- … --- YAML block.
func stripFrontMatter(s string) string {
	if !strings.HasPrefix(s, "---\n") && !strings.HasPrefix(s, "---\r\n") {
		return s
	}
	rest := s[strings.Index(s, "\n")+1:]
	for _, end := range []string{"\n---\n", "\n---\r\n"} {
		if i := strings.Index(rest, end); i >= 0 {
			return strings.TrimLeft(rest[i+len(end):], "\r\n")
		}
	}
	if strings.HasSuffix(strings.TrimRight(rest, "\r\n"), "\n---") || strings.TrimSpace(rest) == "---" {
		return ""
	}
	return s
}
