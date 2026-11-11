package httpapi

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"wise/apps/api/internal/db/dbgen"
	"wise/apps/api/internal/httpapi/apigen"
	"wise/apps/api/internal/improvement"
)

type evalCaseJSON struct {
	ID          uuid.UUID       `json:"id"`
	AgentName   string          `json:"agentName"`
	Kind        string          `json:"kind"`
	Input       json.RawMessage `json:"input"`
	ProjectID   *uuid.UUID      `json:"projectId"`
	SourceRunID *uuid.UUID      `json:"sourceRunId"`
	Source      string          `json:"source"`
	Expectation string          `json:"expectation"`
	Enabled     bool            `json:"enabled"`
	CreatedAt   time.Time       `json:"createdAt"`
}

func toEvalCase(c dbgen.EvalCase) apigen.EvalCase {
	return reshape[apigen.EvalCase](evalCaseJSON{ID: c.ID, AgentName: c.AgentName, Kind: c.Kind, Input: c.Input, ProjectID: c.ProjectID,
		SourceRunID: c.SourceRunID, Source: c.Source, Expectation: c.Expectation, Enabled: c.Enabled, CreatedAt: c.CreatedAt})
}

type evalResultJSON struct {
	ID             uuid.UUID       `json:"id"`
	CaseID         *uuid.UUID      `json:"caseId"`
	CaseKind       string          `json:"caseKind"`
	Expectation    string          `json:"expectation"`
	BaselineRunID  *uuid.UUID      `json:"baselineRunId"`
	CandidateRunID *uuid.UUID      `json:"candidateRunId"`
	JudgeRunID     *uuid.UUID      `json:"judgeRunId"`
	Status         string          `json:"status"`
	Checks         json.RawMessage `json:"checks,omitempty"`
	BaselineScore  *int32          `json:"baselineScore"`
	CandidateScore *int32          `json:"candidateScore"`
	Reasoning      *string         `json:"reasoning"`
	Error          *string         `json:"error"`
}

type evalRunJSON struct {
	ID             uuid.UUID        `json:"id"`
	ProposalID     uuid.UUID        `json:"proposalId"`
	AgentName      string           `json:"agentName"`
	BaselineModel  json.RawMessage  `json:"baselineModel,omitempty"`
	CandidateModel json.RawMessage  `json:"candidateModel,omitempty"`
	JudgeModel     json.RawMessage  `json:"judgeModel,omitempty"`
	Status         string           `json:"status"`
	CostCapUsd     float64          `json:"costCapUsd"`
	Summary        json.RawMessage  `json:"summary,omitempty"`
	Error          *string          `json:"error"`
	CreatedAt      time.Time        `json:"createdAt"`
	FinishedAt     *time.Time       `json:"finishedAt"`
	Results        []evalResultJSON `json:"results"`
}

func toEvalRun(r dbgen.EvalRun, results []dbgen.EvalResult) apigen.EvalRun {
	out := evalRunJSON{ID: r.ID, ProposalID: r.ProposalID, AgentName: r.AgentName, BaselineModel: r.BaselineModel, CandidateModel: r.CandidateModel,
		JudgeModel: r.JudgeModel, Status: r.Status, CostCapUsd: r.CostCapUsd, Summary: r.Summary, Error: r.Error, CreatedAt: r.CreatedAt,
		FinishedAt: r.FinishedAt, Results: []evalResultJSON{}}
	for _, x := range results {
		out.Results = append(out.Results, evalResultJSON{ID: x.ID, CaseID: x.CaseID, CaseKind: x.CaseKind, Expectation: x.Expectation,
			BaselineRunID: x.BaselineRunID, CandidateRunID: x.CandidateRunID, JudgeRunID: x.JudgeRunID, Status: x.Status, Checks: x.Checks,
			BaselineScore: x.BaselineScore, CandidateScore: x.CandidateScore, Reasoning: x.Reasoning, Error: x.Error})
	}
	return reshape[apigen.EvalRun](out)
}

func choice(m *apigen.ModelChoice) improvement.ModelChoice {
	if m == nil {
		return improvement.ModelChoice{}
	}
	return improvement.ModelChoice{Account: deref(m.Account), Model: deref(m.Model)}
}

