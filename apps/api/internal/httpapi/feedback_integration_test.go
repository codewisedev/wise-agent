package httpapi_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
)

func TestFeedbackIsAttributedToTheAgent(t *testing.T) {
	e := setup(t)
	e.login()
	ctx := context.Background()
	pid := e.expect(e.do("POST", "/projects", map[string]any{"name": "Fb"}), 201)["id"].(string)
	runID := e.createRunInProject("product-agent", pid)
	taskID, wfID := e.newTask("Rate me")
	rid := uuid.MustParse(runID)
	content, _ := json.Marshal(map[string]any{"summary": "s"})
	art, err := e.tasks.SaveArtifact(ctx, wfID, taskID, "product_analysis", content, &rid)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := e.tasks.SaveArtifact(ctx, wfID, taskID, "quality_report", content, nil)
	if err != nil {
		t.Fatal(err)
	}

	r := e.expect(e.do("PUT", "/feedback", map[string]any{"targetType": "run", "targetId": runID, "rating": "down", "note": "missed the edge cases"}), 200)
	if r["agentName"] != "product-agent" || r["agentDefinitionId"] == nil {
		t.Fatalf("run feedback: %v", r)
	}
	a := e.expect(e.do("PUT", "/feedback", map[string]any{"targetType": "artifact", "targetId": art.ID, "rating": "up"}), 200)
	if a["agentName"] != "product-agent" || a["taskId"] != taskID.String() {
		t.Fatalf("artifact feedback: %v", a)
	}
	// Wise's own artifacts (no agent run) stay unattributed.
	p := e.expect(e.do("PUT", "/feedback", map[string]any{"targetType": "artifact", "targetId": plain.ID, "rating": "down"}), 200)
	if p["agentName"] != nil {
		t.Fatalf("unattributed: %v", p)
	}
	// One rating per target: a second one replaces it.
	e.expect(e.do("PUT", "/feedback", map[string]any{"targetType": "run", "targetId": runID, "rating": "up"}), 200)
	list := e.expect(e.do("GET", "/feedback?agent=product-agent", nil), 200)["items"].([]any)
	if len(list) != 2 || list[0].(map[string]any)["username"] != adminUser {
		t.Fatalf("list: %v", list)
	}
	e.expect(e.do("PUT", "/feedback", map[string]any{"targetType": "run", "targetId": runID, "rating": "meh"}), 400)
	e.expect(e.do("PUT", "/feedback", map[string]any{"targetType": "artifact", "targetId": uuid.NewString(), "rating": "up"}), 404)

	if r := e.do("DELETE", "/feedback?targetType=run&targetId="+runID, nil); r.status != 204 {
		t.Fatalf("clear: %d", r.status)
	}
	if n := len(e.expect(e.do("GET", "/feedback?targetType=run&targetId="+runID, nil), 200)["items"].([]any)); n != 0 {
		t.Fatalf("after clear: %d", n)
	}
	var audits int
	if err := e.db.Pool.QueryRow(ctx, "SELECT count(*) FROM audit_logs WHERE action LIKE 'feedback.%'").Scan(&audits); err != nil || audits != 5 {
		t.Fatalf("audit: %d %v", audits, err)
	}
}
