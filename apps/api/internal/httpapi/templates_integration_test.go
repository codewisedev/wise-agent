package httpapi_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"wise/apps/api/internal/tasks"
)

func TestTemplatesAndTaskWorkflow(t *testing.T) {
	e := setup(t)
	e.login()

	list := e.expect(e.do("GET", "/workflow-templates", nil), 200)["items"].([]any)
	if len(list) != 3 || list[0].(map[string]any)["isDefault"] != true || list[0].(map[string]any)["slug"] != "engineering" {
		t.Fatalf("seeded templates: %v", list)
	}

	// Invalid definitions are rejected with per-step messages; preview reports the same issues.
	bad := map[string]any{"steps": []any{
		map[string]any{"id": "plan", "agent": "planner-agent"},
		map[string]any{"id": "qa", "agent": "qa-agent"},
	}}
	res := e.expect(e.do("POST", "/workflow-templates", map[string]any{"name": "Bad", "definition": bad}), 400)
	if !strings.Contains(stringify(res), "steps.plan") || !strings.Contains(stringify(res), "not executable") {
		t.Fatalf("validation: %v", res)
	}
	prev := e.expect(e.do("POST", "/workflow-templates/preview", bad), 200)
	if len(prev["issues"].([]any)) < 2 {
		t.Fatalf("preview: %v", prev)
	}

	// A custom template: analysis then requirements reviewed, then planning.
	def := map[string]any{"steps": []any{
		map[string]any{"id": "analysis", "name": "Analysis", "agent": "product-agent"},
		map[string]any{"id": "req", "name": "Requirements", "agent": "requirements-agent", "review": true, "reviewIncludes": []string{"analysis"}},
		map[string]any{"id": "plan", "name": "Plan", "agent": "planner-agent"},
	}, "destination": map[string]any{"column": "review"}, "finalReport": false}
	tmpl := e.expect(e.do("POST", "/workflow-templates", map[string]any{"name": "Lean", "definition": def}), 201)
	tid := tmpl["id"].(string)

	pid := e.expect(e.do("POST", "/projects", map[string]string{"name": "Templates " + uuid.NewString()[:6]}), 201)["id"].(string)
	task := e.expect(e.do("POST", "/projects/"+pid+"/tasks", map[string]any{"title": "Lean task", "start": false, "templateId": tid}), 201)
	taskID := task["task"].(map[string]any)["id"].(string)
	plan := task["plan"].(map[string]any)
	if plan["templateName"] != "Lean" || len(plan["steps"].([]any)) != 3 || plan["destination"] != "review" {
		t.Fatalf("task plan preview: %v", plan)
	}

	// Disabling requirements breaks planning (it needs requirements): refused.
	e.expect(e.do("PUT", "/tasks/"+taskID+"/workflow", map[string]any{
		"templateId": tid, "steps": map[string]any{"req": map[string]any{"enabled": false}},
	}), 400)
	// Disabling planning and pinning an unknown account: refused; a built-in provider is fine.
	e.expect(e.do("PUT", "/tasks/"+taskID+"/workflow", map[string]any{
		"templateId": tid, "steps": map[string]any{"plan": map[string]any{"enabled": false}, "analysis": map[string]any{"account": "nope"}},
	}), 400)
	tw := e.expect(e.do("PUT", "/tasks/"+taskID+"/workflow", map[string]any{
		"templateId": tid, "steps": map[string]any{"plan": map[string]any{"enabled": false}, "analysis": map[string]any{"account": "scripted", "model": "m1"}},
	}), 200)
	steps := tw["plan"].(map[string]any)["steps"].([]any)
	if steps[2].(map[string]any)["enabled"] != false || steps[0].(map[string]any)["account"] != "scripted" {
		t.Fatalf("overrides: %v", steps)
	}

	// Running step by step freezes a plan where every enabled step is reviewed.
	run := e.expect(e.do("POST", "/tasks/"+taskID+"/run", map[string]any{"stepByStep": true}), 202)
	runID := uuid.MustParse(run["id"].(string))
	var cfg []byte
	_ = e.db.Pool.QueryRow(context.Background(), `SELECT config FROM workflow_runs WHERE id = $1`, runID).Scan(&cfg)
	p, ok := tasks.ParsePlan(cfg)
	if !ok || !p.StepByStep || !p.Steps[0].Review || p.Steps[0].Account != "scripted" || p.Steps[2].Enabled {
		t.Fatalf("frozen plan: %+v", p)
	}

	// Editing the template later does not touch the running plan.
	def["steps"] = []any{map[string]any{"id": "only", "agent": "product-agent"}}
	e.expect(e.do("PUT", "/workflow-templates/"+tid, map[string]any{"name": "Lean v2", "definition": def}), 200)
	_ = e.db.Pool.QueryRow(context.Background(), `SELECT config FROM workflow_runs WHERE id = $1`, runID).Scan(&cfg)
	if p2, _ := tasks.ParsePlan(cfg); len(p2.Steps) != 3 {
		t.Fatal("running workflows keep their frozen plan")
	}

	// A waiting step's account can be switched; unknown accounts and steps are refused.
	changed := e.expect(e.do("PATCH", "/workflows/"+runID.String()+"/steps/req", map[string]any{"account": "scripted"}), 200)
	if changed["steps"].([]any)[1].(map[string]any)["account"] != "scripted" {
		t.Fatalf("change step: %v", changed)
	}
	e.expect(e.do("PATCH", "/workflows/"+runID.String()+"/steps/req", map[string]any{"account": "nope"}), 400)
	e.expect(e.do("PATCH", "/workflows/"+runID.String()+"/steps/ghost", map[string]any{"account": "scripted"}), 404)
	if st, err := e.tasks.CurrentStep(context.Background(), runID, "req"); err != nil || st.Account != "scripted" {
		t.Fatalf("activities must see the change: %+v %v", st, err)
	}

	// The default template cannot be deleted; a custom one can.
	defID := list[0].(map[string]any)["id"].(string)
	e.expect(e.do("DELETE", "/workflow-templates/"+defID, nil), 409)
	e.expect(e.do("DELETE", "/workflow-templates/"+tid, nil), 204)
}

func stringify(m map[string]any) string {
	var b strings.Builder
	for k, v := range m {
		b.WriteString(k)
		b.WriteString("=")
		switch t := v.(type) {
		case []any:
			for _, x := range t {
				if mm, ok := x.(map[string]any); ok {
					b.WriteString(stringify(mm))
				}
			}
		default:
			b.WriteString(strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(toString(v), "\n", " "), "\t", " ")))
		}
		b.WriteString(";")
	}
	return b.String()
}

func toString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	default:
		return ""
	}
}
