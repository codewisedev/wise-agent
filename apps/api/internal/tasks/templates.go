package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// PipelineType is the Temporal workflow type of configurable workflows.
const PipelineType = "PipelineWorkflow"

// TemplateDef is a workflow template: ordered agent steps with optional review
// gates, where tasks come from and where they go when done.
type TemplateDef struct {
	Steps        []StepDef   `json:"steps"`
	Source       SourceDef   `json:"source"`
	Destination  Destination `json:"destination"`
	FinalReport  *bool       `json:"finalReport,omitempty"`  // default true
	MaxRevisions *int        `json:"maxRevisions,omitempty"` // default from engineering.yaml
}

// StepDef is one agent step.
type StepDef struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Agent string `json:"agent"`
	// Account and Model pin who runs the step; empty = the agent's default routing.
	Account string `json:"account,omitempty"`
	Model   string `json:"model,omitempty"`
	// Review pauses after the step: approve passes its output on, request
	// changes re-runs it (from ReviseFrom) with the comment as feedback.
	Review         bool     `json:"review,omitempty"`
	ReviewIncludes []string `json:"reviewIncludes,omitempty"` // earlier step ids pinned in the same approval
	ReviseFrom     string   `json:"reviseFrom,omitempty"`     // earliest step re-run on rejection (default: this step)
	Enabled        *bool    `json:"enabled,omitempty"`        // default true
}

// SourceDef says how tasks enter the workflow.
type SourceDef struct {
	// AutoStart runs the workflow as soon as a task is created or imported.
	AutoStart bool `json:"autoStart,omitempty"`
}

// Destination is where a finished task goes.
type Destination struct {
	Column string `json:"column,omitempty"` // board column; default "done"
}

// StepOverride is a task's change to one template step.
type StepOverride struct {
	Enabled *bool   `json:"enabled,omitempty"`
	Account *string `json:"account,omitempty"`
	Model   *string `json:"model,omitempty"`
	Review  *bool   `json:"review,omitempty"`
}

// Overrides are a task's per-step changes (tasks.workflow_overrides).
type Overrides struct {
	Steps map[string]StepOverride `json:"steps,omitempty"`
}

// PlanStep is a resolved step, frozen into the workflow run. Account and Model
// may still be changed while the run waits (they are read when the step runs).
type PlanStep struct {
	ID              string            `json:"id"`
	Name            string            `json:"name"`
	Agent           string            `json:"agent"`
	Kind            string            `json:"kind"`               // artifact kind the agent produces
	Consumes        map[string]string `json:"consumes,omitempty"` // input property → artifact kind
	AcceptsFeedback bool              `json:"acceptsFeedback"`
	Account         string            `json:"account,omitempty"`
	Model           string            `json:"model,omitempty"`
	Enabled         bool              `json:"enabled"`
	Review          bool              `json:"review"`
	ReviewKinds     []string          `json:"reviewKinds,omitempty"`
	ReviseFrom      string            `json:"reviseFrom,omitempty"`
}

// Plan is the frozen configuration of one workflow run (workflow_runs.config).
type Plan struct {
	Type            string     `json:"type"` // "pipeline"
	TemplateID      string     `json:"templateId,omitempty"`
	TemplateName    string     `json:"templateName,omitempty"`
	TemplateVersion int32      `json:"templateVersion,omitempty"`
	Steps           []PlanStep `json:"steps"`
	FinalReport     bool       `json:"finalReport"`
	MaxRevisions    int        `json:"maxRevisions"`
	Destination     string     `json:"destination"`
	StepByStep      bool       `json:"stepByStep"`
}

// Step finds a plan step by id.
func (p Plan) Step(id string) (PlanStep, int, bool) {
	for i, s := range p.Steps {
		if s.ID == id {
			return s, i, true
		}
	}
	return PlanStep{}, -1, false
}

// AgentInfo is what plan resolution needs to know about an agent.
type AgentInfo struct {
	Enabled         bool
	Produces        string
	Consumes        map[string]string
	RequiredInputs  []string
	AcceptsFeedback bool
}

// AgentLookup resolves agent names (implemented over the agent registry).
type AgentLookup func(name string) (AgentInfo, bool)

