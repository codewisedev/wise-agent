package httpapi

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"

	"wise/apps/api/internal/db/dbgen"
	"wise/apps/api/internal/httpapi/apigen"
	"wise/apps/api/internal/memory"
)

func toMemory(m dbgen.Memory, projectName *string) apigen.Memory {
	return apigen.Memory{
		Id: m.ID, Scope: apigen.MemoryScope(m.Scope), ProjectId: m.ProjectID, ProjectName: projectName,
		Kind: apigen.MemoryKind(m.Kind), Category: apigen.MemoryCategory(m.Category), Title: m.Title, Body: m.Body,
		Status: apigen.MemoryStatus(m.Status), Source: apigen.MemorySource(m.Source), SourceRefs: jsonObject(m.SourceRefs),
		Rationale: m.Rationale, DuplicateOf: m.DuplicateOf, Version: int(m.Version), DecidedAt: m.DecidedAt,
		CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt,
	}
}

func listRowToMemory(r dbgen.ListMemoriesRow) apigen.Memory {
	return toMemory(dbgen.Memory{
		ID: r.ID, Scope: r.Scope, ProjectID: r.ProjectID, Kind: r.Kind, Category: r.Category, Title: r.Title, Body: r.Body,
		Status: r.Status, Source: r.Source, SourceRefs: r.SourceRefs, Rationale: r.Rationale, DuplicateOf: r.DuplicateOf,
		Version: r.Version, DecidedAt: r.DecidedAt, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}, r.ProjectName)
}

func memoryInput(b *apigen.MemoryInput) memory.Input {
	in := memory.Input{Scope: string(b.Scope), ProjectID: b.ProjectId, Kind: string(b.Kind), Title: b.Title, Body: b.Body}
	if b.Category != nil {
		in.Category = string(*b.Category)
	}
	return in
}

func strOf[T ~string](p *T) string {
	if p == nil {
		return ""
	}
	return string(*p)
}

func (s *Server) ListMemories(ctx context.Context, req apigen.ListMemoriesRequestObject) (apigen.ListMemoriesResponseObject, error) {
	p := req.Params
	f := memory.Filter{Status: strOf(p.Status), Scope: strOf(p.Scope), Kind: strOf(p.Kind), Category: strOf(p.Category),
		Q: deref(p.Q), ProjectID: p.ProjectId}
	if p.Limit != nil {
		f.Limit = *p.Limit
	}
	rows, err := s.Memory.List(ctx, f)
	if err != nil {
		return nil, err
	}
	return apigen.ListMemories200JSONResponse{Items: mapSlice(rows, listRowToMemory)}, nil
}

// sessionUser requires a signed-in user (API tokens cannot change memory).
func sessionUser(ctx context.Context, what string) (*uuid.UUID, error) {
	p, err := requireSession(ctx, what)
	if err != nil {
		return nil, err
	}
	id := p.User.ID
	return &id, nil
}

func (s *Server) memoryView(ctx context.Context, m dbgen.Memory) apigen.Memory {
	var name *string
	if m.ProjectID != nil {
		if p, err := s.Projects.Get(ctx, *m.ProjectID); err == nil {
			name = &p.Name
		}
	}
	return toMemory(m, name)
}

func (s *Server) CreateMemory(ctx context.Context, req apigen.CreateMemoryRequestObject) (apigen.CreateMemoryResponseObject, error) {
	uid, err := sessionUser(ctx, "adding a memory")
	if err != nil {
		return nil, err
	}
	m, err := s.Memory.Create(ctx, memoryInput(req.Body), uid)
	if err != nil {
		return nil, err
	}
	return apigen.CreateMemory201JSONResponse(s.memoryView(ctx, m)), nil
}

func (s *Server) GetMemoryInbox(ctx context.Context, _ apigen.GetMemoryInboxRequestObject) (apigen.GetMemoryInboxResponseObject, error) {
	n, err := s.Memory.InboxCount(ctx)
	if err != nil {
		return nil, err
	}
	return apigen.GetMemoryInbox200JSONResponse{Candidates: n}, nil
}

func (s *Server) GetMemory(ctx context.Context, req apigen.GetMemoryRequestObject) (apigen.GetMemoryResponseObject, error) {
	m, err := s.Memory.Get(ctx, req.MemoryId)
	if err != nil {
		return nil, err
	}
	return apigen.GetMemory200JSONResponse(s.memoryView(ctx, m)), nil
}

func (s *Server) UpdateMemory(ctx context.Context, req apigen.UpdateMemoryRequestObject) (apigen.UpdateMemoryResponseObject, error) {
	uid, err := sessionUser(ctx, "editing a memory")
	if err != nil {
		return nil, err
	}
	m, err := s.Memory.Update(ctx, req.MemoryId, memoryInput(req.Body), uid)
	if err != nil {
		return nil, err
	}
	return apigen.UpdateMemory200JSONResponse(s.memoryView(ctx, m)), nil
}

func (s *Server) ApproveMemory(ctx context.Context, req apigen.ApproveMemoryRequestObject) (apigen.ApproveMemoryResponseObject, error) {
	uid, err := sessionUser(ctx, "approving a memory")
	if err != nil {
		return nil, err
	}
	var in *memory.Input
	if req.Body != nil && req.Body.Title != "" {
		v := memoryInput(req.Body)
		in = &v
	}
	m, err := s.Memory.Approve(ctx, req.MemoryId, in, uid)
	if err != nil {
		return nil, err
	}
	return apigen.ApproveMemory200JSONResponse(s.memoryView(ctx, m)), nil
}

