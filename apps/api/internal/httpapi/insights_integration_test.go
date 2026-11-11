package httpapi_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
)

func TestInsightsAttributeRejectionsAndSkipEvalRuns(t *testing.T) {
	e := setup(t)
	e.login()
	ctx := context.Background()
	pid := e.expect(e.do("POST", "/projects", map[string]any{"name": "Ins"}), 201)["id"].(string)
	taskID, wfID := e.newTask("Measure me")
	product := e.createRunInProject("product-agent", pid)
	planner := e.createRunInProject("product-agent", pid)
	architect := e.createRunInProject("product-agent", pid)
	evalRun := e.createRunInProject("product-agent", pid)
	q := func(sql string, args ...any) {
		t.Helper()
		if _, err := e.db.Pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	// Re-label runs as other agents (their inputs differ; only attribution matters here).
	relabel := `UPDATE agent_runs SET agent_name = $2,
	  agent_definition_id = (SELECT id FROM agent_definitions WHERE name = $2 AND is_current) WHERE id = ANY($1::uuid[])`
	q(relabel, []string{planner, evalRun}, "planner-agent")
	q(relabel, []string{architect}, "architect-agent")
	q(`UPDATE agent_runs SET status = 'succeeded', started_at = now() - interval '10 seconds', finished_at = now() WHERE id = ANY($1::uuid[])`,
		[]string{product, planner, architect})
	q(`UPDATE agent_runs SET status = 'failed', error = 'boom 42', purpose = 'eval' WHERE id = $1`, evalRun)
	plan, _ := json.Marshal(map[string]any{"type": "pipeline", "templateName": "Engineering", "steps": []map[string]any{
		{"id": "analysis", "agent": "product-agent", "enabled": true},
		{"id": "planning", "agent": "planner-agent", "enabled": true},
		{"id": "architecture", "agent": "architect-agent", "enabled": true, "review": true, "reviseFrom": "planning"},
	}})
	q(`UPDATE workflow_runs SET config = $2 WHERE id = $1`, wfID, plan)
	for stage, run := range map[string]string{"analysis": product, "planning": planner, "architecture": architect} {
		q(`INSERT INTO workflow_steps (id, workflow_run_id, stage, status, agent_run_id) VALUES ($1, $2, $3, 'succeeded', $4)`,
			uuid.New(), wfID, stage, run)
	}
	q(`INSERT INTO approvals (id, workflow_run_id, task_id, gate, status, artifacts, combined_hash, comment, decided_at)
	   VALUES ($1, $2, $3, 'architecture', 'rejected', '[]', 'h', 'Plan ignores migrations', now())`, uuid.New(), wfID, taskID)

	rep := e.expect(e.do("GET", "/insights?window=7d", nil), 200)
	by := map[string]map[string]any{}
	for _, a := range rep["agents"].([]any) {
		m := a.(map[string]any)
		by[m["agent"].(string)] = m
	}
	// A rejection at the architecture gate re-runs planning..architecture: those agents are blamed, analysis is not.
	if by["planner-agent"]["rejected"] != 1.0 || by["architect-agent"]["rejected"] != 1.0 || by["product-agent"]["reviewed"] != 0.0 {
		t.Fatalf("attribution: %v", by)
	}
	// The eval replay is not counted anywhere.
	if by["planner-agent"]["runs"] != 1.0 || by["planner-agent"]["failed"] != 0.0 || len(rep["failures"].([]any)) != 0 {
		t.Fatalf("eval run counted: %v %v", by["planner-agent"], rep["failures"])
	}
	cs := rep["comments"].([]any)
	if len(cs) != 2 || cs[0].(map[string]any)["text"] != "Plan ignores migrations" {
		t.Fatalf("comments: %v", cs)
	}
	d := e.expect(e.do("GET", "/insights/agents/planner-agent?window=30d", nil), 200)
	if len(d["versions"].([]any)) != 1 || len(d["comments"].([]any)) != 1 || len(d["days"].([]any)) != 1 {
		t.Fatalf("detail: %v", d)
	}
}
