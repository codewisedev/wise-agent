package improvement

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"

	"github.com/google/uuid"

	"wise/apps/api/internal/agents"
	"wise/apps/api/internal/apperr"
	"wise/apps/api/internal/audit"
	"wise/apps/api/internal/db"
	"wise/apps/api/internal/db/dbgen"
	"wise/apps/api/internal/schemas"
	"wise/apps/api/internal/secretguard"
)

// Replay evaluation: a proposal is compared with the current version on cases
// built from past runs (approved outputs are "golden", outputs you rejected
// with a comment are "must fix", and you can add your own). Each case runs
// twice — baseline and candidate — as evaluation replays (excluded from
// metrics, run lists and budgets), then the judge agent scores both outputs,
// shown as A and B in random order.

// Case kinds.
const (
	CaseGolden  = "golden"
	CaseMustFix = "must_fix"
	CaseCustom  = "custom"
)

const goldenExpectation = "The earlier output for this input was accepted as is; a good output keeps what made it acceptable."

type agentInfo struct {
	row         dbgen.AgentDefinition
	def         agents.Definition
	criteria    []string
	required    []string
	replayable  bool
	replayError string
}

func (s *Service) agentInfo(ctx context.Context, name string) (agentInfo, error) {
	row, err := s.db.GetCurrentAgentDefinitionByName(ctx, name)
	if db.IsNotFound(err) {
		return agentInfo{}, apperr.NotFound(fmt.Sprintf("agent %q not found", name))
	}
	if err != nil {
		return agentInfo{}, err
	}
	return describeAgent(row)
}

func describeAgent(row dbgen.AgentDefinition) (agentInfo, error) {
	def, err := agents.ParseDefinition(row.Definition)
	if err != nil {
		return agentInfo{}, err
	}
	var extra struct {
		Evaluation struct {
			Criteria []string `json:"criteria"`
		} `json:"evaluation"`
	}
	_ = json.Unmarshal(row.Definition, &extra)
	var out struct {
		Required []string `json:"required"`
	}
	_ = json.Unmarshal(def.OutputSchema, &out)
	a := agentInfo{row: row, def: def, criteria: extra.Evaluation.Criteria, required: out.Required, replayable: true}
	for _, t := range def.Tools {
		if strings.HasPrefix(t, "ws_") {
			a.replayable, a.replayError = false, "it works in a task workspace, which evaluations cannot recreate yet"
		}
	}
	if row.Name == Agent || row.Name == Judge {
		a.replayable, a.replayError = false, "Wise's own analysis agents are not evaluated"
	}
	return a, nil
}

// SyncCases adds cases from your reviews and ratings that are not in the suite yet.
func (s *Service) SyncCases(ctx context.Context, agent string) error {
	rows, err := s.db.EvalCaseSources(ctx, agent)
	if err != nil {
		return err
	}
	seen := map[uuid.UUID]bool{}
	for _, r := range rows {
		if seen[r.RunID] {
			continue
		}
		seen[r.RunID] = true
		kind, exp := CaseGolden, goldenExpectation
		if r.Decision == "rejected" {
			if strings.TrimSpace(r.Note) == "" {
				continue // a rejection without a comment says nothing to check against
			}
			kind, exp = CaseMustFix, clip(r.Note, 4000)
		} else if strings.TrimSpace(r.Note) != "" {
			exp = goldenExpectation + " Your note: " + clip(r.Note, 3000)
		}
		id, _ := uuid.NewV7()
		runID := r.RunID
		if _, err := s.db.InsertEvalCase(ctx, dbgen.InsertEvalCaseParams{ID: id, AgentName: agent, Kind: kind, Input: r.Input,
			ProjectID: r.ProjectID, SourceRunID: &runID, Source: r.Source, Expectation: exp}); err != nil && !db.IsNotFound(err) {
			return err
		}
	}
	return nil
}

// Cases returns an agent's suite (after adding new cases from your reviews and ratings).
func (s *Service) Cases(ctx context.Context, agent string) ([]dbgen.EvalCase, error) {
	if _, err := s.agentInfo(ctx, agent); err != nil {
		return nil, err
	}
	if err := s.SyncCases(ctx, agent); err != nil {
		return nil, err
	}
	return s.db.ListEvalCases(ctx, agent)
}

