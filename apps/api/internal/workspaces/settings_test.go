package workspaces

import "testing"

func TestMatchAnyDocumentationPaths(t *testing.T) {
	docs := []string{"docs/**", "doc/**", "*.md", "*.mdx", "*.rst", "*.adoc", "*.txt", "CHANGELOG*", "README*"}
	for _, p := range []string{"README.md", "docs/setup/install.md", "docs/img/arch.png", "pkg/api/README.md", "CHANGELOG", "notes.txt"} {
		if !MatchAny(docs, p) {
			t.Errorf("%s refused", p)
		}
	}
	for _, p := range []string{"main.go", "src/app.ts", "package.json", ".github/workflows/ci.yml", "Dockerfile", "docsx/a.go", "../docs/x.go"} {
		if MatchAny(docs, p) {
			t.Errorf("%s allowed", p)
		}
	}
}
