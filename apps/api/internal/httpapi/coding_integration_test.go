package httpapi_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"

	"wise/apps/api/internal/agents"
)

// claudeStream is a minimal Claude Code stream-json run ending in report.
func claudeStream(report string) string {
	return strings.Join([]string{
		`{"type":"system","subtype":"init","tools":["Bash","Read","Edit"]}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit","input":{"file_path":"/workspace/src/greet.js"}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","content":"ok"}]}}`,
		`{"type":"result","subtype":"success","is_error":false,"num_turns":3,"total_cost_usd":0.01,"usage":{"input_tokens":500,"output_tokens":80},"structured_output":` + report + `}`,
	}, "\n") + "\n"
}

const validReport = `{"summary":"Added farewell","changed_files":[{"path":"src/greet.js","change":"modified","why":"new function"}],` +
	`"tests":[{"path":"test/greet.test.js","description":"farewell"}],"checks_run":[{"command":"npm test","result":"passed"}],` +
	`"notes":[],"commit_message":"feat: add farewell"}`

func TestExternalCodingRuns(t *testing.T) {
	e := setup(t)
	e.login()
	ctx := context.Background()
	taskID, _ := e.workspaceTask(t)
	tid := uuid.MustParse(taskID)
	var pid string
	if err := e.db.Pool.QueryRow(ctx, "SELECT project_id::text FROM tasks WHERE id = $1", tid).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	wfID, _ := uuid.NewV7()
	if _, err := e.db.Pool.Exec(ctx, "INSERT INTO workflow_runs (id, task_id, workflow_type, status) VALUES ($1, $2, 'PipelineWorkflow', 'running')", wfID, tid); err != nil {
		t.Fatal(err)
	}
	enqueue := func(key string) (string, string) {
		t.Helper()
		_, err := e.agents.Enqueue(ctx, "backend-agent", map[string]any{"request": "Add farewell()", "plan": map[string]any{"steps": []any{}}},
			agents.RunOptions{WorkflowRunID: &wfID, IdempotencyKey: key, Model: &agents.ModelOverride{Runtime: "claude-code"}})
		if err != nil {
			t.Fatal(err)
		}
		c := e.expect(e.runtimeDo(runtimeToken, "POST", "/runtime/v1/runs/claim", map[string]string{"runtimeId": "rt"}), 200)
		if c["runtime"] != "claude-code" {
			t.Fatalf("claim runtime: %v", c["runtime"])
		}
		return c["run"].(map[string]any)["id"].(string), c["runToken"].(string)
	}
	runID, tok := enqueue("k1")
	ext := "/runtime/v1/runs/" + runID + "/external"

	// Not allowed until the project enables the runtime — and subscription mode.
	if r := e.runtimeDo(tok, "POST", ext, nil); r.status != 409 || !strings.Contains(stringify(r.body), "has not enabled claude-code") {
		t.Fatalf("project policy: %d %v", r.status, r.body)
	}
	e.expect(e.do("PUT", "/projects/"+pid+"/coding-runtimes", map[string]any{"enabled": []string{"claude-code"}, "allowSubscription": false}), 200)
	if r := e.runtimeDo(tok, "POST", ext, nil); r.status != 409 || !strings.Contains(stringify(r.body), "subscription") {
		t.Fatalf("subscription not allowed: %d %v", r.status, r.body)
	}
	e.expect(e.do("PUT", "/projects/"+pid+"/coding-runtimes", map[string]any{"enabled": []string{"claude-code"}, "allowSubscription": true}), 200)
	if r := e.runtimeDo(tok, "POST", ext, nil); r.status != 409 || !strings.Contains(stringify(r.body), "no Claude Code token") {
		t.Fatalf("no token: %d %v", r.status, r.body)
	}
	e.coding.EnvToken = "oauth-token-for-test"
	st := e.expect(e.runtimeDo(tok, "POST", ext, nil), 200)
	if st["mode"] != "subscription" || st["reducedIsolation"] != true || st["cliVersion"] != "2.1.319" {
		t.Fatalf("start: %v", st)
	}
	e.exec.mu.Lock()
	req := e.exec.runners[0]
	e.exec.mu.Unlock()
	if req.Env["CLAUDE_CODE_OAUTH_TOKEN"] != "oauth-token-for-test" || req.GrantHosts[0] != "api.anthropic.com" ||
		!strings.Contains(req.Files[".wise/instructions.md"], "Backend Agent") || !strings.Contains(req.Files[".wise/prompt.md"], "Add farewell()") {
		t.Fatalf("runner request: %+v", req)
	}

	// The report must match the agent's contract.
	e.exec.mu.Lock()
	e.exec.runnerOut = claudeStream(`{"files":[]}`)
	e.exec.mu.Unlock()
	p := e.expect(e.runtimeDo(tok, "GET", ext+"?wait=0", nil), 200)
	out := p["outcome"].(map[string]any)
	if p["done"] != true || out["class"] != "incomplete" || !strings.Contains(out["message"].(string), "output contract") || len(p["events"].([]any)) < 3 {
		t.Fatalf("invalid report: %v", p)
	}

	// A valid report succeeds; usage is recorded like a native run's.
	runID2, tok2 := enqueue("k2")
	e.exec.mu.Lock()
	e.exec.runnerOut = claudeStream(validReport)
	e.exec.mu.Unlock()
	e.expect(e.runtimeDo(tok2, "POST", "/runtime/v1/runs/"+runID2+"/external", nil), 200)
	p = e.expect(e.runtimeDo(tok2, "GET", "/runtime/v1/runs/"+runID2+"/external?wait=0", nil), 200)
	out = p["outcome"].(map[string]any)
	if out["class"] != "succeeded" || out["output"].(map[string]any)["commit_message"] != "feat: add farewell" {
		t.Fatalf("valid report: %v", p)
	}
	var provider string
	var cost float64
	if err := e.db.Pool.QueryRow(ctx, "SELECT provider, cost_usd FROM model_calls WHERE run_id = $1", uuid.MustParse(runID2)).Scan(&provider, &cost); err != nil ||
		provider != "claude-code" || cost != 0.01 {
		t.Fatalf("model call: %s %v %v", provider, cost, err)
	}

	// Proxy mode: an Anthropic API-key account; the runner gets only a run token.
	e.expect(e.do("POST", "/accounts", map[string]any{"name": "anthropic-work", "kind": "anthropic", "secret": "sk-ant-api-real-key"}), 201)
	sett := e.expect(e.do("GET", "/settings", nil), 200)
	var cur map[string]any
	var version float64
	for _, it := range sett["items"].([]any) {
		m := it.(map[string]any)
		if m["key"] == "coding_runtimes" {
			cur, version = m["value"].(map[string]any), m["version"].(float64)
		}
	}
	cc := cur["runtimes"].(map[string]any)["claude-code"].(map[string]any)
	cc["credential"] = map[string]any{"mode": "proxy", "account": "anthropic-work"}
	e.expect(e.do("PUT", "/settings/coding_runtimes", map[string]any{"value": cur, "version": version}), 200)
	runID3, tok3 := enqueue("k3")
	e.expect(e.runtimeDo(tok3, "POST", "/runtime/v1/runs/"+runID3+"/external", nil), 200)
	e.exec.mu.Lock()
	req = e.exec.runners[len(e.exec.runners)-1]
	e.exec.mu.Unlock()
	e.proxy.mu.Lock()
	var runTok string
	for k, r := range e.proxy.registered {
		if r.RunID == runID3 {
			runTok = k
			if r.APIKey != "sk-ant-api-real-key" || r.Vendor != "anthropic" || r.BudgetUSD != 3 {
				t.Fatalf("proxy registration: %+v", r)
			}
		}
	}
	e.proxy.mu.Unlock()
	raw, _ := json.Marshal(req)
	if runTok == "" || req.Env["ANTHROPIC_API_KEY"] != runTok || !req.ModelProxy || strings.Contains(string(raw), "sk-ant-api-real-key") {
		t.Fatalf("proxy runner request: %s", raw)
	}
	p = e.expect(e.runtimeDo(tok3, "GET", "/runtime/v1/runs/"+runID3+"/external?wait=0", nil), 200)
	if u := p["outcome"].(map[string]any)["usage"].(map[string]any); u["inputTokens"] != float64(900) {
		t.Fatalf("proxy usage (measured by the proxy): %v", u)
	}
	e.proxy.mu.Lock()
	revoked := len(e.proxy.revoked)
	e.proxy.mu.Unlock()
	if revoked != 1 {
		t.Fatalf("proxy token revoked %d times", revoked)
	}

	var n int
	if err := e.db.Pool.QueryRow(ctx, "SELECT count(*) FROM audit_logs WHERE action = 'project.coding_runtimes_updated'").Scan(&n); err != nil || n != 2 {
		t.Fatalf("audit: %d %v", n, err)
	}
}

func TestCodingRuntimeStepValidation(t *testing.T) {
	e := setup(t)
	e.login()
	bad := map[string]any{"steps": []any{
		map[string]any{"id": "analysis", "agent": "product-agent", "runtime": "claude-code"},
	}}
	res := e.expect(e.do("POST", "/workflow-templates", map[string]any{"name": "Bad runtime", "definition": bad}), 400)
	if !strings.Contains(stringify(res), "only agents that implement the change") {
		t.Fatalf("validation: %v", res)
	}
}
