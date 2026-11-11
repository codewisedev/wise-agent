package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"testing"

	"github.com/google/uuid"

	"wise/apps/api/internal/httpapi"
	"wise/apps/api/internal/modelgateway"
)

const runtimeToken = "test-runtime-token-0123456789abcdef0123"

// scriptedProvider returns queued responses in order (then a valid final).
type scriptedProvider struct {
	mu         sync.Mutex
	queue      []string
	lastSystem string // the system prompt of the last call
}

func (p *scriptedProvider) Name() string                 { return "scripted" }
func (p *scriptedProvider) Cloud() bool                  { return true }
func (p *scriptedProvider) Health(context.Context) error { return nil }
func (p *scriptedProvider) push(texts ...string) {
	p.mu.Lock()
	p.queue = append(p.queue, texts...)
	p.mu.Unlock()
}
func (p *scriptedProvider) Complete(_ context.Context, req modelgateway.ProviderRequest) (modelgateway.ProviderResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lastSystem = req.System
	text := `{"action":"final","final":{}}`
	if len(p.queue) > 0 {
		text, p.queue = p.queue[0], p.queue[1:]
	}
	return modelgateway.ProviderResponse{Text: text, Usage: modelgateway.Usage{InputTokens: 100, OutputTokens: 50}, CostUSD: 0.002}, nil
}

// runtimeDo calls a runtime route with a bearer token and no cookies.
func (e *env) runtimeDo(token, method, path string, body any) resp {
	e.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.srv.URL+httpapi.BasePath+path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	out := resp{status: res.StatusCode, body: map[string]any{}}
	raw, _ := io.ReadAll(res.Body)
	_ = json.Unmarshal(raw, &out.body)
	return out
}

func (e *env) createRun(agent string, input map[string]any) string {
	e.t.Helper()
	return e.expect(e.do("POST", "/agents/"+agent+"/runs", map[string]any{"input": input}), 202)["id"].(string)
}

// createRunInProject starts an agent run with a project attached.
func (e *env) createRunInProject(agent, projectID string) string {
	e.t.Helper()
	return e.expect(e.do("POST", "/agents/"+agent+"/runs", map[string]any{
		"input": map[string]any{"request": "Find the retry policy"}, "projectId": projectID,
	}), 202)["id"].(string)
}

func (e *env) claim() (runID, token string) {
	e.t.Helper()
	c := e.expect(e.runtimeDo(runtimeToken, "POST", "/runtime/v1/runs/claim", map[string]string{"runtimeId": "test-rt"}), 200)
	return c["run"].(map[string]any)["id"].(string), c["runToken"].(string)
}

var validProductOutput = map[string]any{
	"summary": "s", "problem": "p", "target_users": []string{"devs"}, "goals": []string{"g"}, "non_goals": []string{},
	"key_features": []map[string]string{{"name": "f", "description": "d", "priority": "must"}},
	"risks":        []string{}, "assumptions": []string{}, "open_questions": []string{},
}

