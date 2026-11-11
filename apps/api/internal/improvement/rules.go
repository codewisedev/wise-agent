// Package improvement is Wise's self-improvement loop (implementation-plan
// §13): an analysis of its own performance proposes changes to agent prompts,
// memory, model routing and workflow templates, which you evaluate, apply and
// roll back. Proposals never change security policies, agent tools or
// permissions, approval requirements, execution boundaries, secrets, git
// authorization or coding-runtime policies: the checks in this file enforce
// that when a proposal is created and again when it is applied.
package improvement

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"wise/apps/api/internal/memory"
	"wise/apps/api/internal/tasks"
)

// Kinds of proposals.
const (
	KindPrompt   = "prompt"
	KindMemory   = "memory"
	KindRouting  = "routing"
	KindTemplate = "template"
)

// Agent is the analysis agent; Judge scores evaluations.
const (
	Agent = "improvement-agent"
	Judge = "eval-judge-agent"
)

// NoPromptAgents can never be the target of a prompt proposal.
var NoPromptAgents = map[string]string{
	Agent:            "the analysis agent cannot rewrite its own instructions",
	Judge:            "the evaluation judge cannot be changed by the proposals it judges",
	"reviewer-agent": "the code reviewer is a quality gate: weakening it changes an approval requirement",
	"security-agent": "the security reviewer is a quality gate: weakening it changes an approval requirement",
}

// GateAgents back quality gates: template proposals cannot remove, disable,
// replace or re-pin their steps, and memories written by proposals never reach them.
var GateAgents = memory.GateAgents

// RuleError is a refusal under the hard rule (or a malformed change).
type RuleError struct{ Reason string }

func (e *RuleError) Error() string { return e.Reason }

func refuse(format string, a ...any) error { return &RuleError{Reason: fmt.Sprintf(format, a...)} }

// PromptEdit replaces Old (which must occur exactly once) with New; an empty Old appends New.
type PromptEdit struct {
	Old string `json:"old"`
	New string `json:"new"`
}

// ApplyPromptEdits returns the prompt with the edits applied.
func ApplyPromptEdits(prompt string, edits []PromptEdit) (string, error) {
	if len(edits) == 0 {
		return "", refuse("a prompt proposal needs at least one edit")
	}
	out := prompt
	for i, e := range edits {
		switch {
		case e.Old == "" && strings.TrimSpace(e.New) == "":
			return "", refuse("edit %d is empty", i+1)
		case e.Old == "":
			out = strings.TrimRight(out, "\n") + "\n\n" + strings.TrimSpace(e.New) + "\n"
		default:
			if n := strings.Count(out, e.Old); n != 1 {
				return "", refuse("edit %d: the text to replace occurs %d times in the current prompt (must be exactly once)", i+1, n)
			}
			out = strings.Replace(out, e.Old, e.New, 1)
		}
	}
	if out == prompt {
		return "", refuse("the edits do not change the prompt")
	}
	if len(out) > 100_000 {
		return "", refuse("the new prompt is too long")
	}
	return out, nil
}

// CheckPromptTarget refuses agents whose prompts proposals may not change.
func CheckPromptTarget(agent string) error {
	if why, ok := NoPromptAgents[agent]; ok {
		return refuse("%s: %s", agent, why)
	}
	return nil
}

// PatchOp is one JSON patch operation (RFC 6902 subset).
type PatchOp struct {
	Op    string          `json:"op"`
	Path  string          `json:"path"`
	Value json.RawMessage `json:"value,omitempty"`
}

var (
	aliasTargetsPath = regexp.MustCompile(`^/aliases/[A-Za-z0-9_.-]+/targets(/(\d+|-))?$`)
	budgetPath       = regexp.MustCompile(`^/daily_budget_usd/[A-Za-z0-9_.*-]+$`)
)

// RoutingSettings are the settings routing proposals may patch.
var RoutingSettings = []string{"models", "model_policy"}

type target struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

func strictTarget(raw json.RawMessage) error {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return refuse("a target must be an object {provider, model}")
	}
	for k := range m {
		if k != "provider" && k != "model" {
			return refuse("a target may only set provider and model (not %q)", k)
		}
	}
	var t target
	if json.Unmarshal(raw, &t) != nil || t.Provider == "" {
		return refuse("a target needs a provider")
	}
	return nil
}

