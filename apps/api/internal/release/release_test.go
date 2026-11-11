package release

import (
	"strings"
	"testing"

	"wise/apps/api/internal/secretguard"
)

func TestMarkdownReportIsRedactable(t *testing.T) {
	md := Markdown([]byte(`{"summary":"Done","notes":["token ghp_abcdefghijklmnopqrstuvwxyz0123456789 used"]}`))
	if !strings.HasPrefix(md, "### Summary") || !strings.Contains(md, "- token") {
		t.Fatalf("markdown: %s", md)
	}
	if strings.Contains(secretguard.Redact(md), "ghp_abcdefghijklmnopqrstuvwxyz0123456789") {
		t.Fatal("the token survives redaction")
	}
}

func TestCheckTarget(t *testing.T) {
	ok := Request{SHA: strings.Repeat("a", 40), Branch: "wise/x", Remote: "https://github.com/o/r.git", BaseBranch: "main"}
	if err := CheckTarget(ok, "github", "https://github.com"); err != nil {
		t.Fatal(err)
	}
	for name, r := range map[string]Request{
		"main":       {SHA: ok.SHA, Branch: "main", Remote: ok.Remote, BaseBranch: "dev"},
		"prefix":     {SHA: ok.SHA, Branch: "wise/", Remote: ok.Remote, BaseBranch: "main"},
		"base":       {SHA: ok.SHA, Branch: "wise/x", Remote: ok.Remote, BaseBranch: "wise/x"},
		"refspec":    {SHA: ok.SHA, Branch: "wise/x:refs/heads/main", Remote: ok.Remote, BaseBranch: "main"},
		"dots":       {SHA: ok.SHA, Branch: "wise/../main", Remote: ok.Remote, BaseBranch: "main"},
		"short sha":  {SHA: "abc", Branch: "wise/x", Remote: ok.Remote, BaseBranch: "main"},
		"not normal": {SHA: ok.SHA, Branch: "wise/x", Remote: "git@github.com:o/r.git", BaseBranch: "main"},
	} {
		if CheckTarget(r, "github", "https://github.com") == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if CheckTarget(ok, "gitlab", "https://gitlab.com") == nil {
		t.Error("account for another host accepted")
	}
}
