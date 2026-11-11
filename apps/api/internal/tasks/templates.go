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

// Step types. Agent steps run an agent; the others are engineering steps run
// by Wise itself in the task's workspace.
const (
	StepAgent     = "agent"
	StepWorkspace = "workspace" // prepare the task workspace (clone, branch, sandbox, setup)
	StepQuality   = "quality"   // run the repository's checks
	StepGate      = "gate"      // pass/fail on checks and reviews; fix rounds; verified changeset
	StepDeliver   = "deliver"   // write the approved commit as the task branch
	StepRelease   = "release"   // push approval, push, pull request (per project policy)
)

// ExternalRuntimes are the coding CLIs an implementing step may run on.
var ExternalRuntimes = []string{"claude-code", "codex"}

// StepTypes lists the step types.
var StepTypes = []string{StepAgent, StepWorkspace, StepQuality, StepGate, StepDeliver, StepRelease}

// Artifact kinds produced by engineering steps.
const (
	KindQualityReport  = "quality_report"
	KindChangeset      = "changeset"
	KindDelivery       = "delivery"
	KindImplementation = "implementation"
	KindQA             = "qa"
	KindGate           = "quality_gate"
	KindPush           = "push"
)

// StepDef is one workflow step.
type StepDef struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Type  string `json:"type,omitempty"` // default "agent"
	Agent string `json:"agent,omitempty"`
	// Gate steps: FixFrom is the implementing step re-run with the failures
	// (default: the nearest earlier step producing an implementation);
	// DiagnoseWith optionally runs an agent (e.g. qa-agent) to explain them.
	FixFrom      string `json:"fixFrom,omitempty"`
	DiagnoseWith string `json:"diagnoseWith,omitempty"`
	// Runtime runs an implementing agent on an external coding CLI
	// (claude-code, codex); empty or "native" = Wise's own loop.
	Runtime string `json:"runtime,omitempty"`
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
	Runtime *string `json:"runtime,omitempty"`
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
	Type            string            `json:"type,omitempty"` // empty = agent (plans frozen before Phase 5)
	Agent           string            `json:"agent"`
	Inputs          []string          `json:"inputs,omitempty"`
	FixFrom         string            `json:"fixFrom,omitempty"`
	DiagnoseWith    string            `json:"diagnoseWith,omitempty"`
	Runtime         string            `json:"runtime,omitempty"`  // empty = native
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

// IsAgent reports whether the step runs an agent.
func (s PlanStep) IsAgent() bool { return s.Type == "" || s.Type == StepAgent }

// Accepts reports whether the step's agent takes an input property.
func (s PlanStep) Accepts(prop string) bool { return slices.Contains(s.Inputs, prop) }

