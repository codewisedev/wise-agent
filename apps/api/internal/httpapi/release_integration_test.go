package httpapi_test

import (
	"context"
	"encoding/json"
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

func TestPushGrantsOnlyForApprovedDeliveredCommits(t *testing.T) {
	e := setup(t)
	e.login()
	ctx := context.Background()
	taskID, wfID := e.newTask("Release me")
	var pid, rid string
	if err := e.db.Pool.QueryRow(ctx, "SELECT project_id::text FROM tasks WHERE id = $1", taskID).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	dir := gitRepo(t, e.repoRoot, "grant-"+uuid.NewString()[:6])
	rid = e.expect(e.do("POST", "/projects/"+pid+"/repositories", map[string]any{"name": "svc", "sourceType": "local", "localPath": dir}), 201)["id"].(string)
	gh := e.expect(e.do("POST", "/accounts", map[string]any{"name": "gh2", "kind": "github", "secret": "github_pat_grant_0123456789"}), 201)
	sha := strings.Repeat("ab", 20)
	wsID := uuid.New()
	if _, err := e.db.Pool.Exec(ctx, `INSERT INTO workspaces (id, task_id, workflow_run_id, repository_id, status, branch, image, delivered_branch, delivered_sha)
		VALUES ($1, $2, $3, $4, 'stopped', 'wise/release-me', 'img', 'wise/release-me', $5)`, wsID, taskID, wfID, rid, sha); err != nil {
		t.Fatal(err)
	}
	round := 0
	request := func(branch, commit string) string {
		round++
		b, _ := json.Marshal(map[string]any{"workspaceId": wsID, "repositoryId": rid, "sha": commit, "branch": branch,
			"remote": "https://github.com/codewisedev/sample.git", "accountId": gh["id"], "baseBranch": "main"})
		a, err := e.tasks.SaveArtifact(ctx, wfID, taskID, "release_request", b, nil)
		if err != nil {
			t.Fatal(err)
		}
		apID := uuid.New()
		pinned, _ := json.Marshal([]map[string]any{{"artifactId": a.ID, "kind": "release_request", "version": a.Version, "hash": a.ContentHash}})
		if _, err := e.db.Pool.Exec(ctx, `INSERT INTO approvals (id, workflow_run_id, task_id, gate, round, status, artifacts, combined_hash)
			VALUES ($1, $2, $3, 'release', $5, 'pending', $4, 'h')`, apID, wfID, taskID, pinned, round); err != nil {
			t.Fatal(err)
		}
		return apID.String()
	}
	approve := func(id string) {
		if _, err := e.db.Pool.Exec(ctx, "UPDATE approvals SET status = 'approved', decided_at = now() WHERE id = $1", id); err != nil {
			t.Fatal(err)
		}
	}
	grant := func(tok, id string) resp { return e.runtimeDo(tok, "POST", "/internal/git/push-grants/"+id, nil) }

	ap := request("wise/release-me", sha)
	if r := grant("", ap); r.status != 401 {
		t.Fatalf("no token: %d", r.status)
	}
	if r := grant(runtimeToken, ap); r.status != 401 {
		t.Fatalf("runtime token accepted: %d", r.status)
	}
	if r := e.do("POST", "/internal/git/push-grants/"+ap, nil); r.status != 401 {
		t.Fatalf("a user session reached the git route: %d", r.status)
	}
	if r := grant(gitWorkerToken, ap); r.status != 409 {
		t.Fatalf("pending approval granted: %d %v", r.status, r.body)
	}
	approve(ap)
	g := e.expect(grant(gitWorkerToken, ap), 200)
	if g["token"] != "github_pat_grant_0123456789" || g["sha"] != sha || g["branch"] != "wise/release-me" {
		t.Fatalf("grant: %v", g)
	}
	e.expect(grant(gitWorkerToken, ap), 409) // in progress
	e.expect(e.runtimeDo(gitWorkerToken, "POST", "/internal/git/pushes/"+g["pushId"].(string)+"/result", map[string]any{"ok": true}), 200)
	if r := grant(gitWorkerToken, ap); r.status != 409 || !strings.Contains(mustString(r.body), "already pushed") {
		t.Fatalf("second push of the same approval: %d %v", r.status, r.body)
	}
	// Approved, but not what was delivered, or not a wise/ branch: refused.
	other := request("wise/release-me", strings.Repeat("cd", 20))
	approve(other)
	e.expect(grant(gitWorkerToken, other), 409)
	mainAp := request("main", sha)
	approve(mainAp)
	e.expect(grant(gitWorkerToken, mainAp), 409)
	// Policy "off": no pull request even after a push.
	e.expect(e.do("PUT", "/projects/"+pid+"/release-policy", map[string]any{"pullRequests": "off", "draft": true}), 200)
	e.expect(e.runtimeDo(gitWorkerToken, "POST", "/internal/git/pr-grants/"+g["pushId"].(string), nil), 409)
	// "ask" needs an approved pull request approval; "auto" grants.
	e.expect(e.do("PUT", "/projects/"+pid+"/release-policy", map[string]any{"pullRequests": "ask", "draft": true}), 200)
	e.expect(e.runtimeDo(gitWorkerToken, "POST", "/internal/git/pr-grants/"+g["pushId"].(string), nil), 409)
	e.expect(e.do("PUT", "/projects/"+pid+"/release-policy", map[string]any{"pullRequests": "auto", "draft": true}), 200)
	pg := e.expect(e.runtimeDo(gitWorkerToken, "POST", "/internal/git/pr-grants/"+g["pushId"].(string), nil), 200)
	if pg["draft"] != true || pg["title"] != "Release me" || pg["baseBranch"] != "main" {
		t.Fatalf("pr grant: %v", pg)
	}
}
