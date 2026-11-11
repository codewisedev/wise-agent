package httpapi

import (
	"context"

	"wise/apps/api/internal/db/dbgen"
	"wise/apps/api/internal/feedback"
	"wise/apps/api/internal/httpapi/apigen"
)

func toFeedback(f dbgen.Feedback, username string) apigen.Feedback {
	out := apigen.Feedback{Id: f.ID, TargetType: apigen.FeedbackTargetType(f.TargetType), TargetId: f.TargetID, AgentName: f.AgentName,
		AgentDefinitionId: f.AgentDefinitionID, TaskId: f.TaskID, Rating: apigen.FeedbackRating(f.Rating), Note: f.Note, UserId: f.UserID,
		CreatedAt: f.CreatedAt, UpdatedAt: f.UpdatedAt}
	if username != "" {
		out.Username = &username
	}
	return out
}

func strPtr[T ~string](p *T) *string {
	if p == nil {
		return nil
	}
	s := string(*p)
	return &s
}

func (s *Server) ListFeedback(ctx context.Context, req apigen.ListFeedbackRequestObject) (apigen.ListFeedbackResponseObject, error) {
	p := req.Params
	f := feedback.Filter{TargetType: strPtr(p.TargetType), TargetID: p.TargetId, Agent: p.Agent, Rating: strPtr(p.Rating), TaskID: p.TaskId}
	if p.Limit != nil {
		f.Limit = *p.Limit
	}
	rows, err := s.Feedback.List(ctx, f)
	if err != nil {
		return nil, err
	}
	items := make([]apigen.Feedback, len(rows))
	for i, r := range rows {
		items[i] = toFeedback(dbgen.Feedback{ID: r.ID, TargetType: r.TargetType, TargetID: r.TargetID, RunID: r.RunID, ArtifactID: r.ArtifactID,
			AgentName: r.AgentName, AgentDefinitionID: r.AgentDefinitionID, TaskID: r.TaskID, Rating: r.Rating, Note: r.Note, UserID: r.UserID,
			CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}, r.Username)
	}
	return apigen.ListFeedback200JSONResponse{Items: items}, nil
}

func (s *Server) SetFeedback(ctx context.Context, req apigen.SetFeedbackRequestObject) (apigen.SetFeedbackResponseObject, error) {
	uid, err := sessionUser(ctx, "rating a result")
	if err != nil {
		return nil, err
	}
	f, err := s.Feedback.Set(ctx, *uid, feedback.Input{TargetType: string(req.Body.TargetType), TargetID: req.Body.TargetId,
		Rating: string(req.Body.Rating), Note: deref(req.Body.Note)})
	if err != nil {
		return nil, err
	}
	return apigen.SetFeedback200JSONResponse(toFeedback(f, "")), nil
}

func (s *Server) ClearFeedback(ctx context.Context, req apigen.ClearFeedbackRequestObject) (apigen.ClearFeedbackResponseObject, error) {
	uid, err := sessionUser(ctx, "removing a rating")
	if err != nil {
		return nil, err
	}
	if err := s.Feedback.Clear(ctx, *uid, string(req.Params.TargetType), req.Params.TargetId); err != nil {
		return nil, err
	}
	return apigen.ClearFeedback204Response{}, nil
}