// HasType reports whether an enabled step of type t exists.
func (p Plan) HasType(t string) bool {
	return slices.ContainsFunc(p.Steps, func(s PlanStep) bool { return s.Enabled && s.Type == t })
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
	Inputs          []string
	NeedsWorkspace  bool
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
	reviewed := map[string]bool{} // kinds an earlier enabled step's review approves
	reviewedAny, workspace := false, false
	for i, sd := range def.Steps {
		st := PlanStep{ID: sd.ID, Name: strings.TrimSpace(sd.Name), Agent: sd.Agent, Account: sd.Account, Model: sd.Model,
			Enabled: sd.Enabled == nil || *sd.Enabled, Review: sd.Review, ReviseFrom: sd.ReviseFrom, Runtime: sd.Runtime}
		if o, ok := ov.Steps[sd.ID]; ok {
			if o.Runtime != nil {
				st.Runtime = *o.Runtime
			}
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
		typ := sd.Type
		if typ == "" {
			typ = StepAgent
		}
		if typ != StepAgent {
			st.Type = typ // agent steps keep an empty type, as plans always had
		}
		if stepByStep && st.Enabled && (typ == StepAgent || typ == StepGate) {
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
		switch typ {
		case StepAgent:
			info, ok := agents(sd.Agent)
			switch {
			case sd.Agent == "":
				bad("an agent step needs an agent")
			case !ok:
				bad("unknown agent %q", sd.Agent)
			case !info.Enabled:
				bad("agent %q is not executable yet", sd.Agent)
			default:
				st.Kind, st.Consumes, st.AcceptsFeedback, st.Inputs = info.Produces, info.Consumes, info.AcceptsFeedback, info.Inputs
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
					if info.NeedsWorkspace && !workspace {
						bad("%s works in the task workspace: add a workspace step before it", sd.Agent)
					}
					if st.Runtime == "native" {
						st.Runtime = ""
					}
					if st.Runtime != "" {
						if !slices.Contains(ExternalRuntimes, st.Runtime) {
							bad("unknown runtime %q (native, %s)", st.Runtime, strings.Join(ExternalRuntimes, ", "))
						} else if info.Produces != KindImplementation {
							bad("only agents that implement the change (like backend-agent) can run on %s", st.Runtime)
						}
					}
				}
			}
		case StepWorkspace, StepQuality, StepGate, StepDeliver, StepRelease:
			if st.Runtime != "" && st.Runtime != "native" {
				bad("only implementing agent steps run on an external coding runtime")
			}
			if sd.Agent != "" && typ != StepGate {
				bad("a %s step runs no agent", typ)
			}
			st.Agent = ""
			if !st.Enabled {
				break
			}
			switch typ {
			case StepWorkspace:
				if !reviewedAny {
					bad("the workspace step needs an earlier reviewed step (approve the plan before code is written)")
				}
				if st.Review {
					bad("a workspace step has nothing to review")
				}
			case StepQuality:
				st.Kind = KindQualityReport
				if !workspace {
					bad("the quality step needs an earlier workspace step")
				}
			case StepGate:
				st.Kind = KindChangeset
				if !workspace || !produced[KindImplementation] {
					bad("the gate needs earlier workspace and implementation steps")
				}
				fix := sd.FixFrom
				if fix == "" {
					for j := len(plan.Steps) - 1; j >= 0; j-- {
						if plan.Steps[j].Enabled && plan.Steps[j].Kind == KindImplementation {
							fix = plan.Steps[j].ID
							break
						}
					}
				}
				if j, ok := seen[fix]; !ok || fix == sd.ID || plan.Steps[j].Kind != KindImplementation || !plan.Steps[j].Enabled {
					bad("fixFrom must name an earlier enabled step that implements the change")
				}
				st.FixFrom = fix
				if sd.DiagnoseWith != "" {
					if info, ok := agents(sd.DiagnoseWith); !ok || !info.Enabled {
						bad("diagnoseWith: agent %q is not executable", sd.DiagnoseWith)
					}
					st.DiagnoseWith = sd.DiagnoseWith
				}
			case StepDeliver:
				st.Kind = KindDelivery
				if !reviewed[KindChangeset] {
					bad("the deliver step needs an earlier gate with a review (your final approval of the code)")
				}
				if st.Review {
					bad("a deliver step has nothing to review")
				}
			case StepRelease:
				// Its approvals (push, and pull request when the project asks) are built in.
				st.Kind = KindPush
				if !produced[KindDelivery] {
					bad("the release step needs an earlier deliver step")
				}
				if st.Review {
					bad("the release step asks for your push approval itself; it has no other review")
				}
				if len(sd.ID) > 37 {
					bad("the release step id must be at most 37 characters")
				}
			}
		default:
			bad("unknown step type %q (%s)", sd.Type, strings.Join(StepTypes, ", "))
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
		if st.Enabled && st.Review {
			reviewedAny = true
			for _, k := range st.ReviewKinds {
				reviewed[k] = true
			}
		}
		if st.Enabled && typ == StepWorkspace {
			workspace = true
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
	code := TemplateDef{Steps: []StepDef{analysis, requirements, planning, architecture,
		{ID: "workspace", Name: "Prepare workspace", Type: StepWorkspace},
		{ID: "implement", Name: "Implement", Agent: "backend-agent"},
		{ID: "quality", Name: "Quality checks", Type: StepQuality},
		{ID: "code-review", Name: "Code review", Agent: "reviewer-agent"},
		{ID: "security", Name: "Security review", Agent: "security-agent"},
		{ID: "gate", Name: "Quality gate", Type: StepGate, FixFrom: "implement", DiagnoseWith: "qa-agent", Review: true,
			ReviewIncludes: []string{"implement", "quality", "code-review", "security"}, ReviseFrom: "implement"},
		{ID: "deliver", Name: "Deliver branch", Type: StepDeliver},
		{ID: "release", Name: "Push and pull request", Type: StepRelease},
	}, Destination: Destination{Column: "done"}}
	return []struct {
		Slug, Name, Description string
		Default                 bool
		Def                     TemplateDef
	}{
		{"engineering", "Engineering", "Analysis → requirements (review) → planning → architecture (review) → report.", true, eng},
		{"step-by-step", "Step by step", "The engineering steps, pausing after every one so you pass each result on yourself.", false, all},
		{"quick-analysis", "Quick analysis", "Product analysis only; the card moves to Review.", false, quick},
		{"engineering-code", "Engineering → code", "The engineering steps, then Wise writes the code in an isolated workspace: implement → checks → code and security review → quality gate with fix rounds → your final approval of the diff → a wise/… branch in your repository → your push approval → push and (per project) a pull request.", false, code},
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