// AddCase adds your own case (input validated against the agent's input schema).
func (s *Service) AddCase(ctx context.Context, agent string, input json.RawMessage, projectID *uuid.UUID, expectation string, userID *uuid.UUID) (dbgen.EvalCase, error) {
	a, err := s.agentInfo(ctx, agent)
	if err != nil {
		return dbgen.EvalCase{}, err
	}
	if err := schemas.Validate(a.def.InputSchema, input, "input"); err != nil {
		return dbgen.EvalCase{}, err
	}
	if len(expectation) > 4000 {
		return dbgen.EvalCase{}, apperr.Validation(apperr.FieldError{Field: "expectation", Message: "at most 4000 characters"})
	}
	id, _ := uuid.NewV7()
	var c dbgen.EvalCase
	err = s.db.InTx(ctx, func(q *dbgen.Queries) error {
		var err error
		if c, err = q.InsertEvalCase(ctx, dbgen.InsertEvalCaseParams{ID: id, AgentName: agent, Kind: CaseCustom, Input: input, ProjectID: projectID,
			Source: "manual", Expectation: strings.TrimSpace(expectation), CreatedBy: userID}); err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{Action: "eval_case.create", EntityType: "eval_case", EntityID: &id, Payload: map[string]any{"agent": agent}})
	})
	return c, err
}

// UpdateCase switches a case on or off and edits its expectation.
func (s *Service) UpdateCase(ctx context.Context, id uuid.UUID, enabled bool, expectation string) (dbgen.EvalCase, error) {
	if len(expectation) > 4000 {
		return dbgen.EvalCase{}, apperr.Validation(apperr.FieldError{Field: "expectation", Message: "at most 4000 characters"})
	}
	var c dbgen.EvalCase
	err := s.db.InTx(ctx, func(q *dbgen.Queries) error {
		var err error
		c, err = q.UpdateEvalCase(ctx, dbgen.UpdateEvalCaseParams{ID: id, Enabled: enabled, Expectation: strings.TrimSpace(expectation)})
		if db.IsNotFound(err) {
			return apperr.NotFound("case not found")
		}
		if err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{Action: "eval_case.update", EntityType: "eval_case", EntityID: &id, Payload: map[string]any{"enabled": enabled}})
	})
	return c, err
}

// DeleteCase removes one of your own cases (cases from reviews are switched off instead, or they would come back).
func (s *Service) DeleteCase(ctx context.Context, id uuid.UUID) error {
	return s.db.InTx(ctx, func(q *dbgen.Queries) error {
		c, err := q.GetEvalCase(ctx, id)
		if db.IsNotFound(err) {
			return apperr.NotFound("case not found")
		}
		if err != nil {
			return err
		}
		if c.Kind != CaseCustom {
			return apperr.Conflict("cases from your reviews and ratings are switched off, not deleted")
		}
		if err := q.DeleteEvalCase(ctx, id); err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{Action: "eval_case.delete", EntityType: "eval_case", EntityID: &id})
	})
}

// EvalPlan says whether and how a proposal can be evaluated.
type EvalPlan struct {
	Evaluable        bool         `json:"evaluable"`
	Reason           string       `json:"reason,omitempty"`
	Agent            string       `json:"agent,omitempty"`
	Agents           []string     `json:"agents,omitempty"` // routing: the agents the change affects (choose one)
	Cases            int          `json:"cases"`
	EstimatedCostUSD float64      `json:"estimatedCostUsd"`
	Defaults         EvalDefaults `json:"defaults"`
	ChoosesModel     bool         `json:"choosesModel"` // false for routing: the models are what is compared
}

type routeSides struct{ baseline, candidate agents.ModelOverride }

