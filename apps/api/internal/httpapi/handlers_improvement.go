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

type analysisJSON struct {
	ID         uuid.UUID       `json:"id"`
	Trigger    string          `json:"trigger"`
	Period     *string         `json:"period"`
	WindowDays int32           `json:"windowDays"`
	Status     string          `json:"status"`
	AgentRunID *uuid.UUID      `json:"agentRunId"`
	Proposals  int32           `json:"proposals"`
	Refused    json.RawMessage `json:"refused"`
	Error      *string         `json:"error"`
	CreatedAt  time.Time       `json:"createdAt"`
	FinishedAt *time.Time      `json:"finishedAt"`
}

func toAnalysis(a dbgen.ImprovementAnalysis) apigen.ImprovementAnalysis {
	refused := a.Refused
	if len(refused) == 0 {
		refused = []byte("[]")
	}
	return reshape[apigen.ImprovementAnalysis](analysisJSON{ID: a.ID, Trigger: a.Trigger, Period: a.Period, WindowDays: a.WindowDays,
		Status: a.Status, AgentRunID: a.AgentRunID, Proposals: a.Proposals, Refused: refused, Error: a.Error, CreatedAt: a.CreatedAt,
		FinishedAt: a.FinishedAt})
}

type proposalJSON struct {
	ID            uuid.UUID       `json:"id"`
	AnalysisID    *uuid.UUID      `json:"analysisId"`
	Kind          string          `json:"kind"`
	Target        string          `json:"target"`
	Title         string          `json:"title"`
	Rationale     string          `json:"rationale"`
	Evidence      json.RawMessage `json:"evidence"`
	Change        json.RawMessage `json:"change"`
	Before        json.RawMessage `json:"before"`
	Base          json.RawMessage `json:"base"`
	Status        string          `json:"status"`
	MetricsBefore json.RawMessage `json:"metricsBefore,omitempty"`
	Applied       json.RawMessage `json:"applied,omitempty"`
	DecisionNote  *string         `json:"decisionNote"`
	DecidedAt     *time.Time      `json:"decidedAt"`
	AppliedAt     *time.Time      `json:"appliedAt"`
	RolledBackAt  *time.Time      `json:"rolledBackAt"`
	CreatedAt     time.Time       `json:"createdAt"`
	UpdatedAt     time.Time       `json:"updatedAt"`
}

func toProposal(p dbgen.ImprovementProposal) apigen.ImprovementProposal {
	return reshape[apigen.ImprovementProposal](proposalJSON{ID: p.ID, AnalysisID: p.AnalysisID, Kind: p.Kind, Target: p.Target, Title: p.Title,
		Rationale: p.Rationale, Evidence: p.Evidence, Change: p.Change, Before: p.Before, Base: p.Base, Status: p.Status,
		MetricsBefore: p.MetricsBefore, Applied: p.Applied, DecisionNote: p.DecisionNote, DecidedAt: p.DecidedAt, AppliedAt: p.AppliedAt,
		RolledBackAt: p.RolledBackAt, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt})
}

func (s *Server) ListImprovementAnalyses(ctx context.Context, _ apigen.ListImprovementAnalysesRequestObject) (apigen.ListImprovementAnalysesResponseObject, error) {
	rows, err := s.Improvement.Analyses(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]apigen.ImprovementAnalysis, len(rows))
	for i, r := range rows {
		items[i] = toAnalysis(r)
	}
	return apigen.ListImprovementAnalyses200JSONResponse{Items: items}, nil
}

func (s *Server) StartImprovementAnalysis(ctx context.Context, _ apigen.StartImprovementAnalysisRequestObject) (apigen.StartImprovementAnalysisResponseObject, error) {
	uid, err := sessionUser(ctx, "starting an analysis")
	if err != nil {
		return nil, err
	}
	a, err := s.Improvement.Analyse(ctx, "manual", "", uid)
	if err != nil {
		return nil, err
	}
	return apigen.StartImprovementAnalysis202JSONResponse(toAnalysis(a)), nil
}

func (s *Server) ListImprovementProposals(ctx context.Context, req apigen.ListImprovementProposalsRequestObject) (apigen.ListImprovementProposalsResponseObject, error) {
	p := req.Params
	rows, err := s.Improvement.Proposals(ctx, improvement.Filter{Status: strPtr(p.Status), Kind: strPtr(p.Kind), Target: p.Target})
	if err != nil {
		return nil, err
	}
	n, err := s.Improvement.OpenCount(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]apigen.ImprovementProposal, len(rows))
	for i, r := range rows {
		items[i] = toProposal(r)
	}
	return apigen.ListImprovementProposals200JSONResponse{Items: items, OpenCount: n}, nil
}

func (s *Server) GetImprovementProposal(ctx context.Context, req apigen.GetImprovementProposalRequestObject) (apigen.GetImprovementProposalResponseObject, error) {
	p, err := s.Improvement.Proposal(ctx, req.ProposalId)
	if err != nil {
		return nil, err
	}
	return apigen.GetImprovementProposal200JSONResponse(toProposal(p)), nil
}

func (s *Server) RejectImprovementProposal(ctx context.Context, req apigen.RejectImprovementProposalRequestObject) (apigen.RejectImprovementProposalResponseObject, error) {
	uid, err := sessionUser(ctx, "rejecting a proposal")
	if err != nil {
		return nil, err
	}
	note := ""
	if req.Body != nil {
		note = deref(req.Body.Note)
	}
	p, err := s.Improvement.Reject(ctx, req.ProposalId, note, uid)
	if err != nil {
		return nil, err
	}
	return apigen.RejectImprovementProposal200JSONResponse(toProposal(p)), nil
}
