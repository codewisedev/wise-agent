// Package feedback stores your ratings of agent results. Each rating is
// attributed to the agent and definition version that produced the run or
// artifact (artifacts produced by Wise itself, like a changeset, stay
// unattributed), so Insights and improvement analyses can use it.
package feedback

import (
	"context"
	"strings"

	"github.com/google/uuid"

	"wise/apps/api/internal/apperr"
	"wise/apps/api/internal/audit"
	"wise/apps/api/internal/db"
	"wise/apps/api/internal/db/dbgen"
)

// Service is the feedback service.
type Service struct{ db *db.DB }

// New returns a Service.
func New(d *db.DB) *Service { return &Service{db: d} }

// Input is one rating.
type Input struct {
	TargetType string
	TargetID   uuid.UUID
	Rating     string
	Note       string
}

// Set stores (or replaces) the user's rating of a run or artifact.
func (s *Service) Set(ctx context.Context, userID uuid.UUID, in Input) (dbgen.Feedback, error) {
	in.Note = strings.TrimSpace(in.Note)
	if in.Rating != "up" && in.Rating != "down" {
		return dbgen.Feedback{}, apperr.Validation(apperr.FieldError{Field: "rating", Message: "up or down"})
	}
	if len(in.Note) > 2000 {
		return dbgen.Feedback{}, apperr.Validation(apperr.FieldError{Field: "note", Message: "at most 2000 characters"})
	}
	p := dbgen.UpsertFeedbackParams{TargetType: in.TargetType, TargetID: in.TargetID, Rating: in.Rating, Note: in.Note, UserID: userID}
	var runID *uuid.UUID
	switch in.TargetType {
	case "run":
		runID = &in.TargetID
	case "artifact":
		a, err := s.db.GetArtifact(ctx, in.TargetID)
		if db.IsNotFound(err) {
			return dbgen.Feedback{}, apperr.NotFound("artifact not found")
		}
		if err != nil {
			return dbgen.Feedback{}, err
		}
		p.ArtifactID, p.TaskID, runID = &a.ID, &a.TaskID, a.AgentRunID
	default:
		return dbgen.Feedback{}, apperr.Validation(apperr.FieldError{Field: "targetType", Message: "run or artifact"})
	}
	if runID != nil {
		r, err := s.db.GetAgentRun(ctx, *runID)
		if db.IsNotFound(err) {
			return dbgen.Feedback{}, apperr.NotFound("run not found")
		}
		if err != nil {
			return dbgen.Feedback{}, err
		}
		p.RunID, p.AgentName, p.AgentDefinitionID = &r.ID, &r.AgentName, &r.AgentDefinitionID
		if p.TaskID == nil && r.WorkflowRunID != nil {
			if wr, err := s.db.GetWorkflowRun(ctx, *r.WorkflowRunID); err == nil {
				p.TaskID = &wr.TaskID
			}
		}
	}
	p.ID, _ = uuid.NewV7()
	var out dbgen.Feedback
	err := s.db.InTx(ctx, func(q *dbgen.Queries) error {
		var err error
		if out, err = q.UpsertFeedback(ctx, p); err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{Action: "feedback.set", EntityType: in.TargetType, EntityID: &in.TargetID,
			Payload: map[string]any{"rating": in.Rating, "agent": p.AgentName, "hasNote": in.Note != ""}})
	})
	return out, err
}

// Clear removes the user's rating of a target.
func (s *Service) Clear(ctx context.Context, userID uuid.UUID, targetType string, targetID uuid.UUID) error {
	return s.db.InTx(ctx, func(q *dbgen.Queries) error {
		if _, err := q.DeleteFeedback(ctx, dbgen.DeleteFeedbackParams{UserID: userID, TargetType: targetType, TargetID: targetID}); db.IsNotFound(err) {
			return nil
		} else if err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{Action: "feedback.cleared", EntityType: targetType, EntityID: &targetID})
	})
}

// Filter narrows a listing.
type Filter struct {
	TargetType, Agent, Rating *string
	TargetID, TaskID          *uuid.UUID
	Limit                     int
}

// List returns ratings, newest first.
func (s *Service) List(ctx context.Context, f Filter) ([]dbgen.ListFeedbackRow, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	return s.db.ListFeedback(ctx, dbgen.ListFeedbackParams{TargetType: f.TargetType, TargetID: f.TargetID, AgentName: f.Agent,
		Rating: f.Rating, TaskID: f.TaskID, RowLimit: int32(f.Limit)})
}
