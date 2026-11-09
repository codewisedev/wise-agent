package httpapi_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// fakeIndexer plays the Python indexer over the runtime API.
func indexOnce(t *testing.T, e *env, files []map[string]any, deleted []string, commit string, deps []map[string]any) string {
	t.Helper()
	if files == nil {
		files = []map[string]any{}
	}
	if deleted == nil {
		deleted = []string{}
	}
	if deps == nil {
		deps = []map[string]any{}
	}
	c := e.expect(e.runtimeDo(runtimeToken, "POST", "/runtime/v1/index/claim", map[string]string{"runtimeId": "idx"}), 200)
	job, tok := c["jobId"].(string), c["token"].(string)
	if c["repository"].(map[string]any)["localPath"] == "" || c["settings"].(map[string]any)["chunk_lines"] == nil {
		t.Fatalf("claim payload: %v", c)
	}
	e.expect(e.runtimeDo(tok, "POST", "/runtime/v1/index/"+job+"/heartbeat", nil), 204)
	e.expect(e.runtimeDo(tok, "POST", "/runtime/v1/index/"+job+"/files", map[string]any{"files": files, "deleted": deleted}), 204)
	e.expect(e.runtimeDo(tok, "POST", "/runtime/v1/index/"+job+"/complete", map[string]any{"commitSha": commit, "dependencies": deps}), 204)
	// The job token dies with the job.
	e.expect(e.runtimeDo(tok, "POST", "/runtime/v1/index/"+job+"/heartbeat", nil), 401)
	return job
}