// routingSides returns, per affected agent, the first target of its alias before and after a models patch.
func (s *Service) routingSides(ctx context.Context, p dbgen.ImprovementProposal) (map[string]routeSides, error) {
	var ch RoutingChange
	if err := json.Unmarshal(p.Change, &ch); err != nil || ch.Setting != "models" {
		return nil, nil
	}
	type cfg struct {
		Aliases map[string]struct {
			Targets []target `json:"targets"`
		} `json:"aliases"`
	}
	var before, after cfg
	_ = json.Unmarshal(p.Before, &before)
	_ = json.Unmarshal(ch.Value, &after)
	rows, err := s.db.ListCurrentAgentDefinitions(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]routeSides{}
	for _, r := range rows {
		a, err := describeAgent(r.AgentDefinition)
		if err != nil || !a.replayable || r.AgentDefinition.Archived {
			continue
		}
		alias := a.def.ModelConfig.Alias
		b, aft := before.Aliases[alias].Targets, after.Aliases[alias].Targets
		if len(b) == 0 || len(aft) == 0 || b[0] == aft[0] {
			continue
		}
		out[r.AgentDefinition.Name] = routeSides{
			baseline:  agents.ModelOverride{Account: b[0].Provider, Model: b[0].Model},
			candidate: agents.ModelOverride{Account: aft[0].Provider, Model: aft[0].Model},
		}
	}
	return out, nil
}

// Plan reports whether a proposal can be evaluated, on which agent, and an estimate.
func (s *Service) Plan(ctx context.Context, id uuid.UUID, agent string) (EvalPlan, error) {
	p, err := s.Proposal(ctx, id)
	if err != nil {
		return EvalPlan{}, err
	}
	st := s.Settings()
	plan := EvalPlan{Defaults: st.Eval, ChoosesModel: true}
	switch p.Kind {
	case KindPrompt:
		plan.Agent = p.Target
	case KindRouting:
		sides, err := s.routingSides(ctx, p)
		if err != nil {
			return plan, err
		}
		for name := range sides {
			plan.Agents = append(plan.Agents, name)
		}
		slices.Sort(plan.Agents)
		plan.ChoosesModel = false
		if len(plan.Agents) == 0 {
			plan.Reason = "this routing change does not change the first model of any replayable agent's alias (budgets and fallbacks are not evaluated)"
			return plan, nil
		}
		plan.Agent = plan.Agents[0]
		if agent != "" {
			if !slices.Contains(plan.Agents, agent) {
				return plan, apperr.Validation(apperr.FieldError{Field: "agent", Message: "not an agent this change affects"})
			}
			plan.Agent = agent
		}
	default:
		plan.Reason = "memory and template proposals are not evaluated: judge them by their evidence and diff"
		return plan, nil
	}
	if !open(p.Status) {
		plan.Reason = "only open proposals can be evaluated (this one is " + strings.ReplaceAll(p.Status, "_", " ") + ")"
		return plan, nil
	}
	a, err := s.agentInfo(ctx, plan.Agent)
	if err != nil {
		return plan, err
	}
	if !a.replayable {
		plan.Reason = plan.Agent + " cannot be replayed: " + a.replayError
		return plan, nil
	}
	if err := s.SyncCases(ctx, plan.Agent); err != nil {
		return plan, err
	}
	cases, err := s.db.EnabledEvalCases(ctx, dbgen.EnabledEvalCasesParams{AgentName: plan.Agent, RowLimit: int32(MaxEvalCases)})
	if err != nil {
		return plan, err
	}
	plan.Cases = len(cases)
	if plan.Cases == 0 {
		plan.Reason = plan.Agent + " has no evaluation cases yet: they come from reviews and ratings of its runs, or add your own on the agent's page"
		return plan, nil
	}
	avg, err := s.db.AgentAverageRunCost(ctx, plan.Agent)
	if err != nil {
		return plan, err
	}
	n := min(plan.Cases, st.Eval.MaxCases)
	plan.EstimatedCostUSD = round2(float64(n) * avg * 2.3) // baseline + candidate + a judge (~30% of a run)
	plan.Evaluable = true
	return plan, nil
}

// EvalOptions are the choices made in the Evaluate dialog.
type EvalOptions struct {
	Agent      string
	Model      ModelChoice // prompt proposals: who runs both sides (empty = the agent's routing)
	Judge      ModelChoice
	MaxCases   int
	CostCapUSD float64
}

// ErrEvalRunning means the proposal is already being evaluated.
var ErrEvalRunning = errors.New("this proposal is already being evaluated")

func override(m ModelChoice) *agents.ModelOverride {
	if m.Account == "" {
		return nil
	}
	return &agents.ModelOverride{Account: m.Account, Model: m.Model}
}