// AccountChecker validates a step's account choice.
type AccountChecker func(ctx context.Context, name string) error

// Issue is a validation problem, attached to a step when it concerns one.
type Issue struct {
	StepID  string `json:"stepId,omitempty"`
	Message string `json:"message"`
}

var stepIDRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,40}$`)

// Resolve applies overrides to a template and checks the result: known,
// enabled agents; unique step ids; every required agent input produced by an
// earlier enabled step; review references pointing backwards; valid accounts.
func Resolve(ctx context.Context, def TemplateDef, ov Overrides, stepByStep bool, agents AgentLookup, accounts AccountChecker, defaultMaxRevisions int) (Plan, []Issue) {
	plan := Plan{Type: "pipeline", FinalReport: def.FinalReport == nil || *def.FinalReport, MaxRevisions: defaultMaxRevisions,
		Destination: def.Destination.Column, StepByStep: stepByStep}
	if def.MaxRevisions != nil {
		plan.MaxRevisions = *def.MaxRevisions
	}
	if plan.Destination == "" {
		plan.Destination = "done"
	}
	var issues []Issue
	if !slices.Contains(Roles, plan.Destination) {
		issues = append(issues, Issue{Message: fmt.Sprintf("destination %q is not a column role (%s)", plan.Destination, strings.Join(Roles, ", "))})
	}
	if plan.MaxRevisions < 0 || plan.MaxRevisions > 50 {
		issues = append(issues, Issue{Message: "maxRevisions must be between 0 and 50"})
	}
	if len(def.Steps) == 0 {
		issues = append(issues, Issue{Message: "a workflow needs at least one step"})
	}
	if len(def.Steps) > 30 {
		issues = append(issues, Issue{Message: "a workflow can have at most 30 steps"})
	}
	for id := range ov.Steps {
		if !slices.ContainsFunc(def.Steps, func(s StepDef) bool { return s.ID == id }) {
			issues = append(issues, Issue{StepID: id, Message: "override for a step the template does not have"})
		}
	}
	seen := map[string]int{}
	produced := map[string]bool{} // kinds produced by earlier enabled steps
	for i, sd := range def.Steps {
		st := PlanStep{ID: sd.ID, Name: strings.TrimSpace(sd.Name), Agent: sd.Agent, Account: sd.Account, Model: sd.Model,
			Enabled: sd.Enabled == nil || *sd.Enabled, Review: sd.Review, ReviseFrom: sd.ReviseFrom}
		if o, ok := ov.Steps[sd.ID]; ok {
			if o.Enabled != nil {
				st.Enabled = *o.Enabled
			}
			if o.Account != nil {
				st.Account = *o.Account
			}
			if o.Model != nil {
				st.Model = *o.Model
			}
			if o.Review != nil {
				st.Review = *o.Review
			}
		}
		if stepByStep && st.Enabled {
			st.Review = true
		}
		if st.Name == "" {
			st.Name = sd.ID
		}
		bad := func(msg string, args ...any) {
			issues = append(issues, Issue{StepID: sd.ID, Message: fmt.Sprintf(msg, args...)})
		}
		if !stepIDRe.MatchString(sd.ID) {
			bad("step id must be lowercase letters, digits, - or _ (max 41)")
		} else if _, dup := seen[sd.ID]; dup {
			bad("duplicate step id")
		}
		seen[sd.ID] = i
		info, ok := agents(sd.Agent)
		switch {
		case !ok:
			bad("unknown agent %q", sd.Agent)
		case !info.Enabled:
			bad("agent %q is not executable yet", sd.Agent)
		default:
			st.Kind, st.Consumes, st.AcceptsFeedback = info.Produces, info.Consumes, info.AcceptsFeedback
			if st.Enabled {
				for _, in := range info.RequiredInputs {
					kind, mapped := info.Consumes[in]
					if !mapped {
						continue // filled from the task itself (e.g. request)
					}
					if !produced[kind] {
						bad("%s needs %s, which no earlier enabled step produces", sd.Agent, kind)
					}
				}
			}
		}
		if st.Review {
			st.ReviewKinds = []string{st.Kind}
			for _, inc := range sd.ReviewIncludes {
				j, ok := seen[inc]
				if !ok || inc == sd.ID {
					bad("reviewIncludes %q must name an earlier step", inc)
					continue
				}
				if k := plan.Steps[j].Kind; k != "" && plan.Steps[j].Enabled && !slices.Contains(st.ReviewKinds, k) {
					st.ReviewKinds = append(st.ReviewKinds, k)
				}
			}
		}
		if sd.ReviseFrom != "" {
			if _, ok := seen[sd.ReviseFrom]; !ok {
				bad("reviseFrom %q must name this or an earlier step", sd.ReviseFrom)
			}
		}
		if st.Model != "" && st.Account == "" {
			bad("a model can only be chosen together with an account")
		}
		if st.Account != "" && accounts != nil && st.Enabled {
			if err := accounts(ctx, st.Account); err != nil {
				bad("%s", strings.TrimPrefix(err.Error(), "Validation failed: "))
			}
		}
		if st.Enabled && st.Kind != "" {
			produced[st.Kind] = true
		}
		plan.Steps = append(plan.Steps, st)
	}
	if !slices.ContainsFunc(plan.Steps, func(s PlanStep) bool { return s.Enabled }) && len(def.Steps) > 0 {
		issues = append(issues, Issue{Message: "every step is disabled"})
	}
	return plan, issues
}

// ParseTemplate decodes a stored template definition.
func ParseTemplate(raw []byte) (TemplateDef, error) {
	var d TemplateDef
	if err := json.Unmarshal(raw, &d); err != nil {
		return d, fmt.Errorf("decode template: %w", err)
	}
	return d, nil
}

// ParseOverrides decodes tasks.workflow_overrides.
func ParseOverrides(raw []byte) Overrides {
	var o Overrides
	_ = json.Unmarshal(raw, &o)
	return o
}

// ParsePlan decodes workflow_runs.config for pipeline runs (ok=false for others).
func ParsePlan(raw []byte) (Plan, bool) {
	var p Plan
	if json.Unmarshal(raw, &p) != nil || p.Type != "pipeline" {
		return Plan{}, false
	}
	return p, true
}

func boolPtr(b bool) *bool { return &b }

// SeedTemplates are created on startup when missing (never overwritten).
func SeedTemplates() []struct {
	Slug, Name, Description string
	Default                 bool
	Def                     TemplateDef
} {
	analysis := StepDef{ID: "analysis", Name: "Product analysis", Agent: "product-agent"}
	requirements := StepDef{ID: "requirements", Name: "Requirements", Agent: "requirements-agent", Review: true, ReviewIncludes: []string{"analysis"}}
	planning := StepDef{ID: "planning", Name: "Planning", Agent: "planner-agent"}
	architecture := StepDef{ID: "architecture", Name: "Architecture review", Agent: "architect-agent", Review: true,
		ReviewIncludes: []string{"requirements", "planning"}, ReviseFrom: "planning"}
	eng := TemplateDef{Steps: []StepDef{analysis, requirements, planning, architecture}, Destination: Destination{Column: "done"}}
	all := TemplateDef{Steps: []StepDef{analysis, requirements, planning, architecture}, Destination: Destination{Column: "done"}}
	for i := range all.Steps {
		all.Steps[i].Review = true
	}
	quick := TemplateDef{Steps: []StepDef{analysis}, Destination: Destination{Column: "review"}, FinalReport: boolPtr(false)}
	return []struct {
		Slug, Name, Description string
		Default                 bool
		Def                     TemplateDef
	}{
		{"engineering", "Engineering", "Analysis → requirements (review) → planning → architecture (review) → report.", true, eng},
		{"step-by-step", "Step by step", "The engineering steps, pausing after every one so you pass each result on yourself.", false, all},
		{"quick-analysis", "Quick analysis", "Product analysis only; the card moves to Review.", false, quick},
	}
}

// LookupFrom adapts any agent info lookup with the same fields.
func LookupFrom[T any](fn func(string) (T, bool), conv func(T) AgentInfo) AgentLookup {
	return func(name string) (AgentInfo, bool) {
		v, ok := fn(name)
		if !ok {
			return AgentInfo{}, false
		}
		return conv(v), true
	}
}
