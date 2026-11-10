package httpapi

import (
	"context"

	"github.com/google/uuid"

	"wise/apps/api/internal/apperr"
	"wise/apps/api/internal/db/dbgen"
	"wise/apps/api/internal/httpapi/apigen"
	"wise/apps/api/internal/workspaces"
)

func toRepoExecution(r workspaces.RepoSettings) apigen.RepositoryExecution {
	out := apigen.RepositoryExecution{Image: r.Image, Setup: r.Setup, Tools: r.Tools, BaseBranch: r.BaseBranch, Env: r.Env,
		Commands: apigen.QualityCommands{Format: r.Commands.Format, Lint: r.Commands.Lint, Typecheck: r.Commands.Typecheck, Test: r.Commands.Test}}
	if out.Setup == nil {
		out.Setup = []string{}
	}
	if out.Tools == nil {
		out.Tools = []string{}
	}
	if out.Env == nil {
		out.Env = map[string]string{}
	}
	return out
}

func fromRepoExecution(b *apigen.RepositoryExecution) workspaces.RepoSettings {
	return workspaces.RepoSettings{Image: b.Image, Setup: b.Setup, Tools: b.Tools, BaseBranch: b.BaseBranch, Env: b.Env,
		Commands: workspaces.Commands{Format: b.Commands.Format, Lint: b.Commands.Lint, Typecheck: b.Commands.Typecheck, Test: b.Commands.Test}}
}

func toWorkspace(w dbgen.Workspace, repoName *string) apigen.Workspace {
	tv, sc := w.ToolVersions, w.Scrubbed
	if tv == nil {
		tv = []string{}
	}
	if sc == nil {
		sc = []string{}
	}
	return apigen.Workspace{
		Id: w.ID, TaskId: w.TaskID, WorkflowRunId: w.WorkflowRunID, RepositoryId: w.RepositoryID, RepositoryName: repoName,
		Status: apigen.WorkspaceStatus(w.Status), Branch: w.Branch, BaseBranch: w.BaseBranch, BaseSha: w.BaseSha, Image: w.Image,
		Log: w.Log, Error: w.Error, ToolVersions: tv, Scrubbed: sc, HeadSha: w.HeadSha, DeliveredBranch: w.DeliveredBranch,
		DeliveredSha: w.DeliveredSha, DeliveredAt: w.DeliveredAt, CreatedAt: w.CreatedAt, UpdatedAt: w.UpdatedAt,
	}
}

func (s *Server) GetRepositoryExecution(ctx context.Context, req apigen.GetRepositoryExecutionRequestObject) (apigen.GetRepositoryExecutionResponseObject, error) {
	rs, _, err := s.Workspaces.RepoSettings(ctx, req.RepositoryId)
	if err != nil {
		return nil, err
	}
	return apigen.GetRepositoryExecution200JSONResponse(toRepoExecution(rs)), nil
}

func (s *Server) SetRepositoryExecution(ctx context.Context, req apigen.SetRepositoryExecutionRequestObject) (apigen.SetRepositoryExecutionResponseObject, error) {
	if _, err := requireSession(ctx, "changing execution settings"); err != nil {
		return nil, err
	}
	rs, err := s.Workspaces.SetRepoSettings(ctx, req.RepositoryId, fromRepoExecution(req.Body))
	if err != nil {
		return nil, err
	}
	return apigen.SetRepositoryExecution200JSONResponse(toRepoExecution(rs)), nil
}

func (s *Server) DetectRepositoryExecution(ctx context.Context, req apigen.DetectRepositoryExecutionRequestObject) (apigen.DetectRepositoryExecutionResponseObject, error) {
	d, err := s.Workspaces.Detect(ctx, s.Repositories, req.RepositoryId)
	if err != nil {
		return nil, err
	}
	return apigen.DetectRepositoryExecution200JSONResponse{Settings: toRepoExecution(d.Settings), Detected: d.Detected, Notes: d.Notes}, nil
}

func (s *Server) GetTaskWorkspace(ctx context.Context, req apigen.GetTaskWorkspaceRequestObject) (apigen.GetTaskWorkspaceResponseObject, error) {
	t, err := s.Workspaces.Task(ctx, req.TaskId)
	if err != nil {
		return nil, err
	}
	avail := s.Workspaces.Available()
	out := apigen.TaskWorkspace{ExecutorAvailable: &avail}
	repo, err := s.Workspaces.TargetRepository(ctx, t)
	if err != nil {
		if e, ok := apperr.As(err); ok && e.Status < 500 {
			msg := e.Detail
			out.Problem = &msg
		} else {
			return nil, err
		}
	} else {
		configured := workspaces.RepoSettingsFrom(repo.Settings).Image != ""
		out.Repository = &apigen.RepositoryRef{Id: repo.ID, Name: repo.Name, Configured: &configured}
		if !configured {
			msg := "repository " + repo.Name + " has no execution settings yet"
			out.Problem = &msg
		}
	}
	w, err := s.Workspaces.ForTask(ctx, req.TaskId)
	if err != nil {
		return nil, err
	}
	if w != nil {
		var name *string
		if r, err := s.Repositories.Get(ctx, w.RepositoryID); err == nil {
			name = &r.Name
		}
		v := toWorkspace(*w, name)
		out.Workspace = &v
	}
	return apigen.GetTaskWorkspace200JSONResponse(out), nil
}

func (s *Server) PrepareTaskWorkspace(ctx context.Context, req apigen.PrepareTaskWorkspaceRequestObject) (apigen.PrepareTaskWorkspaceResponseObject, error) {
	p, err := requireSession(ctx, "preparing a workspace")
	if err != nil {
		return nil, err
	}
	uid := p.User.ID
	w, err := s.Workspaces.StartAsync(ctx, req.TaskId, &uid)
	if err != nil {
		return nil, err
	}
	return apigen.PrepareTaskWorkspace202JSONResponse(toWorkspace(w, nil)), nil
}

func (s *Server) RemoveTaskWorkspace(ctx context.Context, req apigen.RemoveTaskWorkspaceRequestObject) (apigen.RemoveTaskWorkspaceResponseObject, error) {
	if _, err := requireSession(ctx, "removing a workspace"); err != nil {
		return nil, err
	}
	if err := s.Workspaces.Remove(ctx, req.TaskId); err != nil {
		return nil, err
	}
	return apigen.RemoveTaskWorkspace204Response{}, nil
}

// setTaskRepository handles UpdateTaskRequest.repository ("auto" or an id).
func (s *Server) setTaskRepository(ctx context.Context, taskID uuid.UUID, v string) error {
	if v == "auto" || v == "" {
		return s.Workspaces.SetTarget(ctx, taskID, nil)
	}
	id, err := uuid.Parse(v)
	if err != nil {
		return apperr.Validation(apperr.FieldError{Field: "repository", Message: "a repository id or \"auto\""})
	}
	return s.Workspaces.SetTarget(ctx, taskID, &id)
}