// CheckRoutingOps allows only alias targets (models) and daily budgets
// (model_policy). Providers, credentials, cloud_allowed_agents, retries and the
// cache are never touched.
func CheckRoutingOps(setting string, ops []PatchOp) error {
	if !slices.Contains(RoutingSettings, setting) {
		return refuse("routing proposals may only change the models or model_policy setting, not %q", setting)
	}
	if len(ops) == 0 {
		return refuse("a routing proposal needs at least one operation")
	}
	for i, op := range ops {
		// Stored settings are expanded like the shipped files (${VAR}): a value
		// from a proposal could otherwise pull a secret into a model name.
		if strings.Contains(op.Path, "$") || containsDollar(op.Value) {
			return refuse("operation %d: values may not contain $ (environment references)", i+1)
		}
		if op.Op != "add" && op.Op != "replace" && op.Op != "remove" {
			return refuse("operation %d: %q is not allowed (add, replace or remove)", i+1, op.Op)
		}
		switch setting {
		case "models":
			m := aliasTargetsPath.FindStringSubmatch(op.Path)
			if m == nil {
				return refuse("operation %d: %s is outside the allowed paths (/aliases/<alias>/targets…)", i+1, op.Path)
			}
			whole := m[1] == ""
			switch {
			case op.Op == "remove" && (whole || m[2] == "-"):
				return refuse("operation %d: remove one target by index, not the whole list", i+1)
			case op.Op == "remove":
			case whole:
				var list []json.RawMessage
				if json.Unmarshal(op.Value, &list) != nil || len(list) == 0 {
					return refuse("operation %d: targets must be a non-empty list", i+1)
				}
				for _, t := range list {
					if err := strictTarget(t); err != nil {
						return err
					}
				}
			default:
				if err := strictTarget(op.Value); err != nil {
					return err
				}
			}
		case "model_policy":
			if !budgetPath.MatchString(op.Path) {
				return refuse("operation %d: %s is outside the allowed paths (/daily_budget_usd/<agent>); cloud permissions and retries are never changed", i+1, op.Path)
			}
			if op.Path == "/daily_budget_usd/default" && op.Op == "remove" {
				return refuse("operation %d: the default budget may be changed but not removed (agents without a budget would be uncapped)", i+1)
			}
			if op.Op != "remove" {
				var v float64
				if json.Unmarshal(op.Value, &v) != nil || v < 0 || v > 1000 {
					return refuse("operation %d: a daily budget must be a number between 0 and 1000", i+1)
				}
			}
		}
	}
	return nil
}

// containsDollar reports whether any decoded string (key or value) contains
// '$' — checked after decoding, so "\u0024" escapes cannot hide it.
func containsDollar(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return true // fail closed
	}
	var walk func(any) bool
	walk = func(x any) bool {
		switch t := x.(type) {
		case string:
			return strings.Contains(t, "$")
		case []any:
			for _, e := range t {
				if walk(e) {
					return true
				}
			}
		case map[string]any:
			for k, e := range t {
				if strings.Contains(k, "$") || walk(e) {
					return true
				}
			}
		}
		return false
	}
	return walk(v)
}

func unescape(tok string) string {
	return strings.NewReplacer("~1", "/", "~0", "~").Replace(tok)
}

// ApplyPatch applies JSON patch operations to a document.
func ApplyPatch(doc json.RawMessage, ops []PatchOp) (json.RawMessage, error) {
	var root any
	if err := json.Unmarshal(doc, &root); err != nil {
		return nil, err
	}
	for i, op := range ops {
		toks := strings.Split(strings.TrimPrefix(op.Path, "/"), "/")
		for j := range toks {
			toks[j] = unescape(toks[j])
		}
		var val any
		if op.Op != "remove" {
			if err := json.Unmarshal(op.Value, &val); err != nil {
				return nil, refuse("operation %d: invalid value", i+1)
			}
		}
		var err error
		if root, err = patchAt(root, toks, op.Op, val); err != nil {
			return nil, refuse("operation %d (%s %s): %v", i+1, op.Op, op.Path, err)
		}
	}
	return json.Marshal(root)
}