// Evaluate starts an evaluation of a proposal.
func (s *Service) Evaluate(ctx context.Context, id uuid.UUID, o EvalOptions, userID *uuid.UUID) (dbgen.EvalRun, error) {
	plan, err := s.Plan(ctx, id, o.Agent)
	if err != nil {
		return dbgen.EvalRun{}, err
	}
	if !plan.Evaluable {
		return dbgen.EvalRun{}, apperr.Conflict(plan.Reason)
	}
	if o.MaxCases <= 0 {
		o.MaxCases = plan.Defaults.MaxCases
	}
	if o.CostCapUSD <= 0 {
		o.CostCapUSD = plan.Defaults.CostCapUSD
	}
	if o.MaxCases > MaxEvalCases || o.CostCapUSD > 100 {
		return dbgen.EvalRun{}, apperr.Validation(apperr.FieldError{Field: "maxCases", Message: fmt.Sprintf("at most %d cases and $100", MaxEvalCases)})
	}
	p, err := s.Proposal(ctx, id)
	if err != nil {
		return dbgen.EvalRun{}, err
	}
	a, err := s.agentInfo(ctx, plan.Agent)
	if err != nil {
		return dbgen.EvalRun{}, err
	}
	baselineDef, candidateDef := a.row, a.row
	var baseModel, candModel *agents.ModelOverride
	switch p.Kind {
	case KindPrompt:
		var ch PromptChange
		if err := json.Unmarshal(p.Change, &ch); err != nil {
			return dbgen.EvalRun{}, err
		}
		if candidateDef, err = s.agents.Candidate(ctx, plan.Agent, ch.Prompt, userID); err != nil {
			return dbgen.EvalRun{}, err
		}
		baseModel, candModel = override(o.Model), override(o.Model)
	case KindRouting:
		sides, err := s.routingSides(ctx, p)
		if err != nil {
			return dbgen.EvalRun{}, err
		}
		sd := sides[plan.Agent]
		baseModel, candModel = &sd.baseline, &sd.candidate
	}
	cases, err := s.db.EnabledEvalCases(ctx, dbgen.EnabledEvalCasesParams{AgentName: plan.Agent, RowLimit: int32(o.MaxCases)})
	if err != nil {
		return dbgen.EvalRun{}, err
	}
	runID, _ := uuid.NewV7()
	var er dbgen.EvalRun
	var results []dbgen.EvalResult
	err = s.db.InTx(ctx, func(q *dbgen.Queries) error {
		cur, err := q.LockImprovementProposal(ctx, id)
		if err != nil {
			return err
		}
		if !open(cur.Status) {
			return apperr.Conflict("the proposal is " + cur.Status)
		}
		if er, err = q.InsertEvalRun(ctx, dbgen.InsertEvalRunParams{ID: runID, ProposalID: id, AgentName: plan.Agent,
			BaselineDefinitionID: baselineDef.ID, CandidateDefinitionID: candidateDef.ID, BaselineModel: jsonOrNil(baseModel),
			CandidateModel: jsonOrNil(candModel), JudgeModel: jsonOrNil(override(o.Judge)), CostCapUsd: o.CostCapUSD, CreatedBy: userID}); err != nil {
			if db.IsUniqueViolation(err) {
				return apperr.Conflict(ErrEvalRunning.Error()).WithCause(ErrEvalRunning)
			}
			return err
		}
		for _, c := range cases {
			rid, _ := uuid.NewV7()
			cid := c.ID
			r, err := q.InsertEvalResult(ctx, dbgen.InsertEvalResultParams{ID: rid, EvalRunID: runID, CaseID: &cid, CaseKind: c.Kind, Expectation: c.Expectation})
			if err != nil {
				return err
			}
			results = append(results, r)
		}
		if _, err := q.SetImprovementProposalStatus(ctx, dbgen.SetImprovementProposalStatusParams{ID: id, Status: "evaluating"}); err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{Action: "improvement.evaluate", EntityType: "improvement_proposal", EntityID: &id,
			Payload: map[string]any{"agent": plan.Agent, "cases": len(cases), "costCapUsd": o.CostCapUSD, "model": o.Model, "judge": o.Judge}})
	})
	if err != nil {
		return er, err
	}
	byID := map[uuid.UUID]dbgen.EvalCase{}
	for _, c := range cases {
		byID[c.ID] = c
	}
	for _, r := range results {
		c := byID[*r.CaseID]
		var input map[string]any
		_ = json.Unmarshal(c.Input, &input)
		run := func(side string, def uuid.UUID, m *agents.ModelOverride) (*uuid.UUID, error) {
			ar, err := s.agents.Enqueue(ctx, plan.Agent, input, agents.RunOptions{ProjectID: c.ProjectID, UserID: userID, Model: m,
				DefinitionID: &def, Purpose: "eval", IdempotencyKey: "eval:" + r.ID.String() + ":" + side})
			if err != nil {
				return nil, err
			}
			return &ar.ID, nil
		}
		b, errB := run("baseline", baselineDef.ID, baseModel)
		c2, errC := run("candidate", candidateDef.ID, candModel)
		if err := s.db.SetEvalResultRuns(ctx, dbgen.SetEvalResultRunsParams{ID: r.ID, BaselineRunID: b, CandidateRunID: c2}); err != nil {
			return er, err
		}
		if err := errors.Join(errB, errC); err != nil {
			msg := "could not start the replay: " + clip(err.Error(), 300)
			_ = s.db.FinishEvalResult(ctx, dbgen.FinishEvalResultParams{ID: r.ID, Status: "failed", Error: &msg})
		}
	}
	s.maybeFinishEval(ctx, runID)
	return er, nil
}

