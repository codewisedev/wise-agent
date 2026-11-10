package httpapi_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"wise/apps/api/internal/agents"
	"wise/apps/api/internal/executor"
	"wise/apps/api/internal/modelproxy"
	"wise/apps/api/internal/workflow"
)

// fakeExecutor stands in for the executor service.
type fakeExecutor struct {
	mu         sync.Mutex
	prepared   []executor.PrepareRequest
	removed    []string
	egress     []string
	fail       string
	fsCalls    []map[string]any
	commands   []string
	timeouts   []int
	deliveries []string
	runners    []executor.RunnerRequest
	runnerOut  string // the CLI's stdout for the next runner job
	cancelled  []string
}

func (f *fakeExecutor) Runner(_ context.Context, id string, req executor.RunnerRequest) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runners = append(f.runners, req)
	return "runner-job", nil
}

func (f *fakeExecutor) Job(_ context.Context, jobID string, offset int64, wait int) (executor.JobStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.runnerOut
	if offset >= int64(len(out)) {
		out = ""
	} else {
		out = out[offset:]
	}
	return executor.JobStatus{ID: jobID, Status: executor.JobSucceeded, Output: out, Offset: offset, NextOffset: offset + int64(len(out)),
		Result: executor.RunnerResult{ExitCode: 0}}, nil
}

func (f *fakeExecutor) CancelJob(_ context.Context, jobID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelled = append(f.cancelled, jobID)
	return nil
}

// fakeProxy stands in for the Runtime Proxy.
type fakeProxy struct {
	mu         sync.Mutex
	registered map[string]modelproxy.Run
	revoked    []string
}

func (p *fakeProxy) Register(_ context.Context, token string, run modelproxy.Run) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.registered == nil {
		p.registered = map[string]modelproxy.Run{}
	}
	p.registered[token] = run
	return nil
}

func (p *fakeProxy) Revoke(_ context.Context, token string) (modelproxy.Usage, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.revoked = append(p.revoked, token)
	return modelproxy.Usage{InputTokens: 900, OutputTokens: 100, CostUSD: 0.0042,
		Calls: []modelproxy.Call{{Model: "claude-sonnet", Status: 200, InputTokens: 900, OutputTokens: 100, CostUSD: 0.0042}}}, nil
}

func (f *fakeExecutor) Prepare(_ context.Context, req executor.PrepareRequest) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prepared = append(f.prepared, req)
	return "job-" + req.ID[:8], nil
}

func (f *fakeExecutor) Wait(_ context.Context, jobID string, onOutput func(string), beat func()) (executor.JobStatus, error) {
	if jobID == "cmd-1" {
		onOutput("ok  example/pkg 0.01s\n")
		code := 0
		return executor.JobStatus{ID: jobID, Status: executor.JobSucceeded, ExitCode: &code}, nil
	}
	f.mu.Lock()
	fail := f.fail
	req := f.prepared[len(f.prepared)-1]
	f.mu.Unlock()
	onOutput("cloning …\n✓ workspace ready\n")
	if fail != "" {
		return executor.JobStatus{ID: jobID, Status: executor.JobFailed, Error: fail}, nil
	}
	base := req.Base
	if base == "" {
		base = "main"
	}
	return executor.JobStatus{ID: jobID, Status: executor.JobSucceeded, Result: executor.State{
		ID: req.ID, Branch: req.Branch, Base: base, BaseSHA: strings.Repeat("a", 40), Prepared: true,
		Tools: []string{"git: git version 2.52.0"}, Scrubbed: []string{".env"},
	}}, nil
}

func (f *fakeExecutor) Remove(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, id)
	return nil
}
func (f *fakeExecutor) Stop(context.Context, string) error { return nil }

func (f *fakeExecutor) FS(_ context.Context, id string, req any) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fsCalls = append(f.fsCalls, req.(map[string]any))
	return json.RawMessage(`{"content":"hello\n","totalLines":1}`), nil
}

