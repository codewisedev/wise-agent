package improvement

import (
	"encoding/json"
	"strings"
	"testing"

	"wise/apps/api/internal/tasks"
)

func TestPromptEdits(t *testing.T) {
	cur := "You plan work.\n\n## Output\nReturn JSON.\n"
	out, err := ApplyPromptEdits(cur, []PromptEdit{{Old: "You plan work.", New: "You plan work. Always list migrations."}, {New: "Mention rollback steps."}})
	if err != nil || !strings.Contains(out, "Always list migrations") || !strings.HasSuffix(out, "Mention rollback steps.\n") {
		t.Fatalf("%q %v", out, err)
	}
	for _, bad := range [][]PromptEdit{nil, {{Old: "missing", New: "x"}}, {{Old: "", New: "  "}}, {{Old: "Return JSON.", New: "Return JSON."}}} {
		if _, err := ApplyPromptEdits(cur, bad); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	if _, err := ApplyPromptEdits("a a", []PromptEdit{{Old: "a", New: "b"}}); err == nil {
		t.Error("ambiguous edit accepted")
	}
}

func TestPromptTargets(t *testing.T) {
	for _, a := range []string{Agent, Judge, "reviewer-agent", "security-agent"} {
		if CheckPromptTarget(a) == nil {
			t.Errorf("%s accepted", a)
		}
	}
	if CheckPromptTarget("planner-agent") != nil {
		t.Error("planner refused")
	}
}

func ops(s string) []PatchOp {
	var o []PatchOp
	if err := json.Unmarshal([]byte(s), &o); err != nil {
		panic(err)
	}
	return o
}

func TestRoutingAllowlist(t *testing.T) {
	ok := []struct{ setting, ops string }{
		{"models", `[{"op":"replace","path":"/aliases/reasoning/targets","value":[{"provider":"lmstudio","model":"qwen"}]}]`},
		{"models", `[{"op":"add","path":"/aliases/reasoning/targets/-","value":{"provider":"lmstudio"}}]`},
		{"models", `[{"op":"remove","path":"/aliases/reasoning/targets/1"}]`},
		{"model_policy", `[{"op":"replace","path":"/daily_budget_usd/planner-agent","value":4}]`},
	}
	for _, c := range ok {
		if err := CheckRoutingOps(c.setting, ops(c.ops)); err != nil {
			t.Errorf("%s %s: %v", c.setting, c.ops, err)
		}
	}
	bad := []struct{ setting, ops string }{
		{"model_policy", `[{"op":"add","path":"/cloud_allowed_agents/-","value":"x-agent"}]`},
		{"model_policy", `[{"op":"replace","path":"/cloud_allowed_agents","value":["*"]}]`},
		{"model_policy", `[{"op":"replace","path":"/max_retries","value":9}]`},
		{"model_policy", `[{"op":"replace","path":"/daily_budget_usd/planner-agent","value":-1}]`},
		{"models", `[{"op":"replace","path":"/providers/lmstudio/base_url","value":"http://evil"}]`},
		{"models", `[{"op":"add","path":"/aliases/reasoning/targets/-","value":{"provider":"lmstudio","api_key_env":"HOME"}}]`},
		{"models", `[{"op":"remove","path":"/aliases/reasoning/targets"}]`},
		{"models", `[{"op":"move","path":"/aliases/reasoning/targets/0"}]`},
		{"accounts", `[{"op":"replace","path":"/x","value":1}]`},
		{"execution", `[{"op":"replace","path":"/egress","value":[]}]`},
	}
	for _, c := range bad {
		if err := CheckRoutingOps(c.setting, ops(c.ops)); err == nil {
			t.Errorf("accepted %s %s", c.setting, c.ops)
		}
	}
}

func TestApplyPatch(t *testing.T) {
	doc := []byte(`{"aliases":{"reasoning":{"targets":[{"provider":"a"},{"provider":"b"}]}},"daily_budget_usd":{"default":5}}`)
	out, err := ApplyPatch(doc, ops(`[{"op":"remove","path":"/aliases/reasoning/targets/0"},{"op":"add","path":"/aliases/reasoning/targets/-","value":{"provider":"c"}},{"op":"add","path":"/daily_budget_usd/x-agent","value":2}]`))
	if err != nil || string(out) != `{"aliases":{"reasoning":{"targets":[{"provider":"b"},{"provider":"c"}]}},"daily_budget_usd":{"default":5,"x-agent":2}}` {
		t.Fatalf("%s %v", out, err)
	}
	if _, err := ApplyPatch(doc, ops(`[{"op":"replace","path":"/aliases/nope/targets","value":[]}]`)); err == nil {
		t.Error("missing path accepted")
	}
}

func bp(b bool) *bool { return &b }
func ip(i int) *int   { return &i }

func engineering() tasks.TemplateDef {
	return tasks.TemplateDef{MaxRevisions: ip(3), Steps: []tasks.StepDef{
		{ID: "planning", Agent: "planner-agent"},
		{ID: "architecture", Agent: "architect-agent", Review: true, ReviewIncludes: []string{"planning"}, ReviseFrom: "planning"},
		{ID: "workspace", Type: tasks.StepWorkspace},
		{ID: "implement", Agent: "backend-agent", Runtime: "claude-code"},
		{ID: "review", Agent: "reviewer-agent"},
		{ID: "gate", Type: tasks.StepGate, FixFrom: "implement"},
		{ID: "deliver", Type: tasks.StepDeliver},
	}}
}

func TestTemplateRules(t *testing.T) {
	cur := engineering()
	good := engineering()
	good.Steps[0].Agent = "architect-agent"
	good.Steps[0].Account = "lmstudio"
	good.Steps = append(good.Steps, tasks.StepDef{ID: "docs", Agent: "documentation-agent"})
	good.MaxRevisions = ip(4)
	if err := CheckTemplate(cur, good); err != nil {
		t.Fatalf("allowed change refused: %v", err)
	}
	bad := map[string]func(d *tasks.TemplateDef){
		"remove review":      func(d *tasks.TemplateDef) { d.Steps = append(d.Steps[:1], d.Steps[2:]...) },
		"unreview":           func(d *tasks.TemplateDef) { d.Steps[1].Review = false },
		"narrow includes":    func(d *tasks.TemplateDef) { d.Steps[1].ReviewIncludes = nil },
		"revise from":        func(d *tasks.TemplateDef) { d.Steps[1].ReviseFrom = "" },
		"disable workspace":  func(d *tasks.TemplateDef) { d.Steps[2].Enabled = bp(false) },
		"runtime change":     func(d *tasks.TemplateDef) { d.Steps[3].Runtime = "codex" },
		"runtime removed":    func(d *tasks.TemplateDef) { d.Steps[3].Runtime = "" },
		"remove gate agent":  func(d *tasks.TemplateDef) { d.Steps = append(d.Steps[:4], d.Steps[5:]...) },
		"replace gate agent": func(d *tasks.TemplateDef) { d.Steps[4].Agent = "qa-agent" },
		"gate fixFrom":       func(d *tasks.TemplateDef) { d.Steps[5].FixFrom = "planning" },
		"gate type":          func(d *tasks.TemplateDef) { d.Steps[5].Type = tasks.StepAgent },
		"remove deliver":     func(d *tasks.TemplateDef) { d.Steps = d.Steps[:6] },
		"lower revisions":    func(d *tasks.TemplateDef) { d.MaxRevisions = ip(1) },
		"drop revisions":     func(d *tasks.TemplateDef) { d.MaxRevisions = nil },
		"new step runtime": func(d *tasks.TemplateDef) {
			d.Steps = append(d.Steps, tasks.StepDef{ID: "x", Agent: "frontend-agent", Runtime: "codex"})
		},
		"autostart":   func(d *tasks.TemplateDef) { d.Source.AutoStart = true },
		"destination": func(d *tasks.TemplateDef) { d.Destination.Column = "review" },
		"add release": func(d *tasks.TemplateDef) {
			d.Steps = append(d.Steps, tasks.StepDef{ID: "release", Type: tasks.StepRelease})
		},
		"add deliver": func(d *tasks.TemplateDef) {
			d.Steps = append(d.Steps, tasks.StepDef{ID: "deliver2", Type: tasks.StepDeliver})
		},
	}
	for name, mutate := range bad {
		d := engineering()
		mutate(&d)
		if err := CheckTemplate(cur, d); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	unset := engineering()
	unset.MaxRevisions = nil
	withLimit := engineering()
	withLimit.MaxRevisions = ip(1)
	if CheckTemplate(unset, withLimit) == nil {
		t.Error("setting a limit where the default applied accepted")
	}
}

func TestMemoryOps(t *testing.T) {
	if err := CheckMemoryOps([]MemoryOp{{Op: "create", Title: "t", Body: "b"}, {Op: "archive", IDs: []string{"x"}}, {Op: "merge", IDs: []string{"a", "b"}, Title: "t", Body: "b"}}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]MemoryOp{nil, {{Op: "delete", IDs: []string{"x"}}}, {{Op: "create"}}, {{Op: "merge", IDs: []string{"a"}, Title: "t", Body: "b"}}, {{Op: "archive"}}} {
		if CheckMemoryOps(bad) == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}

func TestHardeningFromReview(t *testing.T) {
	// Environment references never enter a stored setting.
	for _, o := range []string{
		`[{"op":"add","path":"/aliases/local/targets/-","value":{"provider":"${WISE_SECRETS_KEY}"}}]`,
		`[{"op":"replace","path":"/aliases/local/targets","value":[{"provider":"scripted","model":"$HOME"}]}]`,
		`[{"op":"add","path":"/aliases/local/targets/-","value":{"provider":"lmstudio","model":"\u0024{WISE_SECRETS_KEY}"}}]`,
	} {
		if CheckRoutingOps("models", ops(o)) == nil {
			t.Errorf("accepted %s", o)
		}
	}
	if CheckRoutingOps("model_policy", ops(`[{"op":"remove","path":"/daily_budget_usd/default"}]`)) == nil {
		t.Error("removing the default budget accepted")
	}
	if CheckRoutingOps("model_policy", ops(`[{"op":"replace","path":"/daily_budget_usd/default","value":3}]`)) != nil {
		t.Error("changing the default budget refused")
	}

	cur := engineering()
	bad := map[string]func(d *tasks.TemplateDef){
		"remove runtime step":  func(d *tasks.TemplateDef) { d.Steps = append(d.Steps[:3], d.Steps[4:]...) },
		"re-pin gate reviewer": func(d *tasks.TemplateDef) { d.Steps[4].Account = "lmstudio" },
		"gate model":           func(d *tasks.TemplateDef) { d.Steps[4].Model = "tiny" },
		"gate diagnosis":       func(d *tasks.TemplateDef) { d.Steps[5].DiagnoseWith = "qa-agent" },
	}
	for name, mutate := range bad {
		d := engineering()
		mutate(&d)
		if err := CheckTemplate(cur, d); err == nil {
			t.Errorf("%s accepted", name)
		}
	}

	// Resolved plans: the gate's effective fix step and review scope must not move.
	before := tasks.Plan{Steps: []tasks.PlanStep{
		{ID: "analysis", Enabled: true}, {ID: "requirements", Enabled: true, Review: true, ReviewKinds: []string{"product_analysis", "requirements"}},
		{ID: "implement", Enabled: true}, {ID: "gate", Type: tasks.StepGate, Enabled: true, FixFrom: "implement"},
	}}
	moved := tasks.Plan{Steps: []tasks.PlanStep{
		{ID: "analysis", Enabled: true}, {ID: "requirements", Enabled: true, Review: true, ReviewKinds: []string{"product_analysis", "requirements"}},
		{ID: "implement", Enabled: true}, {ID: "impl2", Enabled: true}, {ID: "gate", Type: tasks.StepGate, Enabled: true, FixFrom: "impl2"},
	}}
	narrowed := tasks.Plan{Steps: []tasks.PlanStep{
		{ID: "analysis"}, {ID: "requirements", Enabled: true, Review: true, ReviewKinds: []string{"requirements"}},
		{ID: "implement", Enabled: true}, {ID: "gate", Type: tasks.StepGate, Enabled: true, FixFrom: "implement"},
	}}
	if CheckResolved(before, moved) == nil || CheckResolved(before, narrowed) == nil {
		t.Error("resolved-plan changes accepted")
	}
	if CheckResolved(before, before) != nil {
		t.Error("identical plan refused")
	}
}
