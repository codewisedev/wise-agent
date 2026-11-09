package httpapi

import (
	"encoding/json"

	"wise/apps/api/internal/auth"
	"wise/apps/api/internal/db/dbgen"
	"wise/apps/api/internal/httpapi/apigen"
	"wise/apps/api/internal/projects"
)

func toUser(u dbgen.User) apigen.User {
	return apigen.User{Id: u.ID, Username: u.Username, Role: apigen.UserRole(u.Role), CreatedAt: u.CreatedAt}
}

func toSessionInfo(p auth.Principal) apigen.SessionInfo {
	s := apigen.SessionInfo{User: toUser(p.User), AuthMethod: apigen.SessionInfoAuthMethod(p.Method)}
	if p.Method == auth.MethodSession {
		csrf, exp := p.CSRFToken, p.ExpiresAt
		s.CsrfToken, s.ExpiresAt = &csrf, &exp
	}
	return s
}

func toAPIToken(t dbgen.ApiToken) apigen.ApiToken {
	return apigen.ApiToken{
		Id: t.ID, Name: t.Name, Prefix: auth.APITokenPrefix + t.Prefix, Scopes: t.Scopes,
		CreatedAt: t.CreatedAt, LastUsedAt: t.LastUsedAt, ExpiresAt: t.ExpiresAt, RevokedAt: t.RevokedAt,
	}
}

func toProject(p projects.Project) apigen.Project {
	return apigen.Project{
		Id: p.ID, Slug: p.Slug, Name: p.Name, Description: p.Description,
		Metadata: jsonObject(p.Metadata), RepositoryCount: int(p.RepositoryCount),
		CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt, ArchivedAt: p.ArchivedAt,
	}
}

func toResource(r dbgen.ProjectResource) apigen.ProjectResource {
	return apigen.ProjectResource{
		Id: r.ID, ProjectId: r.ProjectID, Kind: apigen.ProjectResourceKind(r.Kind), Name: r.Name, Uri: r.Uri,
		Metadata: jsonObject(r.Metadata), CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

func toRepository(r dbgen.Repository) apigen.Repository {
	remotes := []apigen.GitRemote{}
	_ = json.Unmarshal(r.GitRemotes, &remotes)
	return apigen.Repository{
		Id: r.ID, ProjectId: r.ProjectID, Name: r.Name,
		Role: apigen.RepositoryRole(r.Role), SourceType: apigen.RepositorySourceType(r.SourceType),
		LocalPath: r.LocalPath, RemoteUrl: r.RemoteUrl,
		DefaultBranch: r.DefaultBranch, CurrentBranch: r.CurrentBranch, HeadSha: r.HeadSha,
		GitRemotes: remotes, GitInspectedAt: r.GitInspectedAt, GitError: r.GitError,
		IndexStatus: apigen.RepositoryIndexStatus(r.IndexStatus), IndexRequestedAt: r.IndexRequestedAt,
		IndexedSha: r.IndexedSha, IndexedAt: r.IndexedAt, IndexStats: ptrMap(jsonObject(r.IndexStats)), IndexError: r.IndexError,
		Settings: jsonObject(r.Settings), CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

func toRepositoryStatus(r dbgen.Repository) apigen.RepositoryStatus {
	return apigen.RepositoryStatus{
		RepositoryId: r.ID, IndexStatus: apigen.RepositoryIndexStatus(r.IndexStatus),
		IndexRequestedAt: r.IndexRequestedAt, HeadSha: r.HeadSha,
		GitInspectedAt: r.GitInspectedAt, GitError: r.GitError,
	}
}

func toRelationship(r dbgen.RepositoryRelationship) apigen.RepositoryRelationship {
	return apigen.RepositoryRelationship{
		Id: r.ID, ProjectId: r.ProjectID, FromRepositoryId: r.FromRepositoryID, ToRepositoryId: r.ToRepositoryID,
		Kind: apigen.RepositoryRelationshipKind(r.Kind), Note: r.Note, CreatedAt: r.CreatedAt,
	}
}

func jsonObject(b []byte) map[string]any {
	m := map[string]any{}
	_ = json.Unmarshal(b, &m)
	return m
}

func mapSlice[T, U any](in []T, f func(T) U) []U {
	out := make([]U, len(in))
	for i, v := range in {
		out[i] = f(v)
	}
	return out
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

func derefMap(p *map[string]any) map[string]any {
	if p == nil {
		return nil
	}
	if *p == nil {
		return map[string]any{}
	}
	return *p
}

func ptrMap(m map[string]any) *map[string]any { return &m }