func (s *Server) ListEvalCases(ctx context.Context, req apigen.ListEvalCasesRequestObject) (apigen.ListEvalCasesResponseObject, error) {
	rows, err := s.Improvement.Cases(ctx, req.AgentName)
	if err != nil {
		return nil, err
	}
	items := make([]apigen.EvalCase, len(rows))
	for i, r := range rows {
		items[i] = toEvalCase(r)
	}
	return apigen.ListEvalCases200JSONResponse{Items: items}, nil
}

func (s *Server) CreateEvalCase(ctx context.Context, req apigen.CreateEvalCaseRequestObject) (apigen.CreateEvalCaseResponseObject, error) {
	uid, err := sessionUser(ctx, "adding an evaluation case")
	if err != nil {
		return nil, err
	}
	input, _ := json.Marshal(req.Body.Input)
	c, err := s.Improvement.AddCase(ctx, req.AgentName, input, req.Body.ProjectId, deref(req.Body.Expectation), uid)
	if err != nil {
		return nil, err
	}
	return apigen.CreateEvalCase201JSONResponse(toEvalCase(c)), nil
}

func (s *Server) UpdateEvalCase(ctx context.Context, req apigen.UpdateEvalCaseRequestObject) (apigen.UpdateEvalCaseResponseObject, error) {
	c, err := s.Improvement.UpdateCase(ctx, req.CaseId, req.Body.Enabled, req.Body.Expectation)
	if err != nil {
		return nil, err
	}
	return apigen.UpdateEvalCase200JSONResponse(toEvalCase(c)), nil
}

func (s *Server) DeleteEvalCase(ctx context.Context, req apigen.DeleteEvalCaseRequestObject) (apigen.DeleteEvalCaseResponseObject, error) {
	if err := s.Improvement.DeleteCase(ctx, req.CaseId); err != nil {
		return nil, err
	}
	return apigen.DeleteEvalCase204Response{}, nil
}

func (s *Server) GetEvalPlan(ctx context.Context, req apigen.GetEvalPlanRequestObject) (apigen.GetEvalPlanResponseObject, error) {
	p, err := s.Improvement.Plan(ctx, req.ProposalId, deref(req.Params.Agent))
	if err != nil {
		return nil, err
	}
	return apigen.GetEvalPlan200JSONResponse(reshape[apigen.EvalPlan](p)), nil
}

func (s *Server) EvaluateProposal(ctx context.Context, req apigen.EvaluateProposalRequestObject) (apigen.EvaluateProposalResponseObject, error) {
	uid, err := sessionUser(ctx, "evaluating a proposal")
	if err != nil {
		return nil, err
	}
	var o improvement.EvalOptions
	if b := req.Body; b != nil {
		o = improvement.EvalOptions{Agent: deref(b.Agent), Model: choice(b.Model), Judge: choice(b.Judge)}
		if b.MaxCases != nil {
			o.MaxCases = *b.MaxCases
		}
		if b.CostCapUsd != nil {
			o.CostCapUSD = float64(*b.CostCapUsd)
		}
	}
	r, err := s.Improvement.Evaluate(ctx, req.ProposalId, o, uid)
	if err != nil {
		return nil, err
	}
	return apigen.EvaluateProposal202JSONResponse(toEvalRun(r, nil)), nil
}

func (s *Server) ListProposalEvals(ctx context.Context, req apigen.ListProposalEvalsRequestObject) (apigen.ListProposalEvalsResponseObject, error) {
	runs, err := s.Improvement.Evals(ctx, req.ProposalId)
	if err != nil {
		return nil, err
	}
	items := make([]apigen.EvalRun, len(runs))
	for i, r := range runs {
		items[i] = toEvalRun(r.Run, r.Results)
	}
	return apigen.ListProposalEvals200JSONResponse{Items: items}, nil
}

func (s *Server) CancelEval(ctx context.Context, req apigen.CancelEvalRequestObject) (apigen.CancelEvalResponseObject, error) {
	if err := s.Improvement.CancelEval(ctx, req.EvalRunId); err != nil {
		return nil, err
	}
	return apigen.CancelEval204Response{}, nil
}