func TestAgentRunLifecycle(t *testing.T) {
	e := setup(t)
	e.login()

	list := e.expect(e.do("GET", "/agents", nil), 200)["items"].([]any)
	if len(list) != 15 {
		t.Fatalf("want 15 agents, got %d", len(list))
	}
	detail := e.expect(e.do("GET", "/agents/product-agent", nil), 200)
	if detail["prompt"] == "" || detail["enabled"] != true || len(detail["tools"].([]any)) != 8 {
		t.Fatalf("detail: %v", detail)
	}
	e.expect(e.do("POST", "/agents/release-agent/runs", map[string]any{"input": map[string]any{}}), 409)
	e.expect(e.do("POST", "/agents/nope-agent/runs", map[string]any{"input": map[string]any{}}), 404)
	e.expect(e.do("POST", "/agents/product-agent/runs", map[string]any{"input": map[string]any{"request": "x", "bogus": 1}}), 400)
	runID := e.createRun("product-agent", map[string]any{"request": "Build a salon booking app"})

	// Runtime auth boundaries.
	e.expect(e.runtimeDo("wrong-token", "POST", "/runtime/v1/runs/claim", map[string]string{"runtimeId": "x"}), 401)
	e.expect(e.do("POST", "/runtime/v1/runs/claim", map[string]string{"runtimeId": "x"}), 401) // user session
	claim := e.expect(e.runtimeDo(runtimeToken, "POST", "/runtime/v1/runs/claim", map[string]string{"runtimeId": "rt-1"}), 200)
	token := claim["runToken"].(string)
	if claim["run"].(map[string]any)["id"] != runID || claim["prompt"] == "" || len(claim["tools"].([]any)) != 8 {
		t.Fatalf("claim: %v", claim)
	}
	if e.runtimeDo(runtimeToken, "POST", "/runtime/v1/runs/claim", map[string]string{"runtimeId": "rt-2"}).status != 204 {
		t.Fatal("queue should be empty")
	}
	base := "/runtime/v1/runs/" + runID
	e.expect(e.do("POST", base+"/heartbeat", nil), 401)                                               // user session
	e.expect(e.runtimeDo(runtimeToken, "POST", base+"/heartbeat", nil), 401)                          // service token
	e.expect(e.runtimeDo(token, "POST", "/runtime/v1/runs/"+uuid.NewString()+"/heartbeat", nil), 403) // other run
	if hb := e.expect(e.runtimeDo(token, "POST", base+"/heartbeat", nil), 200); hb["cancelRequested"] != false {
		t.Fatalf("heartbeat: %v", hb)
	}

	// One tool round, then the final answer.
	e.model.push(`{"thought":"look around","action":"call_tools","tool_calls":[{"tool":"get_project_context","arguments":{}}]}`)
	msgs := []map[string]string{{"role": "user", "content": "Build a salon booking app"}}
	turn := e.expect(e.runtimeDo(token, "POST", base+"/model-turn", map[string]any{"messages": msgs}), 200)
	if turn["action"] != "call_tools" || turn["provider"] != "scripted" {
		t.Fatalf("turn 1: %v", turn)
	}
	e.expect(e.runtimeDo(token, "POST", base+"/events", map[string]any{"events": []map[string]any{
		{"type": "tool.call", "payload": map[string]any{"tool": "get_project_context", "input": map[string]any{}}},
		{"type": "tool.result", "payload": map[string]any{"tool": "get_project_context", "input": map[string]any{}, "output": map[string]any{"project": nil}, "status": "ok", "durationMs": 3}},
	}}), 204)
	e.expect(e.runtimeDo(token, "POST", base+"/events", map[string]any{"events": []map[string]any{{"type": "run.completed", "payload": map[string]any{}}}}), 400)

	finalJSON, _ := json.Marshal(map[string]any{"action": "final", "final": validProductOutput})
	e.model.push(string(finalJSON))
	turn = e.expect(e.runtimeDo(token, "POST", base+"/model-turn", map[string]any{"messages": msgs}), 200)
	if turn["action"] != "final" || turn["final"] == nil {
		t.Fatalf("turn 2: %v", turn)
	}

	e.expect(e.runtimeDo(token, "POST", base+"/complete", map[string]any{"output": map[string]any{"summary": "only"}}), 422)
	e.expect(e.runtimeDo(token, "POST", base+"/complete", map[string]any{"output": validProductOutput}), 204)
	e.expect(e.runtimeDo(token, "POST", base+"/heartbeat", nil), 401) // token dies with the run

	run := e.expect(e.do("GET", "/agent-runs/"+runID, nil), 200)
	if run["status"] != "succeeded" || run["totalInputTokens"].(float64) != 200 || run["totalCostUsd"].(float64) != 0.004 {
		t.Fatalf("run: %v", run)
	}
	evs := e.expect(e.do("GET", "/agent-runs/"+runID+"/events", nil), 200)["items"].([]any)
	var types []string
	for i, ev := range evs {
		m := ev.(map[string]any)
		if int(m["seq"].(float64)) != i+1 {
			t.Fatalf("seq gap at %d: %v", i, m)
		}
		types = append(types, m["type"].(string))
	}
	want := []string{"run.queued", "run.started", "model.request", "model.response", "tool.call", "tool.result", "model.request", "model.response", "run.completed"}
	if len(types) != len(want) {
		t.Fatalf("events: %v", types)
	}
	for i := range want {
		if types[i] != want[i] {
			t.Fatalf("events: %v, want %v", types, want)
		}
	}
	calls := e.expect(e.do("GET", "/agent-runs/"+runID+"/model-calls", nil), 200)["items"].([]any)
	if len(calls) != 2 {
		t.Fatalf("model calls: %v", calls)
	}
	var toolRows int
	_ = e.db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM tool_calls WHERE run_id = $1`, runID).Scan(&toolRows)
	if toolRows != 1 {
		t.Fatalf("tool_calls rows: %d", toolRows)
	}
	e.expect(e.do("POST", "/agent-runs/"+runID+"/cancel", nil), 409)
}

func TestRunQueueClaimsLeasesAndCancellation(t *testing.T) {
	e := setup(t)
	e.login()
	in := map[string]any{"request": "do things"}
	a, b := e.createRun("product-agent", in), e.createRun("requirements-agent", in)

	// Concurrent claimers never get the same run.
	var wg sync.WaitGroup
	got := make([]string, 2)
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := e.runtimeDo(runtimeToken, "POST", "/runtime/v1/runs/claim", map[string]string{"runtimeId": "rt"})
			if r.status == 200 {
				got[i] = r.body["run"].(map[string]any)["id"].(string)
			}
		}()
	}
	wg.Wait()
	if got[0] == "" || got[1] == "" || got[0] == got[1] {
		t.Fatalf("claims: %v (runs %s %s)", got, a, b)
	}

	// Expired lease → requeued (attempt 1 of 2) → claimed again → expired → failed.
	expire := func() {
		if _, err := e.db.Pool.Exec(context.Background(), `UPDATE agent_runs SET lease_expires_at = now() - interval '1 second' WHERE id = $1`, a); err != nil {
			t.Fatal(err)
		}
	}
	expire()
	if n, err := e.queue.Reap(context.Background()); err != nil || n != 1 {
		t.Fatalf("reap: %d %v", n, err)
	}
	if st := e.expect(e.do("GET", "/agent-runs/"+a, nil), 200)["status"]; st != "queued" {
		t.Fatalf("after first expiry: %v", st)
	}
	id, _ := e.claim()
	if id != a {
		t.Fatalf("requeued run should be claimed again, got %s", id)
	}
	expire()
	_, _ = e.queue.Reap(context.Background())
	if r := e.expect(e.do("GET", "/agent-runs/"+a, nil), 200); r["status"] != "failed" || r["attempt"].(float64) != 2 {
		t.Fatalf("after second expiry: %v", r)
	}

	// Cancelling a queued run is immediate; a running run is asked to stop.
	q := e.createRun("product-agent", in)
	if st := e.expect(e.do("POST", "/agent-runs/"+q+"/cancel", nil), 200)["status"]; st != "cancelled" {
		t.Fatalf("queued cancel: %v", st)
	}
	r := e.createRun("product-agent", in)
	rid, tok := e.claim()
	if rid != r {
		t.Fatalf("claimed %s, want %s", rid, r)
	}
	if c := e.expect(e.do("POST", "/agent-runs/"+r+"/cancel", nil), 200); c["cancelRequested"] != true || c["status"] != "running" {
		t.Fatalf("running cancel: %v", c)
	}
	if hb := e.expect(e.runtimeDo(tok, "POST", "/runtime/v1/runs/"+r+"/heartbeat", nil), 200); hb["cancelRequested"] != true {
		t.Fatalf("heartbeat should report cancellation: %v", hb)
	}
	e.expect(e.runtimeDo(tok, "POST", "/runtime/v1/runs/"+r+"/fail", map[string]any{"error": "cancelled by user", "cancelled": true}), 204)
	if st := e.expect(e.do("GET", "/agent-runs/"+r, nil), 200)["status"]; st != "cancelled" {
		t.Fatalf("after runtime ack: %v", st)
	}

	// Retryable failures requeue while attempts remain.
	s := e.createRun("product-agent", in)
	_, tok = e.claim()
	e.expect(e.runtimeDo(tok, "POST", "/runtime/v1/runs/"+s+"/fail", map[string]any{"error": "provider down", "retryable": true}), 204)
	if st := e.expect(e.do("GET", "/agent-runs/"+s, nil), 200)["status"]; st != "queued" {
		t.Fatalf("retryable failure should requeue: %v", st)
	}

	// A cancelled run whose runtime dies is cancelled, never resumed.
	c, _ := e.claim()
	if c != s {
		t.Fatalf("claimed %s, want %s", c, s)
	}
	e.expect(e.do("POST", "/agent-runs/"+s+"/cancel", nil), 200)
	if _, err := e.db.Pool.Exec(context.Background(), `UPDATE agent_runs SET lease_expires_at = now() - interval '1 second' WHERE id = $1`, s); err != nil {
		t.Fatal(err)
	}
	_, _ = e.queue.Reap(context.Background())
	if st := e.expect(e.do("GET", "/agent-runs/"+s, nil), 200)["status"]; st != "cancelled" {
		t.Fatalf("cancel-requested run with expired lease: %v", st)
	}
}
