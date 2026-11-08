package httpapi

import (
	"context"

	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"wise/apps/api/internal/apperr"
	"wise/apps/api/internal/db/dbgen"
	"wise/apps/api/internal/httpapi/apigen"
	"wise/apps/api/internal/tasks"
)

func toSprint(r dbgen.ListSprintsRow) apigen.Sprint {
	out := apigen.Sprint{
		Id: r.ID, ProjectId: r.ProjectID, Name: r.Name, Closed: r.Closed, Current: r.IsCurrent,
		TaskCount: int(r.TaskCount), OpenCount: int(r.OpenCount), SourceName: r.SourceName, CreatedAt: r.CreatedAt,
	}
	if r.StartsOn.Valid {
		out.StartsOn = &openapi_types.Date{Time: r.StartsOn.Time}
	}
	if r.EndsOn.Valid {
		out.EndsOn = &openapi_types.Date{Time: r.EndsOn.Time}
	}
	return out
}

func sprintInput(b *apigen.SprintInput) tasks.SprintInput {
	in := tasks.SprintInput{Name: b.Name}
	if b.StartsOn != nil {
		t := b.StartsOn.Time
		in.StartsOn = &t
	}
	if b.EndsOn != nil {
		t := b.EndsOn.Time
		in.EndsOn = &t
	}
	return in
}

// sprintView re-reads a sprint with its counts and current flag.
func (s *Server) sprintView(ctx context.Context, projectID, id uuid.UUID) (apigen.Sprint, error) {
	rows, err := s.Tasks.Sprints(ctx, projectID)
	if err != nil {
		return apigen.Sprint{}, err
	}
	for _, r := range rows {
		if r.ID == id {
			return toSprint(r), nil
		}
	}
	return apigen.Sprint{}, apperr.NotFound("sprint not found")
}

func (s *Server) ListSprints(ctx context.Context, req apigen.ListSprintsRequestObject) (apigen.ListSprintsResponseObject, error) {
	rows, err := s.Tasks.Sprints(ctx, req.ProjectId)
	if err != nil {
		return nil, err
	}
	return apigen.ListSprints200JSONResponse{Items: mapSlice(rows, toSprint)}, nil
}

func (s *Server) CreateSprint(ctx context.Context, req apigen.CreateSprintRequestObject) (apigen.CreateSprintResponseObject, error) {
	sp, err := s.Tasks.CreateSprint(ctx, req.ProjectId, sprintInput(req.Body))
	if err != nil {
		return nil, err
	}
	v, err := s.sprintView(ctx, sp.ProjectID, sp.ID)
	if err != nil {
		return nil, err
	}
	return apigen.CreateSprint201JSONResponse(v), nil
}

func (s *Server) UpdateSprint(ctx context.Context, req apigen.UpdateSprintRequestObject) (apigen.UpdateSprintResponseObject, error) {
	sp, err := s.Tasks.UpdateSprint(ctx, req.SprintId, sprintInput(req.Body))
	if err != nil {
		return nil, err
	}
	v, err := s.sprintView(ctx, sp.ProjectID, sp.ID)
	if err != nil {
		return nil, err
	}
	return apigen.UpdateSprint200JSONResponse(v), nil
}

func (s *Server) DeleteSprint(ctx context.Context, req apigen.DeleteSprintRequestObject) (apigen.DeleteSprintResponseObject, error) {
	if err := s.Tasks.DeleteSprint(ctx, req.SprintId); err != nil {
		return nil, err
	}
	return apigen.DeleteSprint204Response{}, nil
}

func (s *Server) StartSprint(ctx context.Context, req apigen.StartSprintRequestObject) (apigen.StartSprintResponseObject, error) {
	if err := s.Tasks.StartSprint(ctx, req.SprintId); err != nil {
		return nil, err
	}
	return apigen.StartSprint204Response{}, nil
}

func (s *Server) CompleteSprint(ctx context.Context, req apigen.CompleteSprintRequestObject) (apigen.CompleteSprintResponseObject, error) {
	if err := s.Tasks.CompleteSprint(ctx, req.SprintId); err != nil {
		return nil, err
	}
	return apigen.CompleteSprint204Response{}, nil
}
