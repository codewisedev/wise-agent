package httpapi

import (
	"context"
	"encoding/json"
	"errors"

	"wise/apps/api/internal/apperr"
	"wise/apps/api/internal/audit"
	"wise/apps/api/internal/db/dbgen"
	"wise/apps/api/internal/executor"
	"wise/apps/api/internal/tasks"

	"wise/apps/api/internal/httpapi/apigen"
	"wise/apps/api/internal/release"
)

func toRepoRelease(r release.Repo) apigen.RepositoryRelease {
	return reshape[apigen.RepositoryRelease](r)
}

func (s *Server) GetRepositoryRelease(ctx context.Context, req apigen.GetRepositoryReleaseRequestObject) (apigen.GetRepositoryReleaseResponseObject, error) {
	r, err := s.Release.Repo(ctx, req.RepositoryId)
	if err != nil {
		return nil, err
	}
	return apigen.GetRepositoryRelease200JSONResponse(toRepoRelease(r)), nil
}

func (s *Server) SetRepositoryRelease(ctx context.Context, req apigen.SetRepositoryReleaseRequestObject) (apigen.SetRepositoryReleaseResponseObject, error) {
	in := release.Repo{Remote: deref(req.Body.Remote), AccountID: req.Body.AccountId, BaseBranch: deref(req.Body.BaseBranch)}
	r, err := s.Release.SetRepo(ctx, req.RepositoryId, in, sessionUserID(ctx))
	if err != nil {
		return nil, err
	}
	return apigen.SetRepositoryRelease200JSONResponse(toRepoRelease(r)), nil
}

func (s *Server) DetectRepositoryRelease(ctx context.Context, req apigen.DetectRepositoryReleaseRequestObject) (apigen.DetectRepositoryReleaseResponseObject, error) {
	sug, err := s.Release.Detect(ctx, req.RepositoryId)
	if err != nil {
		return nil, err
	}
	return reshape[apigen.DetectRepositoryRelease200JSONResponse](sug), nil
}

func (s *Server) TestRepositoryRelease(ctx context.Context, req apigen.TestRepositoryReleaseRequestObject) (apigen.TestRepositoryReleaseResponseObject, error) {
	def, err := s.Release.Test(ctx, req.RepositoryId)
	if err != nil {
		return nil, err
	}
	return apigen.TestRepositoryRelease200JSONResponse{DefaultBranch: def}, nil
}

func (s *Server) GetProjectReleasePolicy(ctx context.Context, req apigen.GetProjectReleasePolicyRequestObject) (apigen.GetProjectReleasePolicyResponseObject, error) {
	p, err := s.Release.Policy(ctx, req.ProjectId)
	if err != nil {
		return nil, err
	}
	return reshape[apigen.GetProjectReleasePolicy200JSONResponse](p), nil
}

func (s *Server) SetProjectReleasePolicy(ctx context.Context, req apigen.SetProjectReleasePolicyRequestObject) (apigen.SetProjectReleasePolicyResponseObject, error) {
	p, err := s.Release.SetPolicy(ctx, req.ProjectId, release.Policy{PullRequests: string(req.Body.PullRequests), Draft: req.Body.Draft})
	if err != nil {
		return nil, err
	}
	return reshape[apigen.SetProjectReleasePolicy200JSONResponse](p), nil
}

