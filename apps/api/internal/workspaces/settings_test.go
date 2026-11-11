package workspaces

import "testing"

func TestMatchAnyDocumentationPaths(t *testing.T) {
	docs := []string{"docs/**", "doc/**", "*.md", "*.mdx", "*.rst", "*.adoc", "CHANGELOG*", "README*"}
	for _, p := range []string{"README.md", "docs/setup/install.md", "docs/img/arch.png", "pkg/api/README.md", "CHANGELOG", "docs/notes.txt"} {
		if !MatchAny(docs, p) {
			t.Errorf("%s refused", p)
		}
	}
	for _, p := range []string{"main.go", "src/app.ts", "package.json", ".github/workflows/ci.yml", "Dockerfile", "docsx/a.go", "../docs/x.go",
		"requirements.txt", "CMakeLists.txt", "constraints.txt"} {
		if MatchAny(docs, p) {
			t.Errorf("%s allowed", p)
		}
	}
}
