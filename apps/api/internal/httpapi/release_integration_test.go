package httpapi_test

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestRepositoryReleaseSettings(t *testing.T) {
	e := setup(t)
	e.login()
	pid := e.expect(e.do("POST", "/projects", map[string]any{"name": "Rel"}), 201)["id"].(string)
	dir := gitRepo(t, e.repoRoot, "rel-"+uuid.NewString()[:6])
	if out, err := exec.Command("git", "-C", dir, "remote", "add", "origin", "git@github.com:codewisedev/sample.git").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	rid := e.expect(e.do("POST", "/projects/"+pid+"/repositories", map[string]any{"name": "svc", "sourceType": "local", "localPath": dir}), 201)["id"].(string)

	gh := e.expect(e.do("POST", "/accounts", map[string]any{"name": "gh", "kind": "github", "secret": "github_pat_test_0123456789"}), 201)
	if gh["baseUrl"] != "https://github.com" {
		t.Fatalf("github default base url: %v", gh["baseUrl"])
	}
	gl := e.expect(e.do("POST", "/accounts", map[string]any{"name": "gl", "kind": "gitlab", "baseUrl": "https://gitlab.example.org", "secret": "glpat-test-0123456789"}), 201)
	e.expect(e.do("POST", "/accounts", map[string]any{"name": "bad", "kind": "gitlab", "baseUrl": "http://gitlab.example.org", "secret": "x"}), 400)
	if strings.Contains(mustString(gh), "github_pat_test") {
		t.Fatal("the token is returned by the API")
	}

	sug := e.expect(e.do("POST", "/repositories/"+rid+"/release/detect", nil), 200)
	if sug["remote"] != "https://github.com/codewisedev/sample.git" || sug["baseBranch"] != "main" || len(sug["accounts"].([]any)) != 1 || sug["accounts"].([]any)[0] != gh["id"] {
		t.Fatalf("detect: %v", sug)
	}
	// The account must serve the remote's host; the base branch cannot be a wise/ branch.
	e.expect(e.do("PUT", "/repositories/"+rid+"/release", map[string]any{"remote": sug["remote"], "accountId": gl["id"], "baseBranch": "main"}), 400)
	e.expect(e.do("PUT", "/repositories/"+rid+"/release", map[string]any{"remote": sug["remote"], "accountId": gh["id"], "baseBranch": "wise/x"}), 400)
	e.expect(e.do("PUT", "/repositories/"+rid+"/release", map[string]any{"remote": "http://github.com/o/r.git"}), 400)
	saved := e.expect(e.do("PUT", "/repositories/"+rid+"/release", map[string]any{"remote": "git@github.com:codewisedev/sample.git", "accountId": gh["id"], "baseBranch": "main"}), 200)
	if saved["remote"] != "https://github.com/codewisedev/sample.git" {
		t.Fatalf("saved: %v", saved)
	}
	if got := e.expect(e.do("GET", "/repositories/"+rid+"/release", nil), 200); got["accountId"] != gh["id"] {
		t.Fatalf("get: %v", got)
	}

	if p := e.expect(e.do("GET", "/projects/"+pid+"/release-policy", nil), 200); p["pullRequests"] != "ask" || p["draft"] != true {
		t.Fatalf("default policy: %v", p)
	}
	e.expect(e.do("PUT", "/projects/"+pid+"/release-policy", map[string]any{"pullRequests": "always", "draft": true}), 400)
	if p := e.expect(e.do("PUT", "/projects/"+pid+"/release-policy", map[string]any{"pullRequests": "auto", "draft": false}), 200); p["pullRequests"] != "auto" {
		t.Fatalf("policy: %v", p)
	}
}
