package httpapi_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"

	"wise/apps/api/internal/agents"
	"wise/apps/api/internal/tasks"
)

// pendingCommands returns the undelivered outbox commands of a workflow run.
func (e *env) pendingCommands(runID uuid.UUID) []string {
	e.t.Helper()
	rows, err := e.db.Pool.Query(context.Background(),
		`SELECT kind || ':' || payload::text FROM workflow_commands WHERE workflow_run_id = $1 AND processed_at IS NULL ORDER BY created_at`, runID)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func (e *env) newTask(title string) (taskID, runID uuid.UUID) {
	e.t.Helper()
	pid := e.expect(e.do("POST", "/projects", map[string]string{"name": "Proj " + uuid.NewString()[:8]}), 201)["id"].(string)
	d := e.expect(e.do("POST", "/projects/"+pid+"/tasks", map[string]any{"title": title, "description": "details"}), 201)
	wf := d["workflow"].(map[string]any)
	if wf["status"] != "pending" || d["task"].(map[string]any)["status"] != "in_progress" {
		e.t.Fatalf("new task: %v", d)
	}
	return uuid.MustParse(d["task"].(map[string]any)["id"].(string)), uuid.MustParse(wf["id"].(string))
}

func (e *env) saveArtifact(runID, taskID uuid.UUID, kind string, content map[string]any) {
	e.t.Helper()
	b, _ := json.Marshal(content)
	if _, err := e.tasks.SaveArtifact(context.Background(), runID, taskID, kind, b, nil); err != nil {
		e.t.Fatal(err)
	}
}

func TestTaskWorkflowAndApprovals(t *testing.T) {
	e := setup(t)
	e.login()
	ctx := context.Background()
	taskID, runID := e.newTask("Weekly summary page")

	if got := e.pendingCommands(runID); len(got) != 1 || got[0] != "start:{}" {
		t.Fatalf("start must be in the outbox: %v", got)
	}
	e.expect(e.do("POST", "/tasks/"+taskID.String()+"/workflows", nil), 409) // one active workflow per task
	list := e.expect(e.do("GET", "/tasks", nil), 200)["items"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["projectName"] == "" {
		t.Fatalf("tasks: %v", list)
	}

	// The workflow (simulated through the same mirror the activities use) produces
	// artifacts and opens the scope gate.
	e.saveArtifact(runID, taskID, tasks.KindProductAnalysis, map[string]any{"summary": "s"})
	e.saveArtifact(runID, taskID, tasks.KindRequirements, map[string]any{"title": "r1"})
	ap1, err := e.tasks.CreateApproval(ctx, runID, taskID, tasks.GateScope, nil)
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := e.tasks.CreateApproval(ctx, runID, taskID, tasks.GateScope, nil); again.ID != ap1.ID {
		t.Fatal("CreateApproval must be idempotent for unchanged artifacts")
	}
	inbox := e.expect(e.do("GET", "/approvals?status=pending", nil), 200)
	if inbox["pendingCount"].(float64) != 1 || inbox["items"].([]any)[0].(map[string]any)["taskTitle"] != "Weekly summary page" {
		t.Fatalf("inbox: %v", inbox)
	}
	detail := e.expect(e.do("GET", "/approvals/"+ap1.ID.String(), nil), 200)
	if len(detail["artifacts"].([]any)) != 2 {
		t.Fatalf("approval detail must carry both pinned artifacts: %v", detail)
	}

	base := "/approvals/" + ap1.ID.String()
	e.expect(e.do("POST", base+"/reject", map[string]any{"combinedHash": ap1.CombinedHash}), 400) // comment required
	wrong := map[string]any{"combinedHash": "0000000000000000000000000000000000000000000000000000000000000000"}
	e.expect(e.do("POST", base+"/approve", wrong), 409)

	// A new requirements version supersedes the pending approval; it can no longer be approved.
	e.saveArtifact(runID, taskID, tasks.KindRequirements, map[string]any{"title": "r2"})
	if st := e.expect(e.do("GET", base, nil), 200)["approval"].(map[string]any)["status"]; st != "superseded" {
		t.Fatalf("approval should be superseded, is %v", st)
	}
	e.expect(e.do("POST", base+"/approve", map[string]any{"combinedHash": ap1.CombinedHash}), 409)

	ap2, err := e.tasks.CreateApproval(ctx, runID, taskID, tasks.GateScope, []string{"note"})
	if err != nil {
		t.Fatal(err)
	}
	if ap2.Round != 2 || ap2.CombinedHash == ap1.CombinedHash {
		t.Fatalf("second round must pin the new version: %+v", ap2)
	}

	// Tampered content is detected even with the right hash.
	if _, err := e.db.Pool.Exec(ctx, `UPDATE artifacts SET content = '{"title":"sneaky"}' WHERE task_id = $1 AND kind = 'requirements' AND version = 2`, taskID); err != nil {
		t.Fatal(err)
	}
	e.expect(e.do("POST", "/approvals/"+ap2.ID.String()+"/approve", map[string]any{"combinedHash": ap2.CombinedHash}), 409)
	if _, err := e.db.Pool.Exec(ctx, `UPDATE artifacts SET content = '{"title":"r2"}' WHERE task_id = $1 AND kind = 'requirements' AND version = 2`, taskID); err != nil {
		t.Fatal(err)
	}

	approved := e.expect(e.do("POST", "/approvals/"+ap2.ID.String()+"/approve", map[string]any{"combinedHash": ap2.CombinedHash, "comment": "ship it"}), 200)
	if approved["status"] != "approved" || approved["decidedBy"] == nil {
		t.Fatalf("approve: %v", approved)
	}
	e.expect(e.do("POST", "/approvals/"+ap2.ID.String()+"/approve", map[string]any{"combinedHash": ap2.CombinedHash}), 409) // once only
	cmds := e.pendingCommands(runID)
	if len(cmds) != 2 || cmds[1] != `decision:{"approvalId": "`+ap2.ID.String()+`"}` {
		t.Fatalf("decision must be in the outbox: %v", cmds)
	}
	var audited int
	_ = e.db.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE action = 'approval.approve' AND entity_id = $1`, ap2.ID).Scan(&audited)
	if audited != 1 {
		t.Fatal("approval decision must be audited")
	}
	d, err := e.tasks.ResolveApproval(ctx, ap2.ID)
	if err != nil || d.Status != "approved" || d.Comment != "ship it" {
		t.Fatalf("resolve: %+v %v", d, err)
	}

	evs := e.expect(e.do("GET", "/workflows/"+runID.String()+"/events", nil), 200)["items"].([]any)
	var types []string
	for i, ev := range evs {
		m := ev.(map[string]any)
		if int(m["seq"].(float64)) != i+1 {
			t.Fatalf("event seq must be gap-free: %v", evs)
		}
		types = append(types, m["type"].(string))
	}
	for _, want := range []string{"workflow.created", "artifact.created", "approval.requested", "approval.superseded", "approval.approved"} {
		if !contains(types, want) {
			t.Fatalf("missing event %s in %v", want, types)
		}
	}

	// Retry only from awaiting_retry.
	e.expect(e.do("POST", "/workflows/"+runID.String()+"/retry", nil), 409)
	msg := "agent failed"
	if err := e.tasks.SetState(ctx, runID, tasks.RunAwaitingRetry, tasks.StagePlanning, &msg); err != nil {
		t.Fatal(err)
	}
	e.expect(e.do("POST", "/workflows/"+runID.String()+"/retry", nil), 202)

	task := e.expect(e.do("GET", "/tasks/"+taskID.String(), nil), 200)
	if len(task["artifacts"].([]any)) != 2 || task["workflow"].(map[string]any)["error"] != msg {
		t.Fatalf("task detail: %v", task)
	}

	e.expect(e.do("POST", "/tasks/"+taskID.String()+"/cancel", nil), 202)
	cmds = e.pendingCommands(runID)
	if cmds[len(cmds)-1] != "cancel:{}" {
		t.Fatalf("cancel must be in the outbox: %v", cmds)
	}
	if err := e.tasks.MarkCancelled(ctx, runID, "cancelled by user"); err != nil {
		t.Fatal(err)
	}
	task = e.expect(e.do("GET", "/tasks/"+taskID.String(), nil), 200)
	if task["task"].(map[string]any)["status"] != "cancelled" || task["workflow"].(map[string]any)["status"] != "cancelled" {
		t.Fatalf("cancelled task: %v", task)
	}
	e.expect(e.do("POST", "/tasks/"+taskID.String()+"/cancel", nil), 409)
}

func TestBearerTokenCannotDecide(t *testing.T) {
	e := setup(t)
	e.login()
	taskID, runID := e.newTask("Token boundary")
	e.saveArtifact(runID, taskID, tasks.KindProductAnalysis, map[string]any{"summary": "s"})
	e.saveArtifact(runID, taskID, tasks.KindRequirements, map[string]any{"title": "r"})
	ap, err := e.tasks.CreateApproval(context.Background(), runID, taskID, tasks.GateScope, nil)
	if err != nil {
		t.Fatal(err)
	}
	secret := e.expect(e.do("POST", "/auth/tokens", map[string]string{"name": "automation"}), 201)["secret"].(string)
	e.expect(e.do("POST", "/auth/logout", nil), 204)
	e.csrf, e.bearer = "", secret

	e.expect(e.do("GET", "/approvals", nil), 200) // reading is fine
	e.expect(e.do("POST", "/approvals/"+ap.ID.String()+"/approve", map[string]any{"combinedHash": ap.CombinedHash}), 403)
	e.expect(e.do("POST", "/approvals/"+ap.ID.String()+"/reject", map[string]any{"combinedHash": ap.CombinedHash, "comment": "nope"}), 403)
	e.expect(e.do("POST", "/tasks/"+taskID.String()+"/cancel", nil), 403)
}

func TestWorkflowAgentRunsAreIdempotent(t *testing.T) {
	e := setup(t)
	e.login()
	taskID, runID := e.newTask("Idempotency")
	in := map[string]any{"request": "Build it"}
	opts := agents.RunOptions{WorkflowRunID: &runID, IdempotencyKey: "wf:" + runID.String() + ":PRODUCT_ANALYSIS:a1:r0"}
	r1, err := e.agents.Enqueue(context.Background(), "product-agent", in, opts)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := e.agents.Enqueue(context.Background(), "product-agent", in, opts)
	if err != nil {
		t.Fatal(err)
	}
	if r1.ID != r2.ID || r1.WorkflowRunID == nil || *r1.WorkflowRunID != runID {
		t.Fatalf("same key must return the same run: %v %v", r1.ID, r2.ID)
	}

	// Cancelling the task stops the workflow's agents right away.
	e.expect(e.do("POST", "/tasks/"+taskID.String()+"/cancel", nil), 202)
	if st := e.expect(e.do("GET", "/agent-runs/"+r1.ID.String(), nil), 200)["status"]; st != "cancelled" {
		t.Fatalf("queued agent run should be cancelled, is %v", st)
	}
}

func TestWorkflowEventsReachHubViaNotify(t *testing.T) {
	e := setup(t)
	e.login()
	_, runID := e.newTask("Live events")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, unsub := e.hub.SubscribeWorkflow(&runID)
	defer unsub()
	go e.hub.ListenWorkflowEvents(ctx, e.db, slog.New(slog.NewTextHandler(io.Discard, nil)))
	time.Sleep(300 * time.Millisecond) // let LISTEN start

	if err := e.tasks.SetState(context.Background(), runID, tasks.RunRunning, tasks.StageProductAnalysis, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-ch:
		if ev.WorkflowRunID != runID || ev.Type != "workflow.status" {
			t.Fatalf("unexpected event %+v", ev)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("NOTIFY did not reach the hub")
	}
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
