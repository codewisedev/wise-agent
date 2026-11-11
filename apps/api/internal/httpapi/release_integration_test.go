package httpapi_test

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"wise/apps/api/internal/db/dbgen"

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
	e.expect(e.runtimeDo(gitWorkerToken, "POST", "/internal/git/pull-requests/"+pg["prId"].(string)+"/result",
		map[string]any{"ok": true, "pr": map[string]any{"number": 7, "url": "https://github.com/codewisedev/sample/pull/7", "state": "open", "draft": true}}), 200)
	rel := e.expect(e.do("GET", "/tasks/"+taskID.String()+"/release", nil), 200)
	pushes, prs := rel["pushes"].([]any), rel["pullRequests"].([]any)
	if len(pushes) != 1 || pushes[0].(map[string]any)["status"] != "pushed" || len(prs) != 1 || prs[0].(map[string]any)["number"] != 7.0 {
		t.Fatalf("task release: %v", rel)
	}
	if strings.Contains(mustString(rel), "github_pat_grant") {
		t.Fatal("the token is in the task release view")
	}
}

func TestRewordNeedsAPendingFinalApproval(t *testing.T) {
	e := setup(t)
	e.login()
	ctx := context.Background()
	taskID, wfID := e.newTask("Reword me")
	var pid string
	_ = e.db.Pool.QueryRow(ctx, "SELECT project_id::text FROM tasks WHERE id = $1", taskID).Scan(&pid)
	dir := gitRepo(t, e.repoRoot, "rw-"+uuid.NewString()[:6])
	rid := e.expect(e.do("POST", "/projects/"+pid+"/repositories", map[string]any{"name": "svc", "sourceType": "local", "localPath": dir}), 201)["id"].(string)
	if _, err := e.db.Pool.Exec(ctx, `INSERT INTO workspaces (id, task_id, workflow_run_id, repository_id, status, branch, image)
		VALUES ($1, $2, $3, $4, 'ready', 'wise/reword-me', 'img')`, uuid.New(), taskID, wfID, rid); err != nil {
		t.Fatal(err)
	}
	cs, _ := json.Marshal(map[string]any{"baseSha": strings.Repeat("a", 40), "headSha": strings.Repeat("b", 40), "branch": "wise/reword-me", "message": "old", "files": []any{}})
	art, err := e.tasks.SaveArtifact(ctx, wfID, taskID, "changeset", cs, nil)
	if err != nil {
		t.Fatal(err)
	}
	e.expect(e.do("POST", "/tasks/"+taskID.String()+"/commit-message", map[string]any{"message": "New message"}), 409)
	pinned, _ := json.Marshal([]map[string]any{{"artifactId": art.ID, "kind": "changeset", "version": art.Version, "hash": art.ContentHash}})
	apID := uuid.New()
	if _, err := e.db.Pool.Exec(ctx, `INSERT INTO approvals (id, workflow_run_id, task_id, gate, status, artifacts, combined_hash)
		VALUES ($1, $2, $3, 'gate', 'pending', $4, 'h')`, apID, wfID, taskID, pinned); err != nil {
		t.Fatal(err)
	}
	r := e.expect(e.do("POST", "/tasks/"+taskID.String()+"/commit-message", map[string]any{"message": "New message"}), 200)
	if r["headSha"] != strings.Repeat("c", 40) || r["message"] != "New message" {
		t.Fatalf("reword: %v", r)
	}
	var status string
	_ = e.db.Pool.QueryRow(ctx, "SELECT status FROM approvals WHERE id = $1", apID).Scan(&status)
	if status != "superseded" {
		t.Fatalf("the approval of the old commit is %s, want superseded", status)
	}
}

func TestPrepareReleaseNeedsTheRepositoryTarget(t *testing.T) {
	e := setup(t)
	e.login()
	ctx := context.Background()
	taskID, wfID := e.newTask("Prepare release")
	var pid string
	_ = e.db.Pool.QueryRow(ctx, "SELECT project_id::text FROM tasks WHERE id = $1", taskID).Scan(&pid)
	dir := gitRepo(t, e.repoRoot, "prep-"+uuid.NewString()[:6])
	rid := e.expect(e.do("POST", "/projects/"+pid+"/repositories", map[string]any{"name": "svc", "sourceType": "local", "localPath": dir}), 201)["id"].(string)
	sha := strings.Repeat("ef", 20)
	if _, err := e.db.Pool.Exec(ctx, `INSERT INTO workspaces (id, task_id, workflow_run_id, repository_id, status, branch, image, delivered_branch, delivered_sha)
		VALUES ($1, $2, $3, $4, 'stopped', 'wise/prepare-release', 'img', 'wise/prepare-release', $5)`, uuid.New(), taskID, wfID, rid, sha); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{"branch": "wise/prepare-release", "sha": sha})
	if _, err := e.tasks.SaveArtifact(ctx, wfID, taskID, "delivery", b, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := e.release.Prepare(ctx, taskID); err == nil || !strings.Contains(err.Error(), "set where") {
		t.Fatalf("unconfigured: %v", err)
	}
	gh := e.expect(e.do("POST", "/accounts", map[string]any{"name": "gh3", "kind": "github", "secret": "github_pat_prep_0123456789"}), 201)
	e.expect(e.do("PUT", "/repositories/"+rid+"/release", map[string]any{"remote": "https://github.com/o/r.git", "accountId": gh["id"], "baseBranch": "main"}), 200)
	p, err := e.release.Prepare(ctx, taskID)
	if err != nil || p.Request.SHA != sha || p.Request.Branch != "wise/prepare-release" || p.Request.AccountName != "gh3" {
		t.Fatalf("prepared: %+v %v", p, err)
	}
	// Nothing delivered: nothing to push.
	b, _ = json.Marshal(map[string]any{"note": "no changes to deliver"})
	if _, err := e.tasks.SaveArtifact(ctx, wfID, taskID, "delivery", b, nil); err != nil {
		t.Fatal(err)
	}
	if p, err := e.release.Prepare(ctx, taskID); err != nil || p.Skip == "" {
		t.Fatalf("skip: %+v %v", p, err)
	}
}

func TestReleaseDecisionsAreNotMemoryMaterial(t *testing.T) {
	e := setup(t)
	e.login()
	taskID, _ := e.newTask("No memory from pushes")
	comment := "Not pushing now, the release window is closed."
	for _, kind := range []string{"release_request", "push"} {
		pinned, _ := json.Marshal([]map[string]any{{"kind": kind}})
		e.memory.OnApprovalDecided(context.Background(), dbgen.Approval{ID: uuid.New(), TaskID: taskID, Status: "rejected", Comment: &comment, Artifacts: pinned})
	}
	if r := e.runtimeDo(runtimeToken, "POST", "/runtime/v1/runs/claim", map[string]string{"runtimeId": "test-rt"}); r.status != 204 {
		t.Fatalf("a memory extraction was queued for a release decision: %d %v", r.status, r.body)
	}
}