func TestIndexUploadSearchAndContextTools(t *testing.T) {
	e := setup(t)
	e.login()
	pid := e.expect(e.do("POST", "/projects", map[string]string{"name": "Indexed " + uuid.NewString()[:6]}), 201)["id"].(string)
	dir := gitRepo(t, e.repoRoot, "svc-"+uuid.NewString()[:6])
	if err := os.WriteFile(filepath.Join(dir, "gateway.go"), []byte("package api\n\nfunc (g *Gateway) Reconfigure() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "keys.ts"), []byte("const k = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	repo := e.expect(e.do("POST", "/projects/"+pid+"/repositories", map[string]any{"name": "svc", "sourceType": "local", "localPath": dir}), 201)
	rid, head := repo["id"].(string), repo["headSha"].(string)

	// Nothing queued yet: the indexer gets 204. A request queues one job (a second is reused).
	e.expect(e.runtimeDo(runtimeToken, "POST", "/runtime/v1/index/claim", map[string]string{"runtimeId": "idx"}), 204)
	e.expect(e.do("POST", "/repositories/"+rid+"/index", nil), 202)
	e.expect(e.do("POST", "/repositories/"+rid+"/index", nil), 202)
	if jobs := e.expect(e.do("GET", "/repositories/"+rid+"/index", nil), 200)["jobs"].([]any); len(jobs) != 1 {
		t.Fatalf("one pending job: %v", jobs)
	}
	e.expect(e.runtimeDo("wise_ij_nope", "POST", "/runtime/v1/index/"+uuid.NewString()+"/heartbeat", nil), 401)

	files := []map[string]any{
		{"path": "gateway.go", "language": "go", "contentHash": "h1", "size": 50, "lines": 3, "imports": []string{"context"},
			"symbols": []map[string]any{{"name": "Reconfigure", "kind": "method", "parent": "Gateway", "exported": true, "startLine": 3, "endLine": 3,
				"signature": "func (g *Gateway) Reconfigure()", "doc": "Swaps the configuration."}},
			"chunks": []map[string]any{{"kind": "code", "startLine": 1, "endLine": 3, "heading": "method Gateway.Reconfigure",
				"content": "func (g *Gateway) Reconfigure() {}"}}},
		{"path": "docs/retries.md", "language": "markdown", "contentHash": "h2",
			"chunks": []map[string]any{{"kind": "doc", "startLine": 1, "endLine": 4, "heading": "Webhooks > Retry policy",
				"content": "Failed webhooks are retried with exponential backoff."}}},
		{"path": "keys.ts", "contentHash": "h3", "skippedReason": "secret"},
	}
	// Uploads are validated: no absolute or escaping paths, no chunks on skipped files.
	c := e.expect(e.runtimeDo(runtimeToken, "POST", "/runtime/v1/index/claim", map[string]string{"runtimeId": "idx"}), 200)
	job, tok := c["jobId"].(string), c["token"].(string)
	e.expect(e.runtimeDo(tok, "POST", "/runtime/v1/index/"+job+"/files", map[string]any{"files": []map[string]any{{"path": "../etc/passwd", "contentHash": "x"}}}), 400)
	e.expect(e.runtimeDo(tok, "POST", "/runtime/v1/index/"+job+"/files", map[string]any{"files": []map[string]any{{"path": "a.ts", "contentHash": "x", "skippedReason": "secret",
		"chunks": []map[string]any{{"kind": "code", "startLine": 1, "endLine": 1, "content": "leak"}}}}}), 400)
	e.expect(e.runtimeDo(tok, "POST", "/runtime/v1/index/"+uuid.NewString()+"/files", map[string]any{"files": files}), 403) // another job
	e.expect(e.runtimeDo(tok, "POST", "/runtime/v1/index/"+job+"/files", map[string]any{"files": files}), 204)
	e.expect(e.runtimeDo(tok, "POST", "/runtime/v1/index/"+job+"/complete", map[string]any{"commitSha": head,
		"dependencies": []map[string]any{{"ecosystem": "go", "name": "github.com/google/uuid", "version": "v1.6.0", "manifest": "go.mod"}}}), 204)

	st := e.expect(e.do("GET", "/repositories/"+rid+"/index", nil), 200)
	stats := st["stats"].(map[string]any)
	if st["status"] != "indexed" || st["stale"] != false || stats["files"].(float64) != 2 || stats["skipped"].(float64) != 1 || stats["symbols"].(float64) != 1 {
		t.Fatalf("index state: %v", st)
	}

	// Search: identifiers are split ("gateway reconfigure" finds ReconfigureGateway-style names), symbols by name.
	hits := e.expect(e.do("GET", "/projects/"+pid+"/context/search?q=gateway+reconfigure", nil), 200)["items"].([]any)
	if len(hits) == 0 || hits[0].(map[string]any)["path"] != "gateway.go" || hits[0].(map[string]any)["indexedSha"] != head {
		t.Fatalf("search: %v", hits)
	}
	hits = e.expect(e.do("GET", "/projects/"+pid+"/context/search?q=Reconfigure", nil), 200)["items"].([]any)
	if first := hits[0].(map[string]any); first["kind"] != "symbol" || first["why"] != "exact name" || first["title"] != "method Gateway.Reconfigure" {
		t.Fatalf("symbol first: %v", hits)
	}
	if hits := e.expect(e.do("GET", "/projects/"+pid+"/context/search?q=exponential+backoff&kind=doc", nil), 200)["items"].([]any); len(hits) != 1 ||
		hits[0].(map[string]any)["title"] != "Webhooks > Retry policy" {
		t.Fatalf("doc search: %v", hits)
	}
	if syms := e.expect(e.do("GET", "/projects/"+pid+"/context/symbols?name=reconf", nil), 200)["items"].([]any); len(syms) != 1 {
		t.Fatalf("prefix symbol search: %v", syms)
	}
	if deps := e.expect(e.do("GET", "/projects/"+pid+"/context/dependencies", nil), 200)["items"].([]any); len(deps) != 1 {
		t.Fatalf("deps: %v", deps)
	}

	// Files: real content; secrets (flagged or by name) and escapes refused.
	f := e.expect(e.do("GET", "/repositories/"+rid+"/file?path=gateway.go", nil), 200)
	if !strings.Contains(f["content"].(string), "Reconfigure") || f["lines"].(float64) != 4 {
		t.Fatalf("file: %v", f)
	}
	e.expect(e.do("GET", "/repositories/"+rid+"/file?path=keys.ts", nil), 403)
	e.expect(e.do("GET", "/repositories/"+rid+"/file?path=.env", nil), 403)
	e.expect(e.do("GET", "/repositories/"+rid+"/file?path=../../etc/passwd", nil), 404)
	// A symlink to a secret is refused under its innocent name too.
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("TOKEN=x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(".env", filepath.Join(dir, "notes.txt")); err != nil {
		t.Fatal(err)
	}
	e.expect(e.do("GET", "/repositories/"+rid+"/file?path=notes.txt", nil), 403)

	// Agents search their run's project through the run token.
	e.createRunInProject("product-agent", pid)
	runID, runTok := e.claim()
	got := e.expect(e.runtimeDo(runTok, "GET", "/runtime/v1/runs/"+runID+"/context/search?q=backoff", nil), 200)["items"].([]any)
	if len(got) != 1 {
		t.Fatalf("runtime search: %v", got)
	}
	e.expect(e.runtimeDo(runTok, "GET", "/runtime/v1/runs/"+runID+"/context/symbols?name=Reconfigure&repository=svc", nil), 200)
	e.expect(e.runtimeDo(runTok, "GET", "/runtime/v1/runs/"+runID+"/context/symbols?name=x&repository=nope", nil), 400)
	e.expect(e.runtimeDo(runTok, "GET", "/runtime/v1/runs/"+uuid.NewString()+"/context/search?q=x", nil), 403)

	// HEAD moves: results are flagged stale; the scheduler queues a re-index.
	gitCommit(t, dir, "second")
	if _, err := e.repoSvc.Inspect(context.Background(), uuid.MustParse(rid)); err != nil {
		t.Fatal(err)
	}
	if first := e.expect(e.do("GET", "/projects/"+pid+"/context/search?q=backoff", nil), 200)["items"].([]any)[0].(map[string]any); first["stale"] != true {
		t.Fatalf("stale after commit: %v", first)
	}
	if err := e.index.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	newHead := e.expect(e.do("GET", "/repositories/"+rid+"/index", nil), 200)["headSha"].(string)
	indexOnce(t, e, nil, []string{"docs/retries.md"}, newHead, nil)
	st = e.expect(e.do("GET", "/repositories/"+rid+"/index", nil), 200)
	if st["stale"] != false || st["stats"].(map[string]any)["files"].(float64) != 1 {
		t.Fatalf("re-indexed with deletion: %v", st)
	}

	// A failure is reported on the repository.
	e.expect(e.do("POST", "/repositories/"+rid+"/index", nil), 202)
	c = e.expect(e.runtimeDo(runtimeToken, "POST", "/runtime/v1/index/claim", map[string]string{"runtimeId": "idx"}), 200)
	e.expect(e.runtimeDo(c["token"].(string), "POST", "/runtime/v1/index/"+c["jobId"].(string)+"/fail", map[string]any{"error": "disk gone"}), 204)
	if st := e.expect(e.do("GET", "/repositories/"+rid+"/index", nil), 200); st["status"] != "failed" || st["error"] != "disk gone" {
		t.Fatalf("failure: %v", st)
	}
}