func jsonOrNil(v *agents.ModelOverride) []byte {
	if v == nil {
		return nil
	}
	return mustJSON(v)
}

// SideCheck is the deterministic checks of one output.
type SideCheck struct {
	Valid           bool     `json:"valid"` // the run produced an output matching the contract
	Status          string   `json:"status"`
	InvalidAttempts int64    `json:"invalidAttempts"`
	EmptyRequired   []string `json:"emptyRequired,omitempty"`
	Secret          bool     `json:"secret"`
	CostUSD         float64  `json:"costUsd"`
}

func empty(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(x) == ""
	case []any:
		return len(x) == 0
	case map[string]any:
		return len(x) == 0
	}
	return false
}

// Check runs the deterministic checks on a finished run.
func Check(run dbgen.AgentRun, required []string, invalidAttempts int64) SideCheck {
	c := SideCheck{Valid: run.Status == dbgen.AgentRunStatusSucceeded && len(run.Output) > 0, Status: string(run.Status),
		InvalidAttempts: invalidAttempts, CostUSD: run.TotalCostUsd}
	if !c.Valid {
		return c
	}
	var out map[string]any
	_ = json.Unmarshal(run.Output, &out)
	for _, k := range required {
		if empty(out[k]) {
			c.EmptyRequired = append(c.EmptyRequired, k)
		}
	}
	c.Secret = secretguard.HasToken(string(run.Output))
	return c
}

func terminal(st dbgen.AgentRunStatus) bool {
	return st == dbgen.AgentRunStatusSucceeded || st == dbgen.AgentRunStatusFailed || st == dbgen.AgentRunStatusCancelled
}

// onEvalRun advances the evaluation a finished run belongs to (if any).
func (s *Service) onEvalRun(ctx context.Context, runID uuid.UUID) error {
	r, err := s.db.EvalResultByRun(ctx, &runID)
	if db.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return s.advance(ctx, r.ID)
}

func prettyClip(raw []byte, n int) string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return clip(string(raw), n)
	}
	b, _ := json.MarshalIndent(v, "", " ")
	return clip(string(b), n)
}

