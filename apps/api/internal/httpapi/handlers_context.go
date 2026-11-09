package httpapi

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/google/uuid"

	"wise/apps/api/internal/apperr"
	"wise/apps/api/internal/codeindex"
	"wise/apps/api/internal/db/dbgen"
	"wise/apps/api/internal/httpapi/apigen"
)

type indexJobCtxKey struct{}

func indexJobFrom(ctx context.Context) (dbgen.IndexJob, error) {
	job, ok := ctx.Value(indexJobCtxKey{}).(dbgen.IndexJob)
	if !ok {
		return job, apperr.Unauthorized("index job token required")
	}
	return job, nil
}

// ---- indexer (runtime) ----

func (s *Server) RuntimeClaimIndexJob(ctx context.Context, req apigen.RuntimeClaimIndexJobRequestObject) (apigen.RuntimeClaimIndexJobResponseObject, error) {
	c, err := s.Index.Claim(ctx, req.Body.RuntimeId)
	if err != nil {
		return nil, err
	}
	if c == nil {
		return apigen.RuntimeClaimIndexJob204Response{}, nil
	}
	var settings map[string]any
	raw, _ := json.Marshal(c.Settings)
	_ = json.Unmarshal(raw, &settings)
	path := ""
	if c.Repo.LocalPath != nil {
		path = *c.Repo.LocalPath
	}
	out := apigen.RuntimeClaimIndexJob200JSONResponse{
		JobId: c.Job.ID, Token: c.Token, LeaseSeconds: s.Index.LeaseSeconds(), Settings: settings, Manifest: c.Manifest,
	}
	out.Repository.Id, out.Repository.Name, out.Repository.LocalPath = c.Repo.ID, c.Repo.Name, path
	return out, nil
}

