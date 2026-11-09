package httpapi_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestEngineeringProfileInferenceAndExamples(t *testing.T) {
	e := setup(t)
	e.login()
	pid := e.expect(e.do("POST", "/projects", map[string]string{"name": "Prof " + uuid.NewString()[:6]}), 201)["id"].(string)
	dir := gitRepo(t, e.repoRoot, "prof-"+uuid.NewString()[:6])
	if err := os.WriteFile(filepath.Join(dir, "errors.ts"), []byte("export function problem(status: number) {\n  return { type: 'about:blank', status };\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("TOKEN=x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rid := e.expect(e.do("POST", "/projects/"+pid+"/repositories", map[string]any{"name": "web", "sourceType": "local", "localPath": dir}), 201)["id"].(string)

	// Nothing indexed yet: inference has nothing to read.
	e.expect(e.do("POST", "/projects/"+pid+"/infer-profile", nil), 409)
	e.expect(e.do("POST", "/repositories/"+rid+"/index", nil), 202)
	head := e.expect(e.do("GET", "/repositories/"+rid+"/index", nil), 200)["headSha"].(string)
	indexOnce(t, e, []map[string]any{
		{"path": "tsconfig.json", "contentHash": "t", "chunks": []map[string]any{{"kind": "config", "startLine": 1, "endLine": 3, "heading": "tsconfig.json",
			"content": `{"compilerOptions": {"strict": true, "noUncheckedIndexedAccess": true}}`}}},
		{"path": ".env.local", "contentHash": "s", "skippedReason": "secret"},
	}, nil, head, []map[string]any{{"ecosystem": "npm", "name": "vitest", "version": "^3", "dev": true, "manifest": "package.json"}})

	// The existing profile is shown to the agent so it does not repeat it.
	e.expect(e.do("POST", "/memories", map[string]any{"scope": "developer", "kind": "preference", "category": "testing",
		"title": "Vitest for unit tests", "body": "Use Vitest."}), 201)
	run := e.expect(e.do("POST", "/projects/"+pid+"/infer-profile", nil), 202)
	again := e.expect(e.do("POST", "/projects/"+pid+"/infer-profile", nil), 202)
	if run["id"] != again["id"] || run["agentName"] != "profile-agent" {
		t.Fatalf("one inference per indexed commit: %v %v", run["id"], again["id"])
	}
	in := run["input"].(map[string]any)
	cfg := in["configuration"].(string)
	if !strings.Contains(cfg, "tsconfig.json") || !strings.Contains(cfg, "noUncheckedIndexedAccess") || strings.Contains(cfg, ".env") {
		t.Fatalf("configuration input: %q", cfg)
	}
	if ex := in["existing_memories"].([]any); len(ex) != 1 || ex[0].(map[string]any)["title"] != "Vitest for unit tests" {
		t.Fatalf("existing profile: %v", ex)
	}
	if deps := in["dependencies"].([]any); len(deps) != 1 || !strings.Contains(deps[0].(string), "vitest") {
		t.Fatalf("dependencies: %v", deps)
	}

	runID, tok := e.claim()
	e.expect(e.runtimeDo(tok, "POST", "/runtime/v1/runs/"+runID+"/model-turn", map[string]any{"messages": []map[string]string{{"role": "user", "content": "go"}}}), 200)
	e.model.mu.Lock()
	sys := e.model.lastSystem
	e.model.mu.Unlock()
	if strings.Contains(sys, "## Wise context") {
		t.Fatal("the profile agent gets no memory context")
	}
	e.expect(e.runtimeDo(tok, "POST", "/runtime/v1/runs/"+runID+"/complete", map[string]any{"output": map[string]any{"memories": []any{
		map[string]any{"scope": "developer", "kind": "preference", "category": "coding", "title": "TypeScript strict mode",
			"body": "Enable strict and noUncheckedIndexedAccess.", "rationale": "tsconfig.json: strict: true"},
	}}}), 204)
	var cands []any
	for i := 0; i < 50 && len(cands) == 0; i++ {
		cands = e.expect(e.do("GET", "/memories?status=candidate", nil), 200)["items"].([]any)
		time.Sleep(40 * time.Millisecond)
	}
	if len(cands) != 1 || cands[0].(map[string]any)["source"] != "profile_inference" || cands[0].(map[string]any)["scope"] != "developer" {
		t.Fatalf("profile candidate: %v", cands)
	}

	// "Make it a rule" is an explicit, versioned edit.
	c := cands[0].(map[string]any)
	approved := e.expect(e.do("POST", "/memories/"+c["id"].(string)+"/approve", nil), 200)
	rule := e.expect(e.do("PUT", "/memories/"+c["id"].(string), map[string]any{"scope": "developer", "kind": "rule", "category": "coding",
		"title": approved["title"], "body": approved["body"]}), 200)
	if rule["kind"] != "rule" || rule["version"].(float64) != approved["version"].(float64)+1 {
		t.Fatalf("make it a rule: %v", rule)
	}
	vs := e.expect(e.do("GET", "/memories/"+c["id"].(string)+"/versions", nil), 200)["items"].([]any)
	if vs[0].(map[string]any)["kind"] != "rule" || vs[1].(map[string]any)["kind"] != "preference" {
		t.Fatalf("history shows the change: %v", vs)
	}

	// Approved examples carry the snippet, location and commit; secrets are refused.
	ex := e.expect(e.do("POST", "/repositories/"+rid+"/examples", map[string]any{"path": "errors.ts", "startLine": 1, "endLine": 3,
		"title": "Problem responses", "note": "How we build RFC 7807 errors."}), 201)
	body := ex["body"].(string)
	if ex["kind"] != "example" || ex["scope"] != "project" || ex["status"] != "active" || !strings.Contains(body, "about:blank") ||
		!strings.Contains(body, "web/errors.ts lines 1-3") || !strings.Contains(body, head[:12]) {
		t.Fatalf("example: %v", ex)
	}
	e.expect(e.do("POST", "/repositories/"+rid+"/examples", map[string]any{"path": ".env", "startLine": 1, "endLine": 1}), 403)
	e.expect(e.do("POST", "/repositories/"+rid+"/examples", map[string]any{"path": "errors.ts", "startLine": 3, "endLine": 1}), 400)
}