// advance moves one case forward: replays done → checks and judge; judge done → scores.
func (s *Service) advance(ctx context.Context, resultID uuid.UUID) error {
	var needJudge bool
	var evalRunID uuid.UUID
	err := s.db.InTx(ctx, func(q *dbgen.Queries) error {
		r, err := q.LockEvalResult(ctx, resultID)
		if err != nil {
			return err
		}
		evalRunID = r.EvalRunID
		er, err := q.GetEvalRun(ctx, r.EvalRunID)
		if err != nil || er.Status != "running" {
			return err
		}
		switch r.Status {
		case "running":
			if r.BaselineRunID == nil || r.CandidateRunID == nil {
				return nil
			}
			base, err := q.GetAgentRun(ctx, *r.BaselineRunID)
			if err != nil {
				return err
			}
			cand, err := q.GetAgentRun(ctx, *r.CandidateRunID)
			if err != nil {
				return err
			}
			if !terminal(base.Status) || !terminal(cand.Status) {
				return nil
			}
			a, err := describeAgent(dbgen.AgentDefinition{Name: er.AgentName, Definition: s.definitionOf(ctx, q, er.BaselineDefinitionID)})
			if err != nil {
				return err
			}
			bi, _ := q.RunInvalidOutputs(ctx, base.ID)
			ci, _ := q.RunInvalidOutputs(ctx, cand.ID)
			checks := mustJSON(map[string]SideCheck{"baseline": Check(base, a.required, bi), "candidate": Check(cand, a.required, ci)})
			if base.Status != dbgen.AgentRunStatusSucceeded || cand.Status != dbgen.AgentRunStatusSucceeded {
				msg := "not judged: an output did not meet the contract"
				return q.FinishEvalResult(ctx, dbgen.FinishEvalResultParams{ID: r.ID, Status: "done", Checks: checks, Error: &msg})
			}
			aIs := "baseline"
			if rand.IntN(2) == 1 {
				aIs = "candidate"
			}
			needJudge = true
			return q.StartEvalJudging(ctx, dbgen.StartEvalJudgingParams{ID: r.ID, Checks: checks, AIs: &aIs})
		case "judging":
			if r.JudgeRunID == nil {
				needJudge = true
				return nil
			}
			j, err := q.GetAgentRun(ctx, *r.JudgeRunID)
			if err != nil || !terminal(j.Status) {
				return err
			}
			if j.Status != dbgen.AgentRunStatusSucceeded {
				msg := "the judge did not finish (" + string(j.Status) + ")"
				if j.Error != nil {
					msg += ": " + clip(*j.Error, 300)
				}
				return q.FinishEvalResult(ctx, dbgen.FinishEvalResultParams{ID: r.ID, Status: "failed", Error: &msg})
			}
			var out struct {
				A         int    `json:"score_a"`
				B         int    `json:"score_b"`
				Reasoning string `json:"reasoning"`
			}
			if err := json.Unmarshal(j.Output, &out); err != nil || out.A < 1 || out.A > 5 || out.B < 1 || out.B > 5 {
				msg := "the judge's scores are unreadable"
				return q.FinishEvalResult(ctx, dbgen.FinishEvalResultParams{ID: r.ID, Status: "failed", Error: &msg})
			}
			base, cand := int32(out.A), int32(out.B)
			if r.AIs != nil && *r.AIs == "candidate" {
				base, cand = cand, base
			}
			reasoning := clip(out.Reasoning, 1500)
			return q.FinishEvalResult(ctx, dbgen.FinishEvalResultParams{ID: r.ID, Status: "done", BaselineScore: &base, CandidateScore: &cand, Reasoning: &reasoning})
		}
		return nil
	})
	if err != nil {
		return err
	}
	if needJudge {
		if err := s.startJudge(ctx, resultID); err != nil {
			s.log.Warn("evaluation: start judge", "result", resultID, "err", err)
		}
	}
	s.maybeFinishEval(ctx, evalRunID)
	return nil
}

func (s *Service) definitionOf(ctx context.Context, q *dbgen.Queries, id uuid.UUID) json.RawMessage {
	if d, err := q.GetAgentDefinition(ctx, id); err == nil {
		return d.Definition
	}
	return nil
}

