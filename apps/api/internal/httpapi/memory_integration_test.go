package httpapi_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"wise/apps/api/internal/db/dbgen"
)

func TestMemoryLifecycleExtractionAndInjection(t *testing.T) {
	e := setup(t)
	e.login()
	ctx := context.Background()
	pid := e.expect(e.do("POST", "/projects", map[string]string{"name": "Mem " + uuid.NewString()[:6]}), 201)["id"].(string)

	// A memory the user writes applies at once; validation and scope rules hold.
	rule := e.expect(e.do("POST", "/memories", map[string]any{"scope": "developer", "kind": "rule", "category": "api",
		"title": "Errors use RFC 7807", "body": "Every HTTP error response is an application/problem+json document."}), 201)
	if rule["status"] != "active" || rule["source"] != "manual" || rule["version"].(float64) != 1 {
		t.Fatalf("manual memory: %v", rule)
	}
	e.expect(e.do("POST", "/memories", map[string]any{"scope": "project", "kind": "fact", "title": "x", "body": "y"}), 400) // needs a project
	e.expect(e.do("POST", "/memories", map[string]any{"scope": "developer", "kind": "wish", "title": "x", "body": "y"}), 400)
	fact := e.expect(e.do("POST", "/memories", map[string]any{"scope": "project", "projectId": pid, "kind": "decision", "category": "architecture",
		"title": "Queue via Postgres outbox", "body": "Webhook deliveries are queued in a Postgres outbox table, not Redis."}), 201)
	if fact["projectName"] == nil {
		t.Fatalf("project name: %v", fact)
	}

	// Agent runs start with the rules and the relevant knowledge; the context is frozen on the run.
	runID := e.expect(e.do("POST", "/agents/product-agent/runs", map[string]any{
		"input": map[string]any{"request": "Add retries to webhook deliveries in the outbox"}, "projectId": pid,
	}), 202)["id"].(string)
	claimedID, tok := e.claim()
	if claimedID != runID {
		t.Fatalf("claimed %s", claimedID)
	}
	e.expect(e.runtimeDo(tok, "POST", "/runtime/v1/runs/"+runID+"/model-turn", map[string]any{"messages": []map[string]string{{"role": "user", "content": "go"}}}), 200)
	e.model.mu.Lock()
	sys := e.model.lastSystem
	e.model.mu.Unlock()
	if !strings.Contains(sys, "Errors use RFC 7807") || !strings.Contains(sys, "Queue via Postgres outbox") || !strings.Contains(sys, "## Wise context") {
		t.Fatalf("system prompt lacks the context:\n%s", sys)
	}
	run := e.expect(e.do("GET", "/agent-runs/"+runID, nil), 200)
	rc := run["context"].(map[string]any)
	if len(rc["rules"].([]any)) != 1 || len(rc["relevant"].([]any)) != 1 {
		t.Fatalf("run context: %v", rc)
	}
	e.expect(e.runtimeDo(tok, "POST", "/runtime/v1/runs/"+runID+"/complete", map[string]any{"output": validProductOutput}), 204)

	// Agents can search memory for their run's project.
	runID2 := e.createRunInProject("product-agent", pid)
	_, tok2 := e.claim()
	got := e.expect(e.runtimeDo(tok2, "GET", "/runtime/v1/runs/"+runID2+"/memory/search?q=outbox+webhooks", nil), 200)["items"].([]any)
	if len(got) == 0 || got[0].(map[string]any)["title"] != "Queue via Postgres outbox" {
		t.Fatalf("memory search: %v", got)
	}
	e.expect(e.runtimeDo(tok2, "POST", "/runtime/v1/runs/"+runID2+"/complete", map[string]any{"output": validProductOutput}), 204)

	// A review comment proposes memories through the memory agent; they wait in the inbox.
	task := e.expect(e.do("POST", "/projects/"+pid+"/tasks", map[string]any{"title": "Webhook retries", "start": false}), 201)["task"].(map[string]any)
	taskID := uuid.MustParse(task["id"].(string))
	comment := "Never retry forever: cap webhook retries at 5 attempts, and always log the final failure."
	e.memory.OnApprovalDecided(ctx, dbgen.Approval{ID: uuid.New(), TaskID: taskID, Status: "rejected", Comment: &comment, Artifacts: []byte("[]")})
	memRunID, memTok := e.claim()
	claimed := e.expect(e.do("GET", "/agent-runs/"+memRunID, nil), 200)
	if claimed["agentName"] != "memory-agent" || claimed["input"].(map[string]any)["feedback"] != comment {
		t.Fatalf("memory run: %v", claimed)
	}
	if ex := claimed["input"].(map[string]any)["existing_memories"].([]any); len(ex) == 0 {
		t.Fatal("the agent sees existing memories so it does not repeat them")
	}
	if claimed["context"] != nil && claimed["context"].(map[string]any)["text"] != nil {
		t.Fatal("the memory agent itself gets no memory context (skip list)")
	}
	cand := map[string]any{"scope": "project", "kind": "rule", "category": "api", "title": "Cap webhook retries at 5",
		"body": "Webhook deliveries are retried at most 5 times; the final failure is logged.", "rationale": "Review: 'Never retry forever'"}
	dup := map[string]any{"scope": "project", "kind": "decision", "category": "architecture", "title": "Queue via Postgres outbox",
		"body": "Webhook deliveries are queued in a Postgres outbox table, not Redis.", "rationale": "Report"}
	e.expect(e.runtimeDo(memTok, "POST", "/runtime/v1/runs/"+memRunID+"/complete", map[string]any{"output": map[string]any{"memories": []any{cand, dup}}}), 204)
	var inbox []any
	for i := 0; i < 50 && len(inbox) < 2; i++ { // candidates are created right after the run finishes
		inbox = e.expect(e.do("GET", "/memories?status=candidate", nil), 200)["items"].([]any)
		time.Sleep(40 * time.Millisecond)
	}
	if len(inbox) != 2 || e.expect(e.do("GET", "/memories/inbox", nil), 200)["candidates"].(float64) != 2 {
		t.Fatalf("inbox: %v", inbox)
	}
	var candID string
	for _, it := range inbox {
		m := it.(map[string]any)
		if m["title"] == "Queue via Postgres outbox" {
			if m["duplicateOf"] != fact["id"] {
				t.Fatalf("duplicate not flagged: %v", m)
			}
			e.expect(e.do("POST", "/memories/"+m["id"].(string)+"/reject", nil), 200)
		} else {
			candID = m["id"].(string)
			if m["source"] != "review_feedback" || m["sourceRefs"].(map[string]any)["taskId"] != taskID.String() {
				t.Fatalf("candidate source: %v", m)
			}
		}
	}
	// Candidates do not reach agents until approved.
	if pack, _ := e.memory.BuildPack(ctx, &[]uuid.UUID{uuid.MustParse(pid)}[0], []byte(`{"request":"webhook retries"}`)); strings.Contains(pack.Text, "Cap webhook retries") {
		t.Fatal("a candidate must not be injected")
	}

	// Approve with an edit (a new version); then it applies; history and restore.
	edited := e.expect(e.do("POST", "/memories/"+candID+"/approve", map[string]any{"scope": "project", "projectId": pid, "kind": "rule",
		"category": "api", "title": "Cap webhook retries at 5 attempts", "body": "Retry webhook deliveries at most 5 times and log the final failure."}), 200)
	if edited["status"] != "active" || edited["version"].(float64) != 2 || edited["decidedAt"] == nil {
		t.Fatalf("approved: %v", edited)
	}
	e.expect(e.do("POST", "/memories/"+candID+"/approve", nil), 409) // not a candidate anymore
	if pack, _ := e.memory.BuildPack(ctx, &[]uuid.UUID{uuid.MustParse(pid)}[0], []byte(`{"request":"anything"}`)); !strings.Contains(pack.Text, "Cap webhook retries at 5 attempts") {
		t.Fatalf("approved rules always apply:\n%s", pack.Text)
	}
	if v := e.expect(e.do("GET", "/memories/"+candID+"/versions", nil), 200)["items"].([]any); len(v) != 2 {
		t.Fatalf("versions: %v", v)
	}
	restored := e.expect(e.do("POST", "/memories/"+candID+"/versions/1/restore", nil), 200)
	if restored["title"] != "Cap webhook retries at 5" || restored["version"].(float64) != 3 {
		t.Fatalf("restore: %v", restored)
	}

	// Archived memories stop applying.
	e.expect(e.do("POST", "/memories/"+rule["id"].(string)+"/archive", nil), 200)
	if pack, _ := e.memory.BuildPack(ctx, nil, []byte(`{"request":"errors"}`)); strings.Contains(pack.Text, "RFC 7807") {
		t.Fatal("archived memories must not apply")
	}
	e.expect(e.do("POST", "/memories/"+rule["id"].(string)+"/reactivate", nil), 200)

	// Manual extraction needs something to learn from.
	e.expect(e.do("POST", "/tasks/"+task["id"].(string)+"/extract-memories", nil), 409)

	// API tokens cannot change memory.
	secret := e.expect(e.do("POST", "/auth/tokens", map[string]string{"name": "auto"}), 201)["secret"].(string)
	e.expect(e.do("POST", "/auth/logout", nil), 204)
	e.csrf, e.bearer = "", secret
	e.expect(e.do("POST", "/memories", map[string]any{"scope": "developer", "kind": "fact", "title": "x", "body": "y"}), 403)
	e.expect(e.do("GET", "/memories", nil), 200)
}
