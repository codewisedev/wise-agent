package httpapi_test

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestProjectActivityFeed(t *testing.T) {
	e := setup(t)
	e.login()
	ctx := context.Background()
	taskID, wfID := e.newTask("Feed me")
	var pid string
	_ = e.db.Pool.QueryRow(ctx, "SELECT project_id::text FROM tasks WHERE id = $1", taskID).Scan(&pid)
	q := func(sql string, args ...any) {
		t.Helper()
		if _, err := e.db.Pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	q(`INSERT INTO approvals (id, workflow_run_id, task_id, gate, status, artifacts, combined_hash, comment, decided_at, created_at)
	   VALUES ($1, $2, $3, 'requirements', 'rejected', '[]', 'h', 'needs audit logging', now() + interval '1 second', now())`, uuid.New(), wfID, taskID)
	run := e.createRunInProject("product-agent", pid)
	q(`UPDATE agent_runs SET status = 'failed', error = 'model unreachable', finished_at = now() + interval '2 seconds' WHERE id = $1`, run)
	b, _ := json.Marshal(map[string]any{"branch": "wise/feed-me", "sha": strings.Repeat("a", 40)})
	if _, err := e.tasks.SaveArtifact(ctx, wfID, taskID, "delivery", b, nil); err != nil {
		t.Fatal(err)
	}
	rid := e.expect(e.do("POST", "/projects/"+pid+"/repositories", map[string]any{"name": "svc", "sourceType": "local", "localPath": gitRepo(t, e.repoRoot, "feed-"+uuid.NewString()[:6])}), 201)["id"].(string)
	apID := uuid.New()
	q(`INSERT INTO approvals (id, workflow_run_id, task_id, gate, round, status, artifacts, combined_hash) VALUES ($1, $2, $3, 'release', 1, 'approved', '[]', 'h2')`, apID, wfID, taskID)
	q(`INSERT INTO git_pushes (id, approval_id, task_id, workspace_id, repository_id, remote, branch, sha, base_branch, status, pushed_at)
	   VALUES ($1, $2, $3, $4, $5, 'https://github.com/o/r.git', 'wise/feed-me', $6, 'main', 'pushed', now() + interval '3 seconds')`,
		uuid.New(), apID, taskID, uuid.New(), rid, strings.Repeat("a", 40))

	items := e.expect(e.do("GET", "/projects/"+pid+"/activity", nil), 200)["items"].([]any)
	kinds := []string{}
	for _, it := range items {
		kinds = append(kinds, it.(map[string]any)["kind"].(string))
	}
	joined := strings.Join(kinds, ",")
	for _, want := range []string{"branch.pushed", "run.failed", "approval.rejected", "branch.delivered", "task.created"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %s in %s", want, joined)
		}
	}
	if kinds[0] != "branch.pushed" {
		t.Fatalf("newest first: %v", kinds)
	}
	first := items[0].(map[string]any)
	if first["url"] != "https://github.com/o/r/tree/wise/feed-me" || first["taskId"] != taskID.String() {
		t.Fatalf("push item: %v", first)
	}
	only := e.expect(e.do("GET", "/projects/"+pid+"/activity?kinds=approval", nil), 200)["items"].([]any)
	for _, it := range only {
		if !strings.HasPrefix(it.(map[string]any)["kind"].(string), "approval.") {
			t.Fatalf("filter: %v", it)
		}
	}
	page := e.expect(e.do("GET", "/projects/"+pid+"/activity?limit=2", nil), 200)
	if len(page["items"].([]any)) != 2 || page["next"] == nil {
		t.Fatalf("paging: %v", page)
	}
	rest := e.expect(e.do("GET", "/projects/"+pid+"/activity?before="+url.QueryEscape(page["next"].(string)), nil), 200)["items"].([]any)
	if len(rest) != len(items)-2 {
		t.Fatalf("second page: %d of %d", len(rest), len(items))
	}
}
