package httpapi_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestApplyAndRollBackProposals(t *testing.T) {
	e := setup(t)
	e.login()
	ctx := context.Background()
	pid := e.expect(e.do("POST", "/projects", map[string]any{"name": "Apply"}), 201)["id"].(string)
	runID := e.createRunInProject("product-agent", pid)
	_, tok := e.claim()
	e.expect(e.runtimeDo(tok, "POST", "/runtime/v1/runs/"+runID+"/complete", map[string]any{"output": validProductOutput}), 204)
	fb := e.expect(e.do("PUT", "/feedback", map[string]any{"targetType": "run", "targetId": runID, "rating": "down", "note": "too vague"}), 200)
	ev := map[string]any{"run_ids": []string{runID}, "feedback_ids": []string{fb["id"].(string)}}

	var tplID, tplName string
	var raw []byte
	if err := e.db.Pool.QueryRow(ctx, "SELECT id::text, name, definition FROM workflow_templates WHERE is_default").Scan(&tplID, &tplName, &raw); err != nil {
		t.Fatal(err)
	}
	var tpl map[string]any
	_ = json.Unmarshal(raw, &tpl)
	tpl["steps"].([]any)[0].(map[string]any)["name"] = "Product analysis, with risks"

	e.proposeOne(
		map[string]any{"kind": "prompt", "target": "product-agent", "title": "Be specific", "rationale": "r", "evidence": ev,
			"prompt_edits": []any{map[string]any{"old": "", "new": "Be specific: name files and numbers."}}},
		map[string]any{"kind": "routing", "target": "models", "title": "Other model", "rationale": "r", "evidence": ev,
			"routing_ops": []any{map[string]any{"op": "replace", "path": "/aliases/reasoning/targets", "value": []any{map[string]any{"provider": "scripted", "model": "other-model"}}}}},
		map[string]any{"kind": "template", "target": tplID, "title": "Rename", "rationale": "r", "evidence": ev, "template_definition": tpl},
		map[string]any{"kind": "memory", "target": "memory", "title": "Be specific", "rationale": "r", "evidence": ev,
			"memory_ops": []any{map[string]any{"op": "create", "scope": "developer", "kind": "rule", "category": "product", "title": "Specific analyses", "body": "Name files and numbers."}}},
	)
	byKind := map[string]string{}
	for _, it := range e.expect(e.do("GET", "/improvements/proposals", nil), 200)["items"].([]any) {
		p := it.(map[string]any)
		byKind[p["kind"].(string)] = p["id"].(string)
	}
	if len(byKind) != 4 {
		t.Fatalf("proposals: %v", byKind)
	}
	apply := func(kind string) map[string]any {
		return e.expect(e.do("POST", "/improvements/proposals/"+byKind[kind]+"/apply", nil), 200)
	}
	current := func() (id, version, prompt string, def []byte) {
		if err := e.db.Pool.QueryRow(ctx, "SELECT id::text, version, prompt, definition FROM agent_definitions WHERE name='product-agent' AND is_current").Scan(&id, &version, &prompt, &def); err != nil {
			t.Fatal(err)
		}
		return
	}

	// Prompt: a new agent version with only the prompt (and patch version) changed.
	oldID, oldVer, _, oldDef := current()
	if p := apply("prompt"); p["status"] != "applied" {
		t.Fatalf("apply: %v", p)
	}
	e.expect(e.do("POST", "/improvements/proposals/"+byKind["prompt"]+"/apply", nil), 409)
	newID, newVer, prompt, newDef := current()
	var a, b map[string]any
	_ = json.Unmarshal(oldDef, &a)
	_ = json.Unmarshal(newDef, &b)
	if newID == oldID || newVer == oldVer || !strings.HasSuffix(prompt, "Be specific: name files and numbers.\n") ||
		mustString(a["tools"]) != mustString(b["tools"]) || mustString(a["permissions"]) != mustString(b["permissions"]) {
		t.Fatalf("applied prompt: %s→%s %s→%s", oldID, newID, oldVer, newVer)
	}
	if m := e.expect(e.do("GET", "/improvements/proposals/"+byKind["prompt"]+"/monitor", nil), 200); m["appliedAt"] == nil || m["before"] == nil {
		t.Fatalf("monitor: %v", m)
	}
	e.expect(e.do("POST", "/improvements/proposals/"+byKind["prompt"]+"/rollback", nil), 200)
	if id, _, _, _ := current(); id != oldID {
		t.Fatal("rollback did not restore the previous version")
	}

	// Routing: applied through the setting; a later edit needs confirmation to roll back.
	apply("routing")
	st := e.setting("models")
	if !strings.Contains(mustString(st["value"]), "other-model") {
		t.Fatalf("models: %v", st["value"])
	}
	val := st["value"].(map[string]any)
	val["cache"] = map[string]any{"enabled": false, "ttl_hours": 1}
	e.expect(e.do("PUT", "/settings/models", map[string]any{"value": val, "version": st["version"]}), 200)
	e.expect(e.do("POST", "/improvements/proposals/"+byKind["routing"]+"/rollback", nil), 409)
	e.expect(e.do("POST", "/improvements/proposals/"+byKind["routing"]+"/rollback", map[string]any{"confirm": true}), 200)
	if strings.Contains(mustString(e.setting("models")["value"]), "other-model") {
		t.Fatal("routing not rolled back")
	}

	// Template: saved with history; rollback restores the previous definition.
	apply("template")
	vers := e.expect(e.do("GET", "/workflow-templates/"+tplID+"/versions", nil), 200)["items"].([]any)
	if len(vers) != 1 || strings.Contains(mustString(vers[0]), "with risks") {
		t.Fatalf("history: %v", vers)
	}
	e.expect(e.do("POST", "/improvements/proposals/"+byKind["template"]+"/rollback", nil), 200)
	var def []byte
	_ = e.db.Pool.QueryRow(ctx, "SELECT definition FROM workflow_templates WHERE id = $1", tplID).Scan(&def)
	if strings.Contains(string(def), "with risks") {
		t.Fatal("template not rolled back")
	}

	// Memory: approving the proposal makes it active; rollback archives it.
	ap := apply("memory")
	created := ap["applied"].(map[string]any)["created"].([]any)[0].(string)
	if m := e.expect(e.do("GET", "/memories/"+created, nil), 200); m["status"] != "active" {
		t.Fatalf("memory: %v", m["status"])
	}
	e.expect(e.do("POST", "/improvements/proposals/"+byKind["memory"]+"/rollback", nil), 200)
	if m := e.expect(e.do("GET", "/memories/"+created, nil), 200); m["status"] != "archived" {
		t.Fatalf("memory after rollback: %v", m["status"])
	}
	var audits int
	_ = e.db.Pool.QueryRow(ctx, "SELECT count(*) FROM audit_logs WHERE action IN ('improvement.applied', 'improvement.rolled_back')").Scan(&audits)
	if audits != 8 {
		t.Fatalf("audit: %d", audits)
	}
}

func mustString(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func (e *env) setting(key string) map[string]any {
	e.t.Helper()
	for _, it := range e.expect(e.do("GET", "/settings", nil), 200)["items"].([]any) {
		if m := it.(map[string]any); m["key"] == key {
			return m
		}
	}
	e.t.Fatalf("no setting %s", key)
	return nil
}
