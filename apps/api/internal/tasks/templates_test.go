package tasks

import (
	"context"
	"errors"
	"strings"
	"testing"
)

var testAgents = map[string]AgentInfo{
	"product-agent":      {Enabled: true, Produces: "product_analysis", RequiredInputs: []string{"request"}, AcceptsFeedback: true},
	"requirements-agent": {Enabled: true, Produces: "requirements", Consumes: map[string]string{"product_analysis": "product_analysis"}, RequiredInputs: []string{"request"}, AcceptsFeedback: true},
	"planner-agent":      {Enabled: true, Produces: "plan", Consumes: map[string]string{"requirements": "requirements"}, RequiredInputs: []string{"request", "requirements"}},
	"qa-agent":           {Enabled: false, Produces: "qa"},
}

// phase5Agents mirror the shipped coding and review agents.
var phase5Agents = map[string]AgentInfo{
	"backend-agent": {Enabled: true, Produces: KindImplementation, Consumes: map[string]string{"plan": "plan"}, RequiredInputs: []string{"request", "plan"},
		AcceptsFeedback: true, NeedsWorkspace: true, Inputs: []string{"fix_feedback", "quality_commands"}},
	"reviewer-agent": {Enabled: true, Produces: "code_review", NeedsWorkspace: true},
	"security-agent": {Enabled: true, Produces: "security_review", NeedsWorkspace: true},
	"qa-agent":       {Enabled: true, Produces: KindQA, NeedsWorkspace: true},
}

func lookup(name string) (AgentInfo, bool) { a, ok := testAgents[name]; return a, ok }

func accounts(_ context.Context, name string) error {
	if name == "claude-work" || name == "lmstudio" {
		return nil
	}
	return errors.New("no account named " + name)
}

func messages(is []Issue) string {
	var b strings.Builder
	for _, i := range is {
		b.WriteString(i.StepID + ": " + i.Message + "\n")
	}
	return b.String()
}

func TestResolveSeededTemplatesAreValid(t *testing.T) {
	agents := func(name string) (AgentInfo, bool) {
		if name == "architect-agent" {
			return AgentInfo{Enabled: true, Produces: "architecture_review", Consumes: map[string]string{"requirements": "requirements", "plan": "plan"}, RequiredInputs: []string{"requirements", "plan"}}, true
		}
		if a, ok := phase5Agents[name]; ok {
			return a, true
		}
		return lookup(name)
	}
	for _, seed := range SeedTemplates() {
		if _, issues := Resolve(context.Background(), seed.Def, Overrides{}, false, agents, accounts, 5); len(issues) > 0 {
			t.Errorf("%s: %s", seed.Slug, messages(issues))
		}
	}
}

func TestResolveAppliesOverridesAndStepByStep(t *testing.T) {
	def := TemplateDef{Steps: []StepDef{
		{ID: "analysis", Agent: "product-agent"},
		{ID: "requirements", Agent: "requirements-agent", Review: true, ReviewIncludes: []string{"analysis"}},
	}}
	f := false
	acct := "claude-work"
	plan, issues := Resolve(context.Background(), def, Overrides{Steps: map[string]StepOverride{
		"requirements": {Account: &acct},
	}}, true, lookup, accounts, 5)
	if len(issues) > 0 {
		t.Fatal(messages(issues))
	}
	if !plan.Steps[0].Review || !plan.Steps[1].Review {
		t.Fatal("step by step reviews every step")
	}
	if plan.Steps[1].Account != "claude-work" || plan.Steps[1].Kind != "requirements" {
		t.Fatalf("override not applied: %+v", plan.Steps[1])
	}
	if got := strings.Join(plan.Steps[1].ReviewKinds, ","); got != "requirements,product_analysis" {
		t.Fatalf("review kinds: %s", got)
	}
	plan, _ = Resolve(context.Background(), def, Overrides{Steps: map[string]StepOverride{"analysis": {Enabled: &f}}}, false, lookup, accounts, 5)
	if plan.Steps[0].Enabled || plan.Destination != "done" || !plan.FinalReport {
		t.Fatalf("disable override / defaults: %+v", plan)
	}
}

func TestResolveReportsProblems(t *testing.T) {
	def := TemplateDef{Steps: []StepDef{
		{ID: "Bad ID", Agent: "product-agent"},
		{ID: "plan", Agent: "planner-agent"},                  // needs requirements, nobody produces it
		{ID: "qa", Agent: "qa-agent"},                         // not executable
		{ID: "x", Agent: "nope"},                              // unknown
		{ID: "plan", Agent: "product-agent"},                  // duplicate
		{ID: "r", Agent: "product-agent", Account: "missing"}, // bad account
		{ID: "m", Agent: "product-agent", Model: "gpt"},       // model without account
		{ID: "v", Agent: "product-agent", Review: true, ReviewIncludes: []string{"later"}, ReviseFrom: "zzz"},
	}, Destination: Destination{Column: "nowhere"}}
	_, issues := Resolve(context.Background(), def, Overrides{Steps: map[string]StepOverride{"ghost": {}}}, false, lookup, accounts, 5)
	got := messages(issues)
	for _, want := range []string{
		"Bad ID: step id must be", "plan: planner-agent needs requirements", "qa: agent \"qa-agent\" is not executable",
		"x: unknown agent", "plan: duplicate step id", "r: no account named missing", "m: a model can only be chosen",
		"v: reviewIncludes \"later\"", "v: reviseFrom \"zzz\"", ": destination \"nowhere\"", "ghost: override for a step",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing issue %q in:\n%s", want, got)
		}
	}
}