func (f *fakeExecutor) Command(_ context.Context, id, command string, timeoutS int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commands = append(f.commands, command)
	f.timeouts = append(f.timeouts, timeoutS)
	return "cmd-1", nil
}

func (f *fakeExecutor) Diff(context.Context, string, int) (executor.WorkingDiff, error) {
	return executor.WorkingDiff{Stat: " a.go | 1 +", Patch: "+x"}, nil
}

func (f *fakeExecutor) Snapshot(_ context.Context, id string, req executor.SnapshotRequest) (executor.Changeset, error) {
	return executor.Changeset{BaseSHA: strings.Repeat("a", 40), HeadSHA: strings.Repeat("b", 40), Branch: "wise/x", Message: req.Message}, nil
}

func (f *fakeExecutor) Deliver(_ context.Context, id string, req executor.DeliverRequest) (executor.Delivery, error) {
	f.mu.Lock()
	f.deliveries = append(f.deliveries, req.SHA)
	f.mu.Unlock()
	return executor.Delivery{Branch: "wise/x", SHA: req.SHA, RepoPath: "/repo"}, nil
}
func (f *fakeExecutor) SetEgress(_ context.Context, hosts []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.egress = hosts
	return nil
}

func TestWorkspaceLifecycle(t *testing.T) {
	e := setup(t)
	e.login()
	dir := filepath.Join(e.repoRoot, "shop")
	makeRepo(t, dir, map[string]string{
		"package.json":      `{"name":"shop","scripts":{"test":"vitest run","lint":"eslint .","typecheck":"tsc --noEmit"}}`,
		"package-lock.json": "{}",
	})
	p := e.do("POST", "/projects", map[string]any{"name": "Shop"})
	pid := p.body["id"].(string)
	r := e.do("POST", "/projects/"+pid+"/repositories", map[string]any{"name": "shop", "sourceType": "local", "localPath": dir, "role": "backend"})
	if r.status != 201 {
		t.Fatalf("repo: %d %v", r.status, r.body)
	}
	rid := r.body["id"].(string)
	task := e.expect(e.do("POST", "/projects/"+pid+"/tasks", map[string]any{"title": "Add CSV export!", "description": "Export orders as CSV", "start": false}), 201)
	tid := task["task"].(map[string]any)["id"].(string)

	// Not set up yet.
	w := e.do("GET", "/tasks/"+tid+"/workspace", nil)
	if w.status != 200 || !strings.Contains(w.body["problem"].(string), "no execution settings") {
		t.Fatalf("workspace before setup: %d %v", w.status, w.body)
	}
	if r := e.do("POST", "/tasks/"+tid+"/workspace", nil); r.status != 409 {
		t.Fatalf("prepare without settings: %d %v", r.status, r.body)
	}

	// Detect suggests Node settings from package.json and the lock file.
	d := e.do("POST", "/repositories/"+rid+"/execution/detect", nil)
	st := d.body["settings"].(map[string]any)
	cmds := st["commands"].(map[string]any)
	if d.status != 200 || st["image"] != "wise-toolchain-node:22" || st["setup"].([]any)[0] != "npm ci" ||
		cmds["test"] != "npm run test" || cmds["lint"] != "npm run lint" || cmds["typecheck"] != "npm run typecheck" {
		t.Fatalf("detect: %d %v", d.status, d.body)
	}

	// Validation, then save.
	bad := map[string]any{"image": "Bad Image!", "setup": []string{}, "commands": cmds, "tools": []string{"n p m"}, "baseBranch": "a..b", "env": map[string]string{"PATH": "/x"}}
	if r := e.do("PUT", "/repositories/"+rid+"/execution", bad); r.status != 400 || len(r.body["errors"].([]any)) != 4 {
		t.Fatalf("invalid settings: %d %v", r.status, r.body)
	}
	st["env"] = map[string]string{"NODE_ENV": "test"}
	if r := e.do("PUT", "/repositories/"+rid+"/execution", st); r.status != 200 {
		t.Fatalf("save: %d %v", r.status, r.body)
	}
	if got := e.do("GET", "/repositories/"+rid+"/execution", nil); got.body["env"].(map[string]any)["NODE_ENV"] != "test" {
		t.Fatalf("saved: %v", got.body)
	}

	// Prepare: a branch named after the task, the settings passed through.
	r = e.do("POST", "/tasks/"+tid+"/workspace", nil)
	if r.status != 202 || !strings.HasPrefix(r.body["branch"].(string), "wise/add-csv-export-") {
		t.Fatalf("prepare: %d %v", r.status, r.body)
	}
	var ready map[string]any
	for i := 0; i < 50; i++ {
		ready = e.do("GET", "/tasks/"+tid+"/workspace", nil).body["workspace"].(map[string]any)
		if ready["status"] != "preparing" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if ready["status"] != "ready" || ready["baseSha"] != strings.Repeat("a", 40) || ready["scrubbed"].([]any)[0] != ".env" ||
		!strings.Contains(ready["log"].(string), "workspace ready") {
		t.Fatalf("ready: %v", ready)
	}
	e.exec.mu.Lock()
	req := e.exec.prepared[0]
	egress := e.exec.egress
	e.exec.mu.Unlock()
	if req.RepoPath != dir || req.Image != "wise-toolchain-node:22" || req.Env["NODE_ENV"] != "test" || req.Limits.MemoryMB != 4096 ||
		len(req.Setup) != 1 || len(egress) == 0 {
		t.Fatalf("prepare request: %+v egress %v", req, egress)
	}
	// Preparing again reuses the ready workspace.
	if r := e.do("POST", "/tasks/"+tid+"/workspace", nil); r.body["id"] != ready["id"] || len(e.exec.prepared) != 1 {
		t.Fatalf("reuse: %v", r.body)
	}

	// The task's repository must be a local repository of its project.
	other := e.do("POST", "/projects", map[string]any{"name": "Other"})
	odir := filepath.Join(e.repoRoot, "other")
	makeRepo(t, odir, map[string]string{"a.txt": "a"})
	or := e.do("POST", "/projects/"+other.body["id"].(string)+"/repositories", map[string]any{"name": "other", "sourceType": "local", "localPath": odir})
	if r := e.do("PATCH", "/tasks/"+tid, map[string]any{"repository": or.body["id"]}); r.status != 400 {
		t.Fatalf("foreign repository: %d %v", r.status, r.body)
	}
	if r := e.do("PATCH", "/tasks/"+tid, map[string]any{"repository": rid}); r.status != 200 || r.body["targetRepositoryId"] != rid {
		t.Fatalf("set repository: %d %v", r.status, r.body)
	}

	// Remove.
	if r := e.do("DELETE", "/tasks/"+tid+"/workspace", nil); r.status != 204 {
		t.Fatalf("remove: %d %v", r.status, r.body)
	}
	if w := e.do("GET", "/tasks/"+tid+"/workspace", nil).body["workspace"].(map[string]any); w["status"] != "removed" {
		t.Fatalf("after remove: %v", w)
	}
	var n int
	if err := e.db.Pool.QueryRow(context.Background(), "SELECT count(*) FROM audit_logs WHERE action IN ('repository.execution_updated','workspace.prepare','workspace.removed','task.repository_set')").Scan(&n); err != nil || n != 5 {
		t.Fatalf("audit entries: %d %v", n, err)
	}

	// A failed preparation is recorded with its error.
	e.exec.mu.Lock()
	e.exec.fail = "preflight: \"npm\" is not available in image"
	e.exec.mu.Unlock()
	e.do("POST", "/tasks/"+tid+"/workspace", nil)
	var failed map[string]any
	for i := 0; i < 50; i++ {
		failed = e.do("GET", "/tasks/"+tid+"/workspace", nil).body["workspace"].(map[string]any)
		if failed["status"] != "preparing" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if failed["status"] != "failed" || !strings.Contains(failed["error"].(string), "preflight") {
		t.Fatalf("failed: %v", failed)
	}
}

// makeRepo creates a git repository with files and one commit.
func makeRepo(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "-A"}, {"commit", "-q", "-m", "init"}} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t", "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
}

