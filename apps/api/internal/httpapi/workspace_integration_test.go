package httpapi_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"wise/apps/api/internal/executor"
)

// fakeExecutor stands in for the executor service.
type fakeExecutor struct {
	mu       sync.Mutex
	prepared []executor.PrepareRequest
	removed  []string
	egress   []string
	fail     string
}

func (f *fakeExecutor) Prepare(_ context.Context, req executor.PrepareRequest) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prepared = append(f.prepared, req)
	return "job-" + req.ID[:8], nil
}

func (f *fakeExecutor) Wait(_ context.Context, jobID string, onOutput func(string), beat func()) (executor.JobStatus, error) {
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
