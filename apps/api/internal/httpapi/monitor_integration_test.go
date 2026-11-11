package httpapi_test

import (
	"context"
	"testing"
)

func TestMonitorShowsQueueStuckRunsAndWaitingWork(t *testing.T) {
	e := setup(t)
	e.login()
	ctx := context.Background()
	pid := e.expect(e.do("POST", "/projects", map[string]any{"name": "Mon"}), 201)["id"].(string)
	running := e.createRunInProject("product-agent", pid)
	claimed, _ := e.claim()
	if claimed != running {
		t.Fatalf("claimed %s", claimed)
	}
	queued := e.createRunInProject("product-agent", pid)
	if _, err := e.db.Pool.Exec(ctx, "UPDATE agent_runs SET lease_expires_at = now() - interval '1 minute' WHERE id = $1", running); err != nil {
		t.Fatal(err)
	}
	_, wf := e.newTask("Waiting task")
	if _, err := e.db.Pool.Exec(ctx, "UPDATE workflow_runs SET status = 'awaiting_retry', current_stage = 'implement', error = 'tests failed' WHERE id = $1", wf); err != nil {
		t.Fatal(err)
	}
	m := e.expect(e.do("GET", "/monitor", nil), 200)
	q, r, w := m["queued"].([]any), m["running"].([]any), m["waiting"].([]any)
	if len(q) != 1 || q[0].(map[string]any)["id"] != queued || q[0].(map[string]any)["projectName"] != "Mon" {
		t.Fatalf("queued: %v", q)
	}
	if len(r) != 1 || r[0].(map[string]any)["runtime"] != "native" || r[0].(map[string]any)["stuck"] == "" || m["stuck"] != 1.0 {
		t.Fatalf("running: %v %v", r, m["stuck"])
	}
	if len(w) != 1 || w[0].(map[string]any)["stage"] != "implement" || w[0].(map[string]any)["taskTitle"] != "Waiting task" {
		t.Fatalf("waiting: %v", w)
	}
}