func (s *Server) RewordTaskCommit(ctx context.Context, req apigen.RewordTaskCommitRequestObject) (apigen.RewordTaskCommitResponseObject, error) {
	if _, err := sessionUser(ctx, "editing the commit message"); err != nil {
		return nil, err
	}
	if s.Workspaces == nil {
		return nil, apperr.Unavailable("workspaces are not configured")
	}
	pending := "pending"
	aps, err := s.Tasks.ListApprovals(ctx, &pending, &req.TaskId)
	if err != nil {
		return nil, err
	}
	var gate *dbgen.Approval
	for _, a := range aps {
		var pinned []struct {
			Kind string `json:"kind"`
		}
		_ = json.Unmarshal(a.Approval.Artifacts, &pinned)
		for _, p := range pinned {
			if p.Kind == tasks.KindChangeset {
				ap := a.Approval
				gate = &ap
			}
		}
	}
	if gate == nil {
		return nil, apperr.Conflict("the commit message can be changed only while your final approval of the code is pending")
	}
	arts, err := s.Tasks.LatestArtifacts(ctx, req.TaskId, tasks.KindChangeset)
	if err != nil {
		return nil, err
	}
	art, ok := arts[tasks.KindChangeset]
	if !ok {
		return nil, apperr.Conflict("no verified commit yet")
	}
	var cs executor.Changeset
	if err := json.Unmarshal(art.Content, &cs); err != nil || cs.HeadSHA == "" {
		return nil, apperr.Conflict("there is no commit to edit")
	}
	w, err := s.Workspaces.ForTask(ctx, req.TaskId)
	if err != nil {
		return nil, err
	}
	if w == nil {
		return nil, apperr.Conflict("the task workspace is gone")
	}
	next, err := s.Workspaces.Reword(ctx, *w, cs.HeadSHA, req.Body.Message)
	if err != nil {
		var ee *executor.Error
		if errors.As(err, &ee) && ee.Status < 500 {
			return nil, apperr.Validation(apperr.FieldError{Field: "message", Message: ee.Msg})
		}
		return nil, err
	}
	// Keep the gate's findings; only the commit changes. A new version supersedes the pending approval.
	next.SecretFound = cs.SecretFound
	raw, _ := json.Marshal(next)
	if _, err := s.Tasks.SaveArtifact(ctx, gate.WorkflowRunID, req.TaskId, tasks.KindChangeset, raw, nil); err != nil {
		return nil, err
	}
	if err := audit.Record(ctx, s.DB.Queries, audit.Entry{Action: "task.commit_reworded", EntityType: "task", EntityID: &req.TaskId,
		Payload: map[string]any{"from": cs.HeadSHA, "to": next.HeadSHA}}); err != nil {
		return nil, err
	}
	return apigen.RewordTaskCommit200JSONResponse{HeadSha: next.HeadSHA, Message: next.Message}, nil
}

func (s *Server) GetTaskRelease(ctx context.Context, req apigen.GetTaskReleaseRequestObject) (apigen.GetTaskReleaseResponseObject, error) {
	if _, err := s.Tasks.GetTask(ctx, req.TaskId); err != nil {
		return nil, err
	}
	type item = map[string]any
	out := item{"pushes": []item{}, "pullRequests": []item{}, "approvals": []item{}}
	if arts, err := s.Tasks.LatestArtifacts(ctx, req.TaskId, release.KindRequest); err == nil {
		if a, ok := arts[release.KindRequest]; ok {
			out["request"] = json.RawMessage(a.Content)
		}
	}
	pushes, err := s.DB.ListGitPushesForTask(ctx, req.TaskId)
	if err != nil {
		return nil, err
	}
	for _, p := range pushes {
		out["pushes"] = append(out["pushes"].([]item), item{"id": p.ID, "status": p.Status, "remote": p.Remote, "branch": p.Branch, "sha": p.Sha,
			"baseBranch": p.BaseBranch, "attempts": p.Attempts, "existed": p.Existed, "error": p.Error, "createdAt": p.CreatedAt, "pushedAt": p.PushedAt})
	}
	prs, err := s.DB.ListPullRequestsForTask(ctx, req.TaskId)
	if err != nil {
		return nil, err
	}
	for _, p := range prs {
		out["pullRequests"] = append(out["pullRequests"].([]item), item{"id": p.ID, "status": p.Status, "number": p.Number, "url": p.Url,
			"state": p.State, "draft": p.Draft, "title": p.Title, "error": p.Error, "createdAt": p.CreatedAt})
	}
	pending := "pending"
	aps, err := s.Tasks.ListApprovals(ctx, &pending, &req.TaskId)
	if err != nil {
		return nil, err
	}
	for _, a := range aps {
		var pinned []struct {
			Kind string `json:"kind"`
		}
		_ = json.Unmarshal(a.Approval.Artifacts, &pinned)
		for _, p := range pinned {
			kind := map[string]string{release.KindRequest: "push", release.KindPush: "pull_request"}[p.Kind]
			if kind != "" {
				out["approvals"] = append(out["approvals"].([]item), item{"id": a.Approval.ID, "gate": a.Approval.Gate, "kind": kind,
					"combinedHash": a.Approval.CombinedHash})
			}
		}
	}
	return reshape[apigen.GetTaskRelease200JSONResponse](out), nil
}