func patchAt(node any, toks []string, op string, val any) (any, error) {
	key := toks[0]
	last := len(toks) == 1
	switch n := node.(type) {
	case map[string]any:
		if last {
			_, exists := n[key]
			switch op {
			case "add":
				n[key] = val
			case "replace":
				if !exists {
					return nil, fmt.Errorf("%q does not exist", key)
				}
				n[key] = val
			case "remove":
				if !exists {
					return nil, fmt.Errorf("%q does not exist", key)
				}
				delete(n, key)
			}
			return n, nil
		}
		child, ok := n[key]
		if !ok {
			return nil, fmt.Errorf("%q does not exist", key)
		}
		c, err := patchAt(child, toks[1:], op, val)
		if err != nil {
			return nil, err
		}
		n[key] = c
		return n, nil
	case []any:
		if last && key == "-" && op == "add" {
			return append(n, val), nil
		}
		idx, err := strconv.Atoi(key)
		if err != nil || idx < 0 || idx > len(n) || (idx == len(n) && !(last && op == "add")) {
			return nil, fmt.Errorf("index %s out of range", key)
		}
		if last {
			switch op {
			case "add":
				n = append(n[:idx], append([]any{val}, n[idx:]...)...)
			case "replace":
				n[idx] = val
			case "remove":
				n = append(n[:idx], n[idx+1:]...)
			}
			return n, nil
		}
		c, err := patchAt(n[idx], toks[1:], op, val)
		if err != nil {
			return nil, err
		}
		n[idx] = c
		return n, nil
	default:
		return nil, fmt.Errorf("cannot descend into %q", key)
	}
}

func enabled(s tasks.StepDef) bool { return s.Enabled == nil || *s.Enabled }

func stepType(s tasks.StepDef) string {
	if s.Type == "" {
		return tasks.StepAgent
	}
	return s.Type
}

// protected reports whether a step guards what you approve or what reaches your repository.
func protected(s tasks.StepDef) bool {
	return stepType(s) != tasks.StepAgent || s.Review || slices.Contains(GateAgents, s.Agent)
}

// CheckTemplate compares a proposed template definition with the current one.
// It may change step agents, account/model pins and add steps; it may not
// remove, disable or weaken review, gate, workspace, quality or deliver steps
// (or the agents behind gates), narrow what a review includes, change where a
// revision starts, lower the revision limit, change any runtime, or change the
// task source and destination.
func CheckTemplate(before, after tasks.TemplateDef) error {
	if !reflect.DeepEqual(before.Source, after.Source) {
		return refuse("template proposals may not change how tasks enter the workflow (source)")
	}
	if !reflect.DeepEqual(before.Destination, after.Destination) {
		return refuse("template proposals may not change the destination")
	}
	switch {
	case before.MaxRevisions == nil && after.MaxRevisions != nil:
		return refuse("template proposals may not set a revision limit (it could lower the default)")
	case before.MaxRevisions != nil && (after.MaxRevisions == nil || *after.MaxRevisions < *before.MaxRevisions):
		return refuse("template proposals may not lower or remove the revision limit")
	}
	afterByID := map[string]tasks.StepDef{}
	for _, s := range after.Steps {
		afterByID[s.ID] = s
	}
	beforeByID := map[string]tasks.StepDef{}
	for _, b := range before.Steps {
		beforeByID[b.ID] = b
		a, ok := afterByID[b.ID]
		if !ok {
			if protected(b) {
				return refuse("step %q (%s) may not be removed", b.ID, describe(b))
			}
			if b.Runtime != "" {
				return refuse("step %q runs on %s and may not be removed (its runtime is not the proposal's to change)", b.ID, b.Runtime)
			}
			continue
		}
		if a.Runtime != b.Runtime {
			return refuse("step %q: the runtime may not be changed by a proposal", b.ID)
		}
		if gateGuard(b) && (a.Account != b.Account || a.Model != b.Model || a.DiagnoseWith != b.DiagnoseWith) {
			return refuse("step %q: who runs a quality-gate step (account, model, diagnosis) may not change", b.ID)
		}
		if !protected(b) {
			continue
		}
		switch {
		case stepType(a) != stepType(b):
			return refuse("step %q: its type may not change", b.ID)
		case enabled(b) && !enabled(a):
			return refuse("step %q (%s) may not be disabled", b.ID, describe(b))
		case b.Review && !a.Review:
			return refuse("step %q: its review may not be removed", b.ID)
		case a.ReviseFrom != b.ReviseFrom:
			return refuse("step %q: where a revision starts (reviseFrom) may not change", b.ID)
		case a.FixFrom != b.FixFrom:
			return refuse("step %q: which step fixes gate failures may not change", b.ID)
		case slices.Contains(GateAgents, b.Agent) && a.Agent != b.Agent:
			return refuse("step %q: the %s behind a quality gate may not be replaced", b.ID, b.Agent)
		}
		for _, inc := range b.ReviewIncludes {
			if !slices.Contains(a.ReviewIncludes, inc) {
				return refuse("step %q: the review may not stop including %q", b.ID, inc)
			}
		}
	}
	for _, a := range after.Steps {
		if _, existed := beforeByID[a.ID]; !existed && a.Runtime != "" {
			return refuse("new step %q may not set a runtime", a.ID)
		}
	}
	return nil
}

