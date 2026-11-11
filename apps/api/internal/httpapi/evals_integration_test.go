package httpapi_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// proposeOne runs an analysis whose agent returns the given proposals and returns the stored proposal ids.
func (e *env) proposeOne(proposals ...any) []string {
	e.t.Helper()
	a := e.expect(e.do("POST", "/improvements/analyses", nil), 202)
	run, tok := e.claim()
	e.expect(e.runtimeDo(tok, "POST", "/runtime/v1/runs/"+run+"/complete", map[string]any{"output": map[string]any{"proposals": proposals}}), 204)
	done := e.waitAnalysis(a["id"].(string))
	if done["status"] != "succeeded" {
		e.t.Fatalf("analysis: %v", done)
	}
	var ids []string
	for _, it := range e.expect(e.do("GET", "/improvements/proposals", nil), 200)["items"].([]any) {
		ids = append(ids, it.(map[string]any)["id"].(string))
	}
	return ids
}

func TestEvaluatePromptProposal(t *testing.T) {
	e := setup(t)
	e.login()
	ctx := context.Background()
	pid := e.expect(e.do("POST", "/projects", map[string]any{"name": "Eval"}), 201)["id"].(string)
	var runs []string
	for i := 0; i < 2; i++ {
		id := e.createRunInProject("product-agent", pid)
		_, tok := e.claim()
		e.expect(e.runtimeDo(tok, "POST", "/runtime/v1/runs/"+id+"/complete", map[string]any{"output": validProductOutput}), 204)
		runs = append(runs, id)
	}
	e.expect(e.do("PUT", "/feedback", map[string]any{"targetType": "run", "targetId": runs[0], "rating": "up"}), 200)
	fb := e.expect(e.do("PUT", "/feedback", map[string]any{"targetType": "run", "targetId": runs[1], "rating": "down", "note": "no risks listed"}), 200)

	ids := e.proposeOne(map[string]any{"kind": "prompt", "target": "product-agent", "title": "List risks", "rationale": "Your note.",
		"evidence":     map[string]any{"feedback_ids": []string{fb["id"].(string)}},
		"prompt_edits": []any{map[string]any{"old": "", "new": "Always list at least one risk."}}})
	pidl := ids[0]

	cases := e.expect(e.do("GET", "/agents/product-agent/eval-cases", nil), 200)["items"].([]any)
	kinds := map[string]bool{}
	for _, c := range cases {
		kinds[c.(map[string]any)["kind"].(string)] = true
	}
	if len(cases) != 2 || !kinds["golden"] || !kinds["must_fix"] {
		t.Fatalf("cases from your ratings: %v", cases)
	}
	plan := e.expect(e.do("GET", "/improvements/proposals/"+pidl+"/eval-plan", nil), 200)
	if plan["evaluable"] != true || plan["cases"] != 2.0 || plan["agent"] != "product-agent" {
		t.Fatalf("plan: %v", plan)
	}
	er := e.expect(e.do("POST", "/improvements/proposals/"+pidl+"/evaluate", map[string]any{"maxCases": 2, "costCapUsd": 1}), 202)
	e.expect(e.do("POST", "/improvements/proposals/"+pidl+"/evaluate", nil), 409)
	if p := e.expect(e.do("GET", "/improvements/proposals/"+pidl, nil), 200); p["status"] != "evaluating" {
		t.Fatalf("status %v", p["status"])
	}

	// Four replays: baseline and candidate per case, on different definition versions, marked as evaluation runs.
	defs := map[string]int{}
	for i := 0; i < 4; i++ {
		id, tok := e.claim()
		r := e.expect(e.do("GET", "/agent-runs/"+id, nil), 200)
		if r["purpose"] != "eval" || r["agentName"] != "product-agent" {
			t.Fatalf("replay: %v", r)
		}
		defs[r["agentDefinitionId"].(string)]++
		e.expect(e.runtimeDo(tok, "POST", "/runtime/v1/runs/"+id+"/complete", map[string]any{"output": validProductOutput}), 204)
	}
	if len(defs) != 2 {
		t.Fatalf("baseline and candidate definitions: %v", defs)
	}
	// Judges see A and B only.
	for i := 0; i < 2; i++ {
		var id, tok string
		for k := 0; k < 50 && id == ""; k++ {
			c := e.runtimeDo(runtimeToken, "POST", "/runtime/v1/runs/claim", map[string]string{"runtimeId": "test-rt"})
			if c.status == 200 {
				m := c.body
				if run, ok := m["run"].(map[string]any); ok {
					id, tok = run["id"].(string), m["runToken"].(string)
				}
			}
			if id == "" {
				time.Sleep(30 * time.Millisecond)
			}
		}
		if id == "" {
			t.Fatal("no judge run queued")
		}
		j := e.expect(e.do("GET", "/agent-runs/"+id, nil), 200)
		in, _ := json.Marshal(j["input"])
		if j["agentName"] != "eval-judge-agent" || j["purpose"] != "eval" || !strings.Contains(string(in), "output_a") || strings.Contains(string(in), "baseline") {
			t.Fatalf("judge: %s", in)
		}
		e.expect(e.runtimeDo(tok, "POST", "/runtime/v1/runs/"+id+"/complete", map[string]any{"output": map[string]any{"score_a": 3, "score_b": 3, "reasoning": "equal"}}), 204)
	}
	var ev map[string]any
	for k := 0; k < 100; k++ {
		ev = e.expect(e.do("GET", "/improvements/proposals/"+pidl+"/evals", nil), 200)["items"].([]any)[0].(map[string]any)
		if ev["status"] != "running" {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	sum, _ := ev["summary"].(map[string]any)
	if ev["id"] != er["id"] || ev["status"] != "succeeded" || sum["judged"] != 2.0 || sum["same"] != 2.0 || len(ev["results"].([]any)) != 2 {
		t.Fatalf("evaluation: %v", ev)
	}
	if p := e.expect(e.do("GET", "/improvements/proposals/"+pidl, nil), 200); p["status"] != "evaluated" {
		t.Fatalf("status %v", p["status"])
	}

	// Replays stay out of the run list, insights and the version history.
	for _, it := range e.expect(e.do("GET", "/agent-runs", nil), 200)["items"].([]any) {
		if r := it.(map[string]any); r["purpose"] == "eval" {
			t.Fatalf("run list shows a replay: %v", r["id"])
		}
	}
	ins := e.expect(e.do("GET", "/insights?window=7d", nil), 200)["agents"].([]any)
	for _, a := range ins {
		if m := a.(map[string]any); m["agent"] == "product-agent" && m["runs"] != 2.0 {
			t.Fatalf("insights count replays: %v", m)
		}
	}
	vers := e.expect(e.do("GET", "/agents/product-agent/versions", nil), 200)
	b, _ := json.Marshal(vers)
	for d := range defs {
		var cur string
		_ = e.db.Pool.QueryRow(ctx, "SELECT id::text FROM agent_definitions WHERE name='product-agent' AND is_current").Scan(&cur)
		if d != cur && strings.Contains(string(b), d) {
			t.Fatal("the candidate is listed as a version")
		}
	}
}

func TestEvaluationStopsAtTheCostCap(t *testing.T) {
	e := setup(t)
	e.login()
	ctx := context.Background()
	pid := e.expect(e.do("POST", "/projects", map[string]any{"name": "Cap"}), 201)["id"].(string)
	var runs []string
	for i := 0; i < 3; i++ {
		id := e.createRunInProject("product-agent", pid)
		_, tok := e.claim()
		e.expect(e.runtimeDo(tok, "POST", "/runtime/v1/runs/"+id+"/complete", map[string]any{"output": validProductOutput}), 204)
		e.expect(e.do("PUT", "/feedback", map[string]any{"targetType": "run", "targetId": id, "rating": "up", "note": "good"}), 200)
		runs = append(runs, id)
	}
	ids := e.proposeOne(map[string]any{"kind": "prompt", "target": "product-agent", "title": "t", "rationale": "r",
		"evidence": map[string]any{"run_ids": runs}, "prompt_edits": []any{map[string]any{"old": "", "new": "Add a summary line."}}})
	e.expect(e.do("POST", "/improvements/proposals/"+ids[0]+"/evaluate", map[string]any{"maxCases": 3, "costCapUsd": 0.05}), 202)
	// Two cases start (four replays); the third waits. The replays turn out expensive.
	// Once spending passes the cap, queued replays are cancelled: claim until none is left.
	for i := 0; i < 4; i++ {
		c := e.runtimeDo(runtimeToken, "POST", "/runtime/v1/runs/claim", map[string]string{"runtimeId": "test-rt"})
		if c.status == 204 {
			break
		}
		run, _ := c.body["run"].(map[string]any)
		id, tok := run["id"].(string), c.body["runToken"].(string)
		if _, err := e.db.Pool.Exec(ctx, "UPDATE agent_runs SET total_cost_usd = 0.02 WHERE id = $1", id); err != nil {
			t.Fatal(err)
		}
		out := any(validProductOutput)
		if run["agentName"] == "eval-judge-agent" {
			out = map[string]any{"score_a": 3, "score_b": 3, "reasoning": "same"}
		}
		e.expect(e.runtimeDo(tok, "POST", "/runtime/v1/runs/"+id+"/complete", map[string]any{"output": out}), 204)
		time.Sleep(20 * time.Millisecond)
	}
	var ev map[string]any
	for k := 0; k < 100; k++ {
		ev = e.expect(e.do("GET", "/improvements/proposals/"+ids[0]+"/evals", nil), 200)["items"].([]any)[0].(map[string]any)
		if ev["status"] != "running" {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if ev["status"] != "cancelled" || !strings.Contains(ev["error"].(string), "cost cap") {
		t.Fatalf("evaluation: %v %v", ev["status"], ev["error"])
	}
	started := 0
	for _, r := range ev["results"].([]any) {
		if r.(map[string]any)["baselineRunId"] != nil {
			started++
		}
	}
	if started != 2 {
		t.Fatalf("cases started: %d (the third must not start over the cap)", started)
	}
	var queued int
	_ = e.db.Pool.QueryRow(ctx, "SELECT count(*) FROM agent_runs WHERE purpose = 'eval' AND status IN ('queued','running')").Scan(&queued)
	if queued != 0 {
		t.Fatalf("%d replays or judges still queued after the cap", queued)
	}
}