// workspaceTask creates a project with a configured local repository, a task
// and its ready workspace.
func (e *env) workspaceTask(t *testing.T) (taskID, workspaceID string) {
	t.Helper()
	dir := filepath.Join(e.repoRoot, "svc")
	makeRepo(t, dir, map[string]string{"go.mod": "module svc\n\ngo 1.26\n"})
	pid := e.expect(e.do("POST", "/projects", map[string]any{"name": "Svc"}), 201)["id"].(string)
	rid := e.expect(e.do("POST", "/projects/"+pid+"/repositories", map[string]any{"name": "svc", "sourceType": "local", "localPath": dir}), 201)["id"].(string)
	e.expect(e.do("PUT", "/repositories/"+rid+"/execution", map[string]any{"image": "wise-toolchain-go:1.26", "setup": []string{},
		"commands": map[string]string{"format": "", "lint": "", "typecheck": "", "test": "go test ./..."}, "tools": []string{"go"}, "baseBranch": "", "env": map[string]string{}}), 200)
	taskID = e.expect(e.do("POST", "/projects/"+pid+"/tasks", map[string]any{"title": "Add health check", "start": false}), 201)["task"].(map[string]any)["id"].(string)
	e.expect(e.do("POST", "/tasks/"+taskID+"/workspace", nil), 202)
	for i := 0; i < 50; i++ {
		w := e.do("GET", "/tasks/"+taskID+"/workspace", nil).body["workspace"].(map[string]any)
		if w["status"] == "ready" {
			return taskID, w["id"].(string)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("workspace not ready")
	return "", ""
}

func TestRuntimeWorkspaceTools(t *testing.T) {
	e := setup(t)
	e.login()
	taskID, _ := e.workspaceTask(t)
	tid := uuid.MustParse(taskID)
	wfID, _ := uuid.NewV7()
	if _, err := e.db.Pool.Exec(context.Background(), "INSERT INTO workflow_runs (id, task_id, workflow_type, status) VALUES ($1, $2, 'pipeline', 'running')", wfID, tid); err != nil {
		t.Fatal(err)
	}
	// A read-only agent in the task's workflow: it may read the workspace, not change it.
	if _, err := e.agents.Enqueue(context.Background(), "product-agent", map[string]any{"request": "Add a health check endpoint"}, agents.RunOptions{WorkflowRunID: &wfID}); err != nil {
		t.Fatal(err)
	}
	c := e.expect(e.runtimeDo(runtimeToken, "POST", "/runtime/v1/runs/claim", map[string]string{"runtimeId": "rt"}), 200)
	if c["workspace"] != true {
		t.Fatalf("claim should report the workspace: %v", c["workspace"])
	}
	runID, tok := c["run"].(map[string]any)["id"].(string), c["runToken"].(string)
	base := "/runtime/v1/runs/" + runID + "/workspace/"
	if r := e.expect(e.runtimeDo(tok, "POST", base+"fs", map[string]any{"op": "read", "path": "go.mod"}), 200); r["content"] != "hello\n" {
		t.Fatalf("read: %v", r)
	}
	e.expect(e.runtimeDo(tok, "POST", base+"fs", map[string]any{"op": "write", "path": "a.go", "content": "x"}), 403)
	e.expect(e.runtimeDo(tok, "POST", base+"fs", map[string]any{"op": "chmod", "path": "a.go"}), 400)
	e.expect(e.runtimeDo(tok, "POST", base+"command", map[string]any{"command": "go test ./..."}), 403)
	if r := e.expect(e.runtimeDo(tok, "GET", base+"diff", nil), 200); r["patch"] != "+x" {
		t.Fatalf("diff: %v", r)
	}
	e.exec.mu.Lock()
	n := len(e.exec.fsCalls)
	maxBytes := e.exec.fsCalls[0]["maxBytes"]
	e.exec.mu.Unlock()
	if n != 1 || maxBytes != float64(64<<10) && maxBytes != 64<<10 {
		t.Fatalf("fs calls %d, maxBytes %v", n, maxBytes)
	}

	// Give the agent write and execute tools (as the coding agents have).
	if _, err := e.db.Pool.Exec(context.Background(), `UPDATE agent_definitions SET definition = jsonb_set(definition, '{tools}', '["ws_read_file","ws_write_file","ws_run_command"]') WHERE name = 'product-agent'`); err != nil {
		t.Fatal(err)
	}
	e.expect(e.runtimeDo(tok, "POST", base+"fs", map[string]any{"op": "write", "path": "a.go", "content": "x"}), 200)
	r := e.expect(e.runtimeDo(tok, "POST", base+"command", map[string]any{"command": "go test ./...", "timeoutS": 3600}), 200)
	if r["exitCode"] != float64(0) || !strings.Contains(r["output"].(string), "ok  example/pkg") {
		t.Fatalf("command: %v", r)
	}
	e.exec.mu.Lock()
	timeout := e.exec.timeouts[0]
	e.exec.mu.Unlock()
	if timeout != 600 {
		t.Fatalf("timeout should be capped at the setting: %d", timeout)
	}

	// A run outside any workflow has no workspace.
	pid := e.expect(e.do("POST", "/projects", map[string]any{"name": "Loose"}), 201)["id"].(string)
	e.createRunInProject("product-agent", pid)
	c2 := e.expect(e.runtimeDo(runtimeToken, "POST", "/runtime/v1/runs/claim", map[string]string{"runtimeId": "rt"}), 200)
	if c2["workspace"] == true {
		t.Fatal("a run without a workflow has no workspace")
	}
	e.expect(e.runtimeDo(c2["runToken"].(string), "POST", "/runtime/v1/runs/"+c2["run"].(map[string]any)["id"].(string)+"/workspace/fs", map[string]any{"op": "list"}), 409)
}

func TestGateEvaluationAndDeliveryNeedTheExactApprovedChangeset(t *testing.T) {
	e := setup(t)
	e.login()
	ctx := context.Background()
	taskID, _ := e.workspaceTask(t)
	tid := uuid.MustParse(taskID)
	runID, _ := uuid.NewV7()
	if _, err := e.db.Pool.Exec(ctx, "INSERT INTO workflow_runs (id, task_id, workflow_type, status) VALUES ($1, $2, 'PipelineWorkflow', 'running')", runID, tid); err != nil {
		t.Fatal(err)
	}
	acts := &workflow.Activities{Tasks: e.tasks, Agents: e.agents, Workspaces: e.ws}

	// The gate reads real check and review artifacts.
	e.saveArtifact(runID, tid, "quality_report", map[string]any{"passed": false, "checks": []map[string]any{
		{"name": "test", "command": "go test ./...", "status": "failed", "exitCode": 1, "durationMs": 900, "output": "--- FAIL: TestHealth\nexpected 200, got 500"},
		{"name": "lint", "command": "go vet ./...", "status": "passed", "exitCode": 0, "durationMs": 300, "output": ""},
	}})
	e.saveArtifact(runID, tid, "code_review", map[string]any{"verdict": "changes_requested", "summary": "s", "findings": []map[string]any{
		{"severity": "major", "file": "health.go", "line": 12, "title": "Handler ignores DB errors", "detail": "a down DB returns 200", "suggestion": "return 503"},
		{"severity": "minor", "file": "health.go", "title": "Naming", "detail": "nit"},
	}})
	req := workflow.EngRequest{WorkflowRunID: runID, TaskID: tid, Stage: "gate", Step: 1, Kinds: []string{"quality_report", "code_review"}}
	g, err := acts.EvaluateGate(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if g.Passed || g.MaxRounds != 3 || !strings.Contains(g.Feedback, "expected 200, got 500") || !strings.Contains(g.Feedback, "Handler ignores DB errors") ||
		strings.Contains(g.Feedback, "Naming") || !strings.Contains(g.Summary, "1 of 2 checks failed") {
		t.Fatalf("failing gate: %+v", g)
	}
	var status string
	if err := e.db.Pool.QueryRow(ctx, "SELECT status FROM workflow_steps WHERE workflow_run_id = $1 AND stage = 'gate' AND attempt = 1", runID).Scan(&status); err != nil || status != "failed" {
		t.Fatalf("gate step row: %q %v", status, err)
	}
	e.saveArtifact(runID, tid, "quality_report", map[string]any{"passed": true, "checks": []map[string]any{{"name": "test", "command": "go test ./...", "status": "passed", "exitCode": 0, "durationMs": 1, "output": ""}}})
	e.saveArtifact(runID, tid, "code_review", map[string]any{"verdict": "pass", "summary": "ok", "findings": []any{}})
	req.Step = 2
	if g, err := acts.EvaluateGate(ctx, req); err != nil || !g.Passed {
		t.Fatalf("passing gate: %+v %v", g, err)
	}

	// Delivery: only the exact changeset the latest gate decision approved.
	deliver := func() error {
		return acts.Deliver(ctx, workflow.DeliverRequest{EngRequest: workflow.EngRequest{WorkflowRunID: runID, TaskID: tid, Stage: "deliver", Step: 1}, Gate: "gate"})
	}
	changeset := func(sha string) {
		e.saveArtifact(runID, tid, "changeset", map[string]any{"baseSha": strings.Repeat("a", 40), "headSha": sha, "branch": "wise/x", "files": []any{}, "patch": "+x"})
	}
	expectRefused := func(want string) {
		t.Helper()
		err := deliver()
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("deliver: want refusal %q, got %v", want, err)
		}
	}
	changeset(strings.Repeat("b", 40))
	expectRefused("has not been approved")
	ap, err := e.tasks.CreateApproval(ctx, runID, tid, "gate", []string{"changeset"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	expectRefused("has not been approved") // pending
	e.expect(e.do("POST", "/approvals/"+ap.ID.String()+"/approve", map[string]any{"combinedHash": ap.CombinedHash}), 200)

	changeset(strings.Repeat("c", 40)) // changed after the approval
	expectRefused("is superseded")     // a new changeset makes the earlier approval stale
	ap2, err := e.tasks.CreateApproval(ctx, runID, tid, "gate", []string{"changeset"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	e.expect(e.do("POST", "/approvals/"+ap2.ID.String()+"/reject", map[string]any{"combinedHash": ap2.CombinedHash, "comment": "use a context timeout"}), 200)
	expectRefused("is rejected")
	e.exec.mu.Lock()
	n := len(e.exec.deliveries)
	e.exec.mu.Unlock()
	if n != 0 {
		t.Fatalf("delivered %d times before an approval of the latest changeset", n)
	}

	ap3, err := e.tasks.CreateApproval(ctx, runID, tid, "gate", []string{"changeset"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	e.expect(e.do("POST", "/approvals/"+ap3.ID.String()+"/approve", map[string]any{"combinedHash": ap3.CombinedHash}), 200)
	if err := deliver(); err != nil {
		t.Fatal(err)
	}
	e.exec.mu.Lock()
	got := append([]string(nil), e.exec.deliveries...)
	e.exec.mu.Unlock()
	if len(got) != 1 || got[0] != strings.Repeat("c", 40) {
		t.Fatalf("deliveries: %v", got)
	}
	w := e.expect(e.do("GET", "/tasks/"+taskID+"/workspace", nil), 200)["workspace"].(map[string]any)
	if w["deliveredBranch"] != "wise/x" || w["deliveredSha"] != strings.Repeat("c", 40) {
		t.Fatalf("workspace after delivery: %v", w)
	}
}
