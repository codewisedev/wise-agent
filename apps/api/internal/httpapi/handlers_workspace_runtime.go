package httpapi

import (
	"context"
	"encoding/json"

	"wise/apps/api/internal/apperr"
	"wise/apps/api/internal/db/dbgen"
	"wise/apps/api/internal/httpapi/apigen"
)

// runWorkspace returns the run's task workspace and the side effects its
// agent's tools allow.
func (s *Server) runWorkspace(ctx context.Context) (dbgen.Workspace, map[string]bool, error) {
	run, err := runFrom(ctx)
	if err != nil {
		return dbgen.Workspace{}, nil, err
	}
	w, err := s.Workspaces.ForRun(ctx, run)
	if err != nil {
		return dbgen.Workspace{}, nil, err
	}
	if w == nil {
		return dbgen.Workspace{}, nil, apperr.Conflict("this run has no task workspace")
	}
	tools, err := s.RunQueue.RunTools(ctx, run)
	if err != nil {
		return dbgen.Workspace{}, nil, err
	}
	effects := map[string]bool{}
	for _, e := range tools {
		effects[e] = true
	}
	return *w, effects, nil
}

func (s *Server) RuntimeWorkspaceFs(ctx context.Context, req apigen.RuntimeWorkspaceFsRequestObject) (apigen.RuntimeWorkspaceFsResponseObject, error) {
	w, effects, err := s.runWorkspace(ctx)
	if err != nil {
		return nil, err
	}
	body := map[string]any{}
	if req.Body != nil {
		body = *req.Body
	}
	raw, err := s.Workspaces.FS(ctx, w, body, effects["write"])
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return apigen.RuntimeWorkspaceFs200JSONResponse(out), nil
}

func (s *Server) RuntimeWorkspaceCommand(ctx context.Context, req apigen.RuntimeWorkspaceCommandRequestObject) (apigen.RuntimeWorkspaceCommandResponseObject, error) {
	w, effects, err := s.runWorkspace(ctx)
	if err != nil {
		return nil, err
	}
	if !effects["execute"] {
		return nil, apperr.Forbidden("this agent may not run commands")
	}
	timeout := 0
	if req.Body.TimeoutS != nil {
		timeout = *req.Body.TimeoutS
	}
	r, err := s.Workspaces.Command(ctx, w, req.Body.Command, timeout)
	if err != nil {
		return nil, err
	}
	trunc, timedOut := r.Truncated, r.TimedOut
	return apigen.RuntimeWorkspaceCommand200JSONResponse{ExitCode: r.ExitCode, Status: r.Status, Output: r.Output,
		Truncated: &trunc, TimedOut: &timedOut, DurationMs: r.DurationMs}, nil
}

func (s *Server) RuntimeWorkspaceDiff(ctx context.Context, _ apigen.RuntimeWorkspaceDiffRequestObject) (apigen.RuntimeWorkspaceDiffResponseObject, error) {
	w, _, err := s.runWorkspace(ctx)
	if err != nil {
		return nil, err
	}
	d, err := s.Workspaces.Diff(ctx, w)
	if err != nil {
		return nil, err
	}
	trunc := d.Truncated
	return apigen.RuntimeWorkspaceDiff200JSONResponse{Stat: d.Stat, Patch: d.Patch, Truncated: &trunc}, nil
}