// startJudge queues the judge for a case (idempotent), unless the cost cap is reached.
func (s *Service) startJudge(ctx context.Context, resultID uuid.UUID) error {
	r, err := s.db.GetEvalResult(ctx, resultID)
	if err != nil || r.Status != "judging" || r.JudgeRunID != nil {
		return err
	}
	er, err := s.db.GetEvalRun(ctx, r.EvalRunID)
	if err != nil || er.Status != "running" {
		return err
	}
	cost, err := s.db.EvalRunCost(ctx, er.ID)
	if err != nil {
		return err
	}
	if cost >= er.CostCapUsd {
		s.stopEval(ctx, er.ID, fmt.Sprintf("stopped: the cost cap of $%.2f was reached ($%.2f spent)", er.CostCapUsd, cost))
		return nil
	}
	a, err := describeAgent(dbgen.AgentDefinition{Name: er.AgentName, Definition: s.definitionOf(ctx, s.db.Queries, er.BaselineDefinitionID)})
	if err != nil {
		return err
	}
	base, err := s.db.GetAgentRun(ctx, *r.BaselineRunID)
	if err != nil {
		return err
	}
	cand, err := s.db.GetAgentRun(ctx, *r.CandidateRunID)
	if err != nil {
		return err
	}
	outA, outB := base.Output, cand.Output
	if r.AIs != nil && *r.AIs == "candidate" {
		outA, outB = outB, outA
	}
	input := map[string]any{"agent": er.AgentName, "agent_description": clip(a.def.Description, 500), "criteria": a.criteria,
		"case_kind": r.CaseKind, "expectation": r.Expectation, "input": prettyClip(base.Input, 8000),
		"output_a": prettyClip(outA, 12000), "output_b": prettyClip(outB, 12000)}
	if a.criteria == nil {
		input["criteria"] = []string{}
	}
	var jm *agents.ModelOverride
	if len(er.JudgeModel) > 0 {
		jm = &agents.ModelOverride{}
		_ = json.Unmarshal(er.JudgeModel, jm)
	}
	j, err := s.agents.Enqueue(ctx, Judge, input, agents.RunOptions{UserID: er.CreatedBy, Model: jm, Purpose: "eval",
		IdempotencyKey: "eval:" + r.ID.String() + ":judge"})
	if err != nil {
		msg := "could not start the judge: " + clip(err.Error(), 300)
		return s.db.FinishEvalResult(ctx, dbgen.FinishEvalResultParams{ID: r.ID, Status: "failed", Error: &msg})
	}
	return s.db.SetEvalJudgeRun(ctx, dbgen.SetEvalJudgeRunParams{ID: r.ID, JudgeRunID: &j.ID})
}

// stopEval ends an evaluation early (cost cap or cancel): unfinished cases fail, active runs are cancelled.
func (s *Service) stopEval(ctx context.Context, evalRunID uuid.UUID, reason string) {
	ids, _ := s.db.ActiveEvalRunIDs(ctx, evalRunID)
	results, _ := s.db.ListEvalResults(ctx, evalRunID)
	for _, r := range results {
		if r.Status == "running" || r.Status == "judging" {
			_ = s.db.FinishEvalResult(ctx, dbgen.FinishEvalResultParams{ID: r.ID, Status: "failed", Error: &reason})
		}
	}
	s.finishEval(ctx, evalRunID, "cancelled", reason)
	for _, id := range ids {
		_, _ = s.agents.CancelRun(ctx, id)
	}
}

// CancelEval stops a running evaluation of a proposal.
func (s *Service) CancelEval(ctx context.Context, evalRunID uuid.UUID) error {
	er, err := s.db.GetEvalRun(ctx, evalRunID)
	if db.IsNotFound(err) {
		return apperr.NotFound("evaluation not found")
	}
	if err != nil {
		return err
	}
	if er.Status != "running" {
		return apperr.Conflict("the evaluation is " + er.Status)
	}
	s.stopEval(ctx, evalRunID, "cancelled by you")
	return nil
}

// Summary aggregates an evaluation's results. With few cases it reports
// counts, not a verdict.
type Summary struct {
	Cases            int     `json:"cases"`
	Judged           int     `json:"judged"`
	Failed           int     `json:"failed"`
	BaselineMean     float64 `json:"baselineMean"`
	CandidateMean    float64 `json:"candidateMean"`
	CandidateBetter  int     `json:"candidateBetter"`
	Same             int     `json:"same"`
	CandidateWorse   int     `json:"candidateWorse"`
	BaselineInvalid  int     `json:"baselineInvalid"`
	CandidateInvalid int     `json:"candidateInvalid"`
	CandidateSecrets int     `json:"candidateSecrets"`
	CostUSD          float64 `json:"costUsd"`
}

