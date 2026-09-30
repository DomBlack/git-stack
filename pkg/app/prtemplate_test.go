package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindPRTemplate(t *testing.T) {
	dir := t.TempDir()
	if _, ok := FindPRTemplate(dir); ok {
		t.Fatal("no template expected")
	}
	if err := os.MkdirAll(filepath.Join(dir, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "docs", "PULL_REQUEST_TEMPLATE.md"), []byte("docs one"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, ok := FindPRTemplate(dir); !ok || got != "docs one" {
		t.Errorf("docs/: %q %v", got, ok)
	}
	if err := os.WriteFile(filepath.Join(dir, "pull_request_template"), []byte("root one"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, _ := FindPRTemplate(dir); got != "root one" {
		t.Errorf("root wins over docs: %q", got)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".github"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".github", "pull_request_template.md"), []byte("---\nname: x\n---\n\n## Why\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, _ := FindPRTemplate(dir); got != "## Why\n" {
		t.Errorf(".github wins and front matter is stripped: %q", got)
	}
	if err := os.WriteFile(filepath.Join(dir, ".github", "pull_request_template_notes.md"), []byte("no"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, _ := FindPRTemplate(dir); got != "## Why\n" {
		t.Errorf("suffix must be a dot or end: %q", got)
	}
	if got := stripFrontMatter("plain"); got != "plain" {
		t.Errorf("stripFrontMatter(plain) = %q", got)
	}
}
