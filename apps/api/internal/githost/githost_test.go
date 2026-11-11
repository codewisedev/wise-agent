package githost

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseRemote(t *testing.T) {
	ok := map[string]string{
		"git@github.com:codewisedev/wise-agent.git":       "https://github.com/codewisedev/wise-agent.git",
		"https://github.com/codewisedev/wise-agent":       "https://github.com/codewisedev/wise-agent.git",
		"https://user:secret@github.com/o/r.git":          "https://github.com/o/r.git",
		"ssh://git@gitlab.example.org/group/sub/repo.git": "https://gitlab.example.org/group/sub/repo.git",
		"https://gitlab.example.org:8443/g/r.git":         "https://gitlab.example.org:8443/g/r.git",
	}
	for in, want := range ok {
		r, err := ParseRemote(in)
		if err != nil || r.URL() != want {
			t.Errorf("%s → %s %v, want %s", in, r.URL(), err, want)
		}
	}
	for _, bad := range []string{"http://github.com/o/r.git", "/local/path", "https://github.com/onlyowner", "git@github.com:o/../r.git", ""} {
		if _, err := ParseRemote(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	r, _ := ParseRemote("git@github.com:o/r.git")
	if !Matches(GitHub, "", r) || Matches(GitLab, "", r) || Matches(GitHub, "https://ghe.example.org", r) {
		t.Error("host matching")
	}
}

func TestGitHubPRFlow(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(401)
			return
		}
		switch {
		case r.Method == "POST" && r.URL.Path == "/repos/o/r/pulls":
			_ = json.NewDecoder(r.Body).Decode(&got)
			_, _ = w.Write([]byte(`{"number":7,"html_url":"https://github.com/o/r/pull/7","state":"open","draft":true,"title":"T"}`))
		case r.URL.Path == "/repos/o/r/pulls/7":
			_, _ = w.Write([]byte(`{"number":7,"state":"closed","merged_at":"2026-11-11T00:00:00Z"}`))
		case r.URL.Path == "/repos/o/r":
			_, _ = w.Write([]byte(`{"default_branch":"main"}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	c := &Client{Kind: GitHub, Token: "tok", APIBase: srv.URL}
	r, _ := ParseRemote("git@github.com:o/r.git")
	ctx := context.Background()
	if b, err := c.CheckRepo(ctx, r); err != nil || b != "main" {
		t.Fatalf("check: %s %v", b, err)
	}
	pr, err := c.CreatePR(ctx, r, PRInput{Title: "T", Body: "B", Head: "wise/x", Base: "main", Draft: true})
	if err != nil || pr.Number != 7 || !pr.Draft || got["draft"] != true || got["head"] != "wise/x" {
		t.Fatalf("create: %+v %v %v", pr, err, got)
	}
	if p, _ := c.GetPR(ctx, r, 7); p.State != "merged" {
		t.Fatalf("merged state: %+v", p)
	}
	bad := &Client{Kind: GitHub, Token: "nope", APIBase: srv.URL}
	if _, err := bad.CheckRepo(ctx, r); !errors.Is(err, ErrAuth) || strings.Contains(err.Error(), "nope") {
		t.Fatalf("auth: %v", err)
	}
}

func TestGitLabDraftTitle(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") != "tok" {
			w.WriteHeader(401)
			return
		}
		if r.Method == "POST" && strings.HasSuffix(r.URL.EscapedPath(), "/projects/g%2Fsub%2Fr/merge_requests") {
			_ = json.NewDecoder(r.Body).Decode(&got)
			_, _ = w.Write([]byte(`{"iid":3,"web_url":"https://gitlab.com/g/sub/r/-/merge_requests/3","state":"opened","title":"Draft: T"}`))
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()
	c := &Client{Kind: GitLab, Token: "tok", APIBase: srv.URL}
	r, _ := ParseRemote("https://gitlab.com/g/sub/r.git")
	pr, err := c.CreatePR(context.Background(), r, PRInput{Title: "T", Head: "wise/x", Base: "main", Draft: true})
	if err != nil || pr.State != "open" || got["title"] != "Draft: T" || got["source_branch"] != "wise/x" {
		t.Fatalf("%+v %v %v", pr, err, got)
	}
}