func (s *Server) RejectMemory(ctx context.Context, req apigen.RejectMemoryRequestObject) (apigen.RejectMemoryResponseObject, error) {
	uid, err := sessionUser(ctx, "rejecting a memory")
	if err != nil {
		return nil, err
	}
	m, err := s.Memory.Reject(ctx, req.MemoryId, uid)
	if err != nil {
		return nil, err
	}
	return apigen.RejectMemory200JSONResponse(s.memoryView(ctx, m)), nil
}

func (s *Server) ArchiveMemory(ctx context.Context, req apigen.ArchiveMemoryRequestObject) (apigen.ArchiveMemoryResponseObject, error) {
	uid, err := sessionUser(ctx, "archiving a memory")
	if err != nil {
		return nil, err
	}
	m, err := s.Memory.Archive(ctx, req.MemoryId, uid)
	if err != nil {
		return nil, err
	}
	return apigen.ArchiveMemory200JSONResponse(s.memoryView(ctx, m)), nil
}

func (s *Server) ReactivateMemory(ctx context.Context, req apigen.ReactivateMemoryRequestObject) (apigen.ReactivateMemoryResponseObject, error) {
	uid, err := sessionUser(ctx, "reactivating a memory")
	if err != nil {
		return nil, err
	}
	m, err := s.Memory.Reactivate(ctx, req.MemoryId, uid)
	if err != nil {
		return nil, err
	}
	return apigen.ReactivateMemory200JSONResponse(s.memoryView(ctx, m)), nil
}

func (s *Server) ListMemoryVersions(ctx context.Context, req apigen.ListMemoryVersionsRequestObject) (apigen.ListMemoryVersionsResponseObject, error) {
	rows, err := s.Memory.Versions(ctx, req.MemoryId)
	if err != nil {
		return nil, err
	}
	items := make([]apigen.MemoryVersion, len(rows))
	for i, v := range rows {
		items[i] = apigen.MemoryVersion{Version: int(v.Version), Scope: v.Scope, ProjectId: v.ProjectID, Kind: v.Kind, Category: v.Category,
			Title: v.Title, Body: v.Body, Status: v.Status, CreatedAt: v.CreatedAt}
	}
	return apigen.ListMemoryVersions200JSONResponse{Items: items}, nil
}

func (s *Server) RestoreMemoryVersion(ctx context.Context, req apigen.RestoreMemoryVersionRequestObject) (apigen.RestoreMemoryVersionResponseObject, error) {
	uid, err := sessionUser(ctx, "restoring a memory")
	if err != nil {
		return nil, err
	}
	m, err := s.Memory.Restore(ctx, req.MemoryId, int32(req.Version), uid)
	if err != nil {
		return nil, err
	}
	return apigen.RestoreMemoryVersion200JSONResponse(s.memoryView(ctx, m)), nil
}

func (s *Server) ExtractTaskMemories(ctx context.Context, req apigen.ExtractTaskMemoriesRequestObject) (apigen.ExtractTaskMemoriesResponseObject, error) {
	uid, err := sessionUser(ctx, "extracting memories")
	if err != nil {
		return nil, err
	}
	run, err := s.Memory.ExtractFromTask(ctx, req.TaskId, uid)
	if err != nil {
		return nil, err
	}
	return apigen.ExtractTaskMemories202JSONResponse(toAgentRun(run)), nil
}

func (s *Server) RuntimeMemorySearch(ctx context.Context, req apigen.RuntimeMemorySearchRequestObject) (apigen.RuntimeMemorySearchResponseObject, error) {
	run, err := runFrom(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.Memory.Search(ctx, run.ProjectID, req.Params.Q, 15)
	if err != nil {
		return nil, err
	}
	return apigen.RuntimeMemorySearch200JSONResponse{Items: mapSlice(rows, func(m dbgen.Memory) apigen.Memory { return toMemory(m, nil) })}, nil
}

// runContext decodes the context frozen on a run.
func runContext(raw []byte) *apigen.RunContext {
	if len(raw) == 0 {
		return nil
	}
	var c apigen.RunContext
	if json.Unmarshal(raw, &c) != nil {
		return nil
	}
	return &c
}

func (s *Server) InferProfile(ctx context.Context, req apigen.InferProfileRequestObject) (apigen.InferProfileResponseObject, error) {
	uid, err := sessionUser(ctx, "inferring the engineering profile")
	if err != nil {
		return nil, err
	}
	run, err := s.Memory.InferProfile(ctx, req.ProjectId, uid)
	if err != nil {
		return nil, err
	}
	return apigen.InferProfile202JSONResponse(toAgentRun(run)), nil
}

func (s *Server) CreateExample(ctx context.Context, req apigen.CreateExampleRequestObject) (apigen.CreateExampleResponseObject, error) {
	uid, err := sessionUser(ctx, "saving an approved example")
	if err != nil {
		return nil, err
	}
	b := req.Body
	in := memory.ExampleInput{RepositoryID: req.RepositoryId, Path: b.Path, StartLine: b.StartLine, EndLine: b.EndLine,
		Title: deref(b.Title), Note: deref(b.Note), Category: strOf(b.Category)}
	m, err := s.Memory.CreateExample(ctx, s.Repositories, in, uid)
	if err != nil {
		return nil, err
	}
	return apigen.CreateExample201JSONResponse(s.memoryView(ctx, m)), nil
}