// Summarize aggregates results.
func Summarize(results []dbgen.EvalResult) Summary {
	var s Summary
	var bs, cs int
	for _, r := range results {
		s.Cases++
		if r.Status == "failed" {
			s.Failed++
		}
		var checks map[string]SideCheck
		_ = json.Unmarshal(r.Checks, &checks)
		if b, ok := checks["baseline"]; ok && !b.Valid {
			s.BaselineInvalid++
		}
		if c, ok := checks["candidate"]; ok {
			if !c.Valid {
				s.CandidateInvalid++
			}
			if c.Secret {
				s.CandidateSecrets++
			}
		}
		if r.BaselineScore == nil || r.CandidateScore == nil {
			continue
		}
		s.Judged++
		bs += int(*r.BaselineScore)
		cs += int(*r.CandidateScore)
		switch {
		case *r.CandidateScore > *r.BaselineScore:
			s.CandidateBetter++
		case *r.CandidateScore < *r.BaselineScore:
			s.CandidateWorse++
		default:
			s.Same++
		}
	}
	if s.Judged > 0 {
		s.BaselineMean = round2(float64(bs) / float64(s.Judged))
		s.CandidateMean = round2(float64(cs) / float64(s.Judged))
	}
	return s
}

func (s *Service) maybeFinishEval(ctx context.Context, evalRunID uuid.UUID) {
	results, err := s.db.ListEvalResults(ctx, evalRunID)
	if err != nil {
		return
	}
	for _, r := range results {
		if r.Status == "running" || r.Status == "judging" {
			return
		}
	}
	sum := Summarize(results)
	status, msg := "succeeded", ""
	if sum.Cases > 0 && sum.Failed == sum.Cases {
		status, msg = "failed", "every case failed"
	}
	s.finishEval(ctx, evalRunID, status, msg)
}

func (s *Service) finishEval(ctx context.Context, evalRunID uuid.UUID, status, msg string) {
	_ = s.db.InTx(ctx, func(q *dbgen.Queries) error {
		er, err := q.LockEvalRun(ctx, evalRunID)
		if err != nil || er.Status != "running" {
			return err
		}
		results, err := q.ListEvalResults(ctx, evalRunID)
		if err != nil {
			return err
		}
		sum := Summarize(results)
		if sum.CostUSD, err = q.EvalRunCost(ctx, evalRunID); err != nil {
			return err
		}
		sum.CostUSD = float64(int64(sum.CostUSD*10000+0.5)) / 10000
		var errMsg *string
		if msg != "" {
			errMsg = &msg
		}
		if err := q.FinishEvalRun(ctx, dbgen.FinishEvalRunParams{ID: evalRunID, Status: status, Summary: mustJSON(sum), Error: errMsg}); err != nil {
			return err
		}
		p, err := q.LockImprovementProposal(ctx, er.ProposalID)
		if err != nil {
			return err
		}
		if p.Status == "evaluating" {
			if _, err := q.SetImprovementProposalStatus(ctx, dbgen.SetImprovementProposalStatusParams{ID: p.ID, Status: "evaluated"}); err != nil {
				return err
			}
		}
		return audit.Record(ctx, q, audit.Entry{Action: "improvement.evaluated", EntityType: "improvement_proposal", EntityID: &er.ProposalID,
			Payload: map[string]any{"status": status, "summary": sum}})
	})
}

// sweepEvals advances cases whose hook was missed.
func (s *Service) sweepEvals(ctx context.Context) {
	rows, err := s.db.PendingEvalWork(ctx)
	if err != nil {
		return
	}
	for _, r := range rows {
		if err := s.advance(ctx, r.ID); err != nil {
			s.log.Warn("evaluation sweep", "result", r.ID, "err", err)
		}
	}
}

// EvalRunWithResults is one evaluation and its cases.
type EvalRunWithResults struct {
	Run     dbgen.EvalRun
	Results []dbgen.EvalResult
}

// Evals lists a proposal's evaluations.
func (s *Service) Evals(ctx context.Context, proposalID uuid.UUID) ([]EvalRunWithResults, error) {
	runs, err := s.db.ListEvalRunsForProposal(ctx, proposalID)
	if err != nil {
		return nil, err
	}
	out := make([]EvalRunWithResults, 0, len(runs))
	for _, r := range runs {
		res, err := s.db.ListEvalResults(ctx, r.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, EvalRunWithResults{Run: r, Results: res})
	}
	return out, nil
}
