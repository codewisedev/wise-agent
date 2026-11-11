package httpapi

import (
	"context"

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