func TestDisablingAProducerBreaksItsConsumer(t *testing.T) {
	def := TemplateDef{Steps: []StepDef{
		{ID: "requirements", Agent: "requirements-agent"},
		{ID: "plan", Agent: "planner-agent"},
	}}
	f := false
	_, issues := Resolve(context.Background(), def, Overrides{Steps: map[string]StepOverride{"requirements": {Enabled: &f}}}, false, lookup, accounts, 5)
	if !strings.Contains(messages(issues), "plan: planner-agent needs requirements") {
		t.Fatalf("got %s", messages(issues))
	}
}

func TestEngineeringStepRules(t *testing.T) {
	agents := func(name string) (AgentInfo, bool) {
		if a, ok := phase5Agents[name]; ok {
			return a, true
		}
		return lookup(name)
	}
	resolve := func(steps ...StepDef) string {
		_, is := Resolve(context.Background(), TemplateDef{Steps: steps}, Overrides{}, false, agents, accounts, 3)
		return messages(is)
	}
	planning := []StepDef{{ID: "analysis", Agent: "product-agent"}, {ID: "req", Agent: "requirements-agent", Review: true}, {ID: "plan", Agent: "planner-agent"}}
	with := func(more ...StepDef) []StepDef { return append(append([]StepDef{}, planning...), more...) }

	// Code is written only after an approval, in a workspace.
	msg := resolve(StepDef{ID: "ws", Type: StepWorkspace}, StepDef{ID: "x", Agent: "product-agent"})
	if !strings.Contains(msg, "ws: the workspace step needs an earlier reviewed step") {
		t.Errorf("workspace before approval: %s", msg)
	}
	if msg := resolve(with(StepDef{ID: "impl", Agent: "backend-agent"})...); !strings.Contains(msg, "add a workspace step before it") {
		t.Errorf("coding agent without workspace: %s", msg)
	}
	if msg := resolve(with(StepDef{ID: "q", Type: StepQuality})...); !strings.Contains(msg, "needs an earlier workspace step") {
		t.Errorf("quality without workspace: %s", msg)
	}
	if msg := resolve(with(StepDef{ID: "ws", Type: StepWorkspace}, StepDef{ID: "impl", Agent: "backend-agent"}, StepDef{ID: "d", Type: StepDeliver})...); !strings.Contains(msg, "needs an earlier gate with a review") {
		t.Errorf("deliver without approved gate: %s", msg)
	}
	if msg := resolve(with(StepDef{ID: "ws", Type: StepWorkspace}, StepDef{ID: "g", Type: StepGate})...); !strings.Contains(msg, "the gate needs earlier workspace and implementation steps") {
		t.Errorf("gate without implementation: %s", msg)
	}
	if msg := resolve(with(StepDef{ID: "w", Type: "deploy"})...); !strings.Contains(msg, "unknown step type") {
		t.Errorf("bad type: %s", msg)
	}

	disabledFix := with(StepDef{ID: "ws", Type: StepWorkspace}, StepDef{ID: "impl", Agent: "backend-agent"},
		StepDef{ID: "impl2", Agent: "backend-agent", Enabled: boolPtr(false)}, StepDef{ID: "g", Type: StepGate, FixFrom: "impl2"})
	if msg := resolve(disabledFix...); !strings.Contains(msg, "fixFrom must name an earlier enabled step") {
		t.Errorf("disabled fixFrom: %s", msg)
	}

	// A valid one: the gate defaults fixFrom to the implementing step, the
	// deliver step needs its review.
	plan, is := Resolve(context.Background(), TemplateDef{Steps: with(
		StepDef{ID: "ws", Type: StepWorkspace}, StepDef{ID: "impl", Agent: "backend-agent"}, StepDef{ID: "q", Type: StepQuality},
		StepDef{ID: "rev", Agent: "reviewer-agent"}, StepDef{ID: "g", Type: StepGate, Review: true, ReviewIncludes: []string{"q"}},
		StepDef{ID: "d", Type: StepDeliver})}, Overrides{}, false, agents, accounts, 3)
	if len(is) != 0 {
		t.Fatalf("issues: %s", messages(is))
	}
	g, _, _ := plan.Step("g")
	if g.FixFrom != "impl" || g.Kind != KindChangeset || strings.Join(g.ReviewKinds, ",") != "changeset,quality_report" {
		t.Fatalf("gate: %+v", g)
	}
	impl, _, _ := plan.Step("impl")
	if impl.Type != "" || !impl.Accepts("fix_feedback") || !plan.HasType(StepWorkspace) {
		t.Fatalf("impl: %+v", impl)
	}
}