// gateGuard: gate steps and the reviewer/security steps behind them.
func gateGuard(s tasks.StepDef) bool {
	return stepType(s) == tasks.StepGate || slices.Contains(GateAgents, s.Agent)
}

// CheckResolved compares the resolved plans (defaults applied): the step a
// gate's failures go back to, and what each review covers, may not change by
// side effect (e.g. inserting an implementing step or disabling an included one).
func CheckResolved(before, after tasks.Plan) error {
	idx := map[string]tasks.PlanStep{}
	for _, s := range after.Steps {
		idx[s.ID] = s
	}
	for _, b := range before.Steps {
		if !b.Enabled {
			continue
		}
		a, ok := idx[b.ID]
		if !ok {
			continue // removal is checked on the definition
		}
		if b.Type == tasks.StepGate && a.FixFrom != b.FixFrom {
			return refuse("step %q: its failures would go back to %q instead of %q", b.ID, a.FixFrom, b.FixFrom)
		}
		if b.Review {
			for _, k := range b.ReviewKinds {
				if !slices.Contains(a.ReviewKinds, k) {
					return refuse("step %q: the review would no longer include %s", b.ID, k)
				}
			}
		}
	}
	return nil
}

func describe(s tasks.StepDef) string {
	switch {
	case s.Review:
		return "reviewed"
	case stepType(s) != tasks.StepAgent:
		return stepType(s)
	default:
		return s.Agent
	}
}

// MemoryOp is one memory change.
type MemoryOp struct {
	Op        string   `json:"op"` // create, archive, merge
	IDs       []string `json:"ids,omitempty"`
	Scope     string   `json:"scope,omitempty"`
	ProjectID string   `json:"project_id,omitempty"`
	Kind      string   `json:"kind,omitempty"`
	Category  string   `json:"category,omitempty"`
	Title     string   `json:"title,omitempty"`
	Body      string   `json:"body,omitempty"`
}

// CheckMemoryOps validates the shape of memory operations.
func CheckMemoryOps(ops []MemoryOp) error {
	if len(ops) == 0 || len(ops) > 20 {
		return refuse("a memory proposal needs 1-20 operations")
	}
	for i, op := range ops {
		switch op.Op {
		case "create":
			if len(op.IDs) > 0 {
				return refuse("operation %d: create takes no ids", i+1)
			}
			if strings.TrimSpace(op.Title) == "" || strings.TrimSpace(op.Body) == "" {
				return refuse("operation %d: a new memory needs a title and a body", i+1)
			}
		case "archive":
			if len(op.IDs) == 0 {
				return refuse("operation %d: archive needs the memory ids", i+1)
			}
		case "merge":
			if len(op.IDs) < 2 {
				return refuse("operation %d: merge needs at least two memories", i+1)
			}
			if strings.TrimSpace(op.Title) == "" || strings.TrimSpace(op.Body) == "" {
				return refuse("operation %d: the merged memory needs a title and a body", i+1)
			}
		default:
			return refuse("operation %d: %q is not a memory operation (create, archive, merge)", i+1, op.Op)
		}
	}
	return nil
}