func (s *Server) RuntimeIndexHeartbeat(ctx context.Context, _ apigen.RuntimeIndexHeartbeatRequestObject) (apigen.RuntimeIndexHeartbeatResponseObject, error) {
	job, err := indexJobFrom(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.Index.Heartbeat(ctx, job); err != nil {
		return nil, err
	}
	return apigen.RuntimeIndexHeartbeat204Response{}, nil
}

func (s *Server) RuntimeIndexUpload(ctx context.Context, req apigen.RuntimeIndexUploadRequestObject) (apigen.RuntimeIndexUploadResponseObject, error) {
	job, err := indexJobFrom(ctx)
	if err != nil {
		return nil, err
	}
	var files []codeindex.File
	var deleted []string
	if req.Body.Files != nil {
		files = reshape[[]codeindex.File](*req.Body.Files)
	}
	if req.Body.Deleted != nil {
		deleted = *req.Body.Deleted
	}
	if err := s.Index.Upload(ctx, job, files, deleted); err != nil {
		return nil, err
	}
	return apigen.RuntimeIndexUpload204Response{}, nil
}

func (s *Server) RuntimeIndexComplete(ctx context.Context, req apigen.RuntimeIndexCompleteRequestObject) (apigen.RuntimeIndexCompleteResponseObject, error) {
	job, err := indexJobFrom(ctx)
	if err != nil {
		return nil, err
	}
	var deps []codeindex.Dependency
	if req.Body.Dependencies != nil {
		deps = reshape[[]codeindex.Dependency](*req.Body.Dependencies)
	}
	var stats map[string]any
	if req.Body.Stats != nil {
		stats = *req.Body.Stats
	}
	if err := s.Index.Complete(ctx, job, deref(req.Body.CommitSha), deps, stats); err != nil {
		return nil, err
	}
	return apigen.RuntimeIndexComplete204Response{}, nil
}

func (s *Server) RuntimeIndexFail(ctx context.Context, req apigen.RuntimeIndexFailRequestObject) (apigen.RuntimeIndexFailResponseObject, error) {
	job, err := indexJobFrom(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.Index.Fail(ctx, job, req.Body.Error); err != nil {
		return nil, err
	}
	return apigen.RuntimeIndexFail204Response{}, nil
}

// ---- context tools (runtime, scoped to the run's project) ----

// runProject returns the run's project and resolves a repository name in it.
func (s *Server) runProject(ctx context.Context, repoName *string) (uuid.UUID, *uuid.UUID, error) {
	run, err := runFrom(ctx)
	if err != nil {
		return uuid.Nil, nil, err
	}
	if run.ProjectID == nil {
		return uuid.Nil, nil, apperr.Conflict("this run has no project attached; context tools need one")
	}
	if repoName == nil || strings.TrimSpace(*repoName) == "" {
		return *run.ProjectID, nil, nil
	}
	repos, err := s.Repositories.List(ctx, *run.ProjectID)
	if err != nil {
		return uuid.Nil, nil, err
	}
	for _, r := range repos {
		if strings.EqualFold(r.Name, strings.TrimSpace(*repoName)) {
			return *run.ProjectID, &r.ID, nil
		}
	}
	return uuid.Nil, nil, apperr.Validation(apperr.FieldError{Field: "repository", Message: "no repository with that name in this project"})
}

func toHits(h []codeindex.Hit) apigen.ContextHits {
	items := reshape[[]apigen.ContextHit](h)
	if items == nil {
		items = []apigen.ContextHit{}
	}
	out := apigen.ContextHits{Items: items}
	if len(h) == 0 {
		note := "No matches in the index. Try other words, find_symbol, or search_code for exact text."
		out.Note = &note
	} else {
		for _, x := range h {
			if x.Stale {
				note := "Some results come from an older commit than HEAD; read the files to confirm."
				out.Note = &note
				break
			}
		}
	}
	return out
}

func toDeps(rows []dbgen.ListProjectDependenciesRow) apigen.DependencyList {
	items := make([]apigen.Dependency, len(rows))
	for i, r := range rows {
		items[i] = apigen.Dependency{Ecosystem: r.Ecosystem, Name: r.Name, Version: r.Version, Dev: r.Dev, Manifest: r.Manifest, Repository: r.RepositoryName}
	}
	return apigen.DependencyList{Items: items}
}

func kindOf(k *apigen.ContextKind) string {
	if k == nil {
		return ""
	}
	return string(*k)
}

func (s *Server) RuntimeContextSearch(ctx context.Context, req apigen.RuntimeContextSearchRequestObject) (apigen.RuntimeContextSearchResponseObject, error) {
	pid, repo, err := s.runProject(ctx, req.Params.Repository)
	if err != nil {
		return nil, err
	}
	limit := 10
	if req.Params.Limit != nil {
		limit = *req.Params.Limit
	}
	hits, err := s.Index.Search(ctx, codeindex.SearchQuery{ProjectID: pid, Text: req.Params.Q, Kind: kindOf(req.Params.Kind), RepositoryID: repo, Limit: limit})
	if err != nil {
		return nil, err
	}
	return apigen.RuntimeContextSearch200JSONResponse(toHits(hits)), nil
}

func (s *Server) RuntimeContextSymbols(ctx context.Context, req apigen.RuntimeContextSymbolsRequestObject) (apigen.RuntimeContextSymbolsResponseObject, error) {
	pid, repo, err := s.runProject(ctx, req.Params.Repository)
	if err != nil {
		return nil, err
	}
	hits, err := s.Index.Symbols(ctx, pid, req.Params.Name, deref(req.Params.Kind), repo, 25)
	if err != nil {
		return nil, err
	}
	return apigen.RuntimeContextSymbols200JSONResponse(toHits(hits)), nil
}

func (s *Server) RuntimeContextDependencies(ctx context.Context, req apigen.RuntimeContextDependenciesRequestObject) (apigen.RuntimeContextDependenciesResponseObject, error) {
	pid, repo, err := s.runProject(ctx, req.Params.Repository)
	if err != nil {
		return nil, err
	}
	rows, err := s.Index.Dependencies(ctx, pid, repo)
	if err != nil {
		return nil, err
	}
	return apigen.RuntimeContextDependencies200JSONResponse(toDeps(rows)), nil
}

// ---- dashboard ----

func (s *Server) SearchContext(ctx context.Context, req apigen.SearchContextRequestObject) (apigen.SearchContextResponseObject, error) {
	limit := 30
	if req.Params.Limit != nil {
		limit = *req.Params.Limit
	}
	hits, err := s.Index.Search(ctx, codeindex.SearchQuery{ProjectID: req.ProjectId, Text: req.Params.Q, Kind: kindOf(req.Params.Kind),
		RepositoryID: req.Params.RepositoryId, Limit: limit})
	if err != nil {
		return nil, err
	}
	return apigen.SearchContext200JSONResponse(toHits(hits)), nil
}

func (s *Server) ListContextSymbols(ctx context.Context, req apigen.ListContextSymbolsRequestObject) (apigen.ListContextSymbolsResponseObject, error) {
	if _, err := s.Projects.Get(ctx, req.ProjectId); err != nil {
		return nil, err
	}
	hits, err := s.Index.Symbols(ctx, req.ProjectId, req.Params.Name, deref(req.Params.Kind), req.Params.RepositoryId, 100)
	if err != nil {
		return nil, err
	}
	return apigen.ListContextSymbols200JSONResponse(toHits(hits)), nil
}

func (s *Server) ListContextDependencies(ctx context.Context, req apigen.ListContextDependenciesRequestObject) (apigen.ListContextDependenciesResponseObject, error) {
	if _, err := s.Projects.Get(ctx, req.ProjectId); err != nil {
		return nil, err
	}
	rows, err := s.Index.Dependencies(ctx, req.ProjectId, req.Params.RepositoryId)
	if err != nil {
		return nil, err
	}
	return apigen.ListContextDependencies200JSONResponse(toDeps(rows)), nil
}

func (s *Server) GetRepositoryIndex(ctx context.Context, req apigen.GetRepositoryIndexRequestObject) (apigen.GetRepositoryIndexResponseObject, error) {
	r, err := s.Repositories.Get(ctx, req.RepositoryId)
	if err != nil {
		return nil, err
	}
	jobs, err := s.Index.Jobs(ctx, r.ID)
	if err != nil {
		return nil, err
	}
	out := apigen.GetRepositoryIndex200JSONResponse{
		Status: apigen.RepositoryIndexStatus(r.IndexStatus), IndexedSha: r.IndexedSha, HeadSha: r.HeadSha, IndexedAt: r.IndexedAt,
		Stale: r.IndexedSha != nil && r.HeadSha != nil && *r.IndexedSha != *r.HeadSha, Stats: jsonObject(r.IndexStats), Error: r.IndexError,
		Jobs: make([]apigen.IndexJob, len(jobs)),
	}
	for i, j := range jobs {
		out.Jobs[i] = apigen.IndexJob{Id: j.ID, Status: apigen.IndexJobStatus(j.Status), Reason: j.Reason, CommitSha: j.CommitSha,
			Stats: jsonObject(j.Stats), Error: j.Error, CreatedAt: j.CreatedAt, StartedAt: j.StartedAt, FinishedAt: j.FinishedAt}
	}
	return out, nil
}

func (s *Server) ReadRepositoryFile(ctx context.Context, req apigen.ReadRepositoryFileRequestObject) (apigen.ReadRepositoryFileResponseObject, error) {
	content, lines, truncated, repo, err := s.Repositories.ReadFile(ctx, req.RepositoryId, req.Params.Path)
	if err != nil {
		return nil, err
	}
	return apigen.ReadRepositoryFile200JSONResponse{Path: req.Params.Path, Content: string(content), Lines: lines, Truncated: truncated, HeadSha: repo.HeadSha}, nil
}
