package httpapi

import (
	"context"
	"net/http"
	"time"

	"wise/apps/api/internal/agents"
	"wise/apps/api/internal/apperr"
	"wise/apps/api/internal/auth"
	"wise/apps/api/internal/db"
	"wise/apps/api/internal/db/dbgen"
	"wise/apps/api/internal/events"
	"wise/apps/api/internal/httpapi/apigen"
	"wise/apps/api/internal/modelgateway"
	"wise/apps/api/internal/projects"
	"wise/apps/api/internal/repositories"
	"wise/apps/api/internal/runqueue"
	"wise/apps/api/internal/tasks"
)

// Server implements apigen.StrictServerInterface by delegating to domain services.
type Server struct {
	DB           *db.DB
	Auth         *auth.Service
	Projects     *projects.Service
	Repositories *repositories.Service
	Agents       *agents.Service
	RunQueue     *runqueue.Service
	Gateway      *modelgateway.Gateway
	Hub          *events.Hub
	Tasks        *tasks.Service
	Cookie       CookieConfig
	// RuntimeToken authenticates agent runtimes claiming runs (empty disables the runtime API).
	RuntimeToken string
	// WSOriginPatterns are extra Origin host patterns accepted for WebSockets.
	WSOriginPatterns []string
}

type CookieConfig struct {
	Secure bool
	TTL    time.Duration
}

var _ apigen.StrictServerInterface = (*Server)(nil)

func principal(ctx context.Context) (auth.Principal, error) {
	p, ok := auth.PrincipalFrom(ctx)
	if !ok {
		return auth.Principal{}, apperr.Unauthorized("authentication required")
	}
	return p, nil
}

// ---- system ----

func (s *Server) GetHealth(ctx context.Context, _ apigen.GetHealthRequestObject) (apigen.GetHealthResponseObject, error) {
	return apigen.GetHealth200JSONResponse{Status: "ok"}, nil
}

func (s *Server) GetReadiness(ctx context.Context, _ apigen.GetReadinessRequestObject) (apigen.GetReadinessResponseObject, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := s.DB.Pool.Ping(ctx); err != nil {
		return nil, apperr.Unavailable("database unreachable").WithCause(err)
	}
	return apigen.GetReadiness200JSONResponse{Status: "ok"}, nil
}

// ---- auth ----

// loginResponse sets the session cookie alongside the JSON body.
type loginResponse struct {
	body   apigen.SessionInfo
	cookie *http.Cookie
}

func (r loginResponse) VisitLoginResponse(w http.ResponseWriter) error {
	http.SetCookie(w, r.cookie)
	w.Header().Set("Cache-Control", "no-store")
	return apigen.Login200JSONResponse(r.body).VisitLoginResponse(w)
}

type logoutResponse struct{ cookie *http.Cookie }

func (r logoutResponse) VisitLogoutResponse(w http.ResponseWriter) error {
	http.SetCookie(w, r.cookie)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) sessionCookie(value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name: auth.SessionCookieName, Value: value, Path: "/api",
		MaxAge: maxAge, HttpOnly: true, Secure: s.Cookie.Secure, SameSite: http.SameSiteStrictMode,
	}
}

// renewSessionCookie re-issues the session cookie when the server extended the
// session, so the browser's cookie lifetime tracks the database expiry.
func (s *Server) renewSessionCookie(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p, ok := auth.PrincipalFrom(r.Context()); ok && p.Method == auth.MethodSession && p.Renewed {
			if c, err := r.Cookie(auth.SessionCookieName); err == nil {
				http.SetCookie(w, s.sessionCookie(c.Value, int(time.Until(p.ExpiresAt).Seconds())))
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) Login(ctx context.Context, req apigen.LoginRequestObject) (apigen.LoginResponseObject, error) {
	client := clientInfoFrom(ctx)
	sess, err := s.Auth.Login(ctx, req.Body.Username, req.Body.Password, client)
	if err != nil {
		return nil, err
	}
	return loginResponse{
		body:   toSessionInfo(sess.Principal),
		cookie: s.sessionCookie(sess.Token, int(s.Cookie.TTL.Seconds())),
	}, nil
}

func (s *Server) Logout(ctx context.Context, _ apigen.LogoutRequestObject) (apigen.LogoutResponseObject, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.Auth.Logout(ctx, p); err != nil {
		return nil, err
	}
	return logoutResponse{cookie: s.sessionCookie("", -1)}, nil
}

func (s *Server) GetMe(ctx context.Context, _ apigen.GetMeRequestObject) (apigen.GetMeResponseObject, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	return apigen.GetMe200JSONResponse(toSessionInfo(p)), nil
}

func (s *Server) ListApiTokens(ctx context.Context, _ apigen.ListApiTokensRequestObject) (apigen.ListApiTokensResponseObject, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	toks, err := s.Auth.ListAPITokens(ctx, p)
	if err != nil {
		return nil, err
	}
	return apigen.ListApiTokens200JSONResponse{Items: mapSlice(toks, toAPIToken)}, nil
}

func (s *Server) CreateApiToken(ctx context.Context, req apigen.CreateApiTokenRequestObject) (apigen.CreateApiTokenResponseObject, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	// A bearer token must not mint a (possibly non-expiring) replacement that
	// would survive its own revocation.
	if p.Method != auth.MethodSession {
		return nil, apperr.Forbidden("api tokens can only be created from a signed-in session")
	}
	created, err := s.Auth.CreateAPIToken(ctx, p, req.Body.Name, req.Body.ExpiresInDays)
	if err != nil {
		return nil, err
	}
	return apigen.CreateApiToken201JSONResponse{Token: toAPIToken(created.Token), Secret: created.Secret}, nil
}

func (s *Server) RevokeApiToken(ctx context.Context, req apigen.RevokeApiTokenRequestObject) (apigen.RevokeApiTokenResponseObject, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.Auth.RevokeAPIToken(ctx, p, req.TokenId); err != nil {
		return nil, err
	}
	return apigen.RevokeApiToken204Response{}, nil
}

// ---- projects ----

func (s *Server) ListProjects(ctx context.Context, req apigen.ListProjectsRequestObject) (apigen.ListProjectsResponseObject, error) {
	ps, err := s.Projects.List(ctx, deref(req.Params.IncludeArchived))
	if err != nil {
		return nil, err
	}
	return apigen.ListProjects200JSONResponse{Items: mapSlice(ps, toProject)}, nil
}

func (s *Server) CreateProject(ctx context.Context, req apigen.CreateProjectRequestObject) (apigen.CreateProjectResponseObject, error) {
	b := req.Body
	p, err := s.Projects.Create(ctx, projects.CreateInput{
		Name: b.Name, Slug: deref(b.Slug), Description: deref(b.Description), Metadata: derefMap(b.Metadata),
	})
	if err != nil {
		return nil, err
	}
	return apigen.CreateProject201JSONResponse(toProject(p)), nil
}

func (s *Server) GetProject(ctx context.Context, req apigen.GetProjectRequestObject) (apigen.GetProjectResponseObject, error) {
	p, err := s.Projects.Get(ctx, req.ProjectId)
	if err != nil {
		return nil, err
	}
	return apigen.GetProject200JSONResponse(toProject(p)), nil
}

func (s *Server) UpdateProject(ctx context.Context, req apigen.UpdateProjectRequestObject) (apigen.UpdateProjectResponseObject, error) {
	b := req.Body
	p, err := s.Projects.Update(ctx, req.ProjectId, projects.UpdateInput{
		Name: b.Name, Description: b.Description, Metadata: derefMap(b.Metadata),
	})
	if err != nil {
		return nil, err
	}
	return apigen.UpdateProject200JSONResponse(toProject(p)), nil
}

func (s *Server) ArchiveProject(ctx context.Context, req apigen.ArchiveProjectRequestObject) (apigen.ArchiveProjectResponseObject, error) {
	if err := s.Projects.Archive(ctx, req.ProjectId); err != nil {
		return nil, err
	}
	return apigen.ArchiveProject204Response{}, nil
}

func (s *Server) ListProjectResources(ctx context.Context, req apigen.ListProjectResourcesRequestObject) (apigen.ListProjectResourcesResponseObject, error) {
	rs, err := s.Projects.ListResources(ctx, req.ProjectId)
	if err != nil {
		return nil, err
	}
	return apigen.ListProjectResources200JSONResponse{Items: mapSlice(rs, toResource)}, nil
}

func (s *Server) CreateProjectResource(ctx context.Context, req apigen.CreateProjectResourceRequestObject) (apigen.CreateProjectResourceResponseObject, error) {
	b := req.Body
	r, err := s.Projects.CreateResource(ctx, req.ProjectId, projects.CreateResourceInput{
		Kind: dbgen.ProjectResourceKind(b.Kind), Name: b.Name, URI: deref(b.Uri), Metadata: derefMap(b.Metadata),
	})
	if err != nil {
		return nil, err
	}
	return apigen.CreateProjectResource201JSONResponse(toResource(r)), nil
}

func (s *Server) DeleteProjectResource(ctx context.Context, req apigen.DeleteProjectResourceRequestObject) (apigen.DeleteProjectResourceResponseObject, error) {
	if err := s.Projects.DeleteResource(ctx, req.ResourceId); err != nil {
		return nil, err
	}
	return apigen.DeleteProjectResource204Response{}, nil
}

// ---- repositories ----

func (s *Server) ListRepositories(ctx context.Context, req apigen.ListRepositoriesRequestObject) (apigen.ListRepositoriesResponseObject, error) {
	rs, err := s.Repositories.List(ctx, req.ProjectId)
	if err != nil {
		return nil, err
	}
	return apigen.ListRepositories200JSONResponse{Items: mapSlice(rs, toRepository)}, nil
}

func (s *Server) CreateRepository(ctx context.Context, req apigen.CreateRepositoryRequestObject) (apigen.CreateRepositoryResponseObject, error) {
	b := req.Body
	r, err := s.Repositories.Create(ctx, req.ProjectId, repositories.CreateInput{
		Name: b.Name, Role: dbgen.RepositoryRole(deref(b.Role)), SourceType: dbgen.RepositorySourceType(b.SourceType),
		LocalPath: deref(b.LocalPath), RemoteURL: deref(b.RemoteUrl), DefaultBranch: deref(b.DefaultBranch),
		Settings: derefMap(b.Settings),
	})
	if err != nil {
		return nil, err
	}
	return apigen.CreateRepository201JSONResponse(toRepository(r)), nil
}

func (s *Server) GetRepository(ctx context.Context, req apigen.GetRepositoryRequestObject) (apigen.GetRepositoryResponseObject, error) {
	r, err := s.Repositories.Get(ctx, req.RepositoryId)
	if err != nil {
		return nil, err
	}
	return apigen.GetRepository200JSONResponse(toRepository(r)), nil
}

func (s *Server) UpdateRepository(ctx context.Context, req apigen.UpdateRepositoryRequestObject) (apigen.UpdateRepositoryResponseObject, error) {
	b := req.Body
	in := repositories.UpdateInput{
		Name: b.Name, RemoteURL: b.RemoteUrl, DefaultBranch: b.DefaultBranch, Settings: derefMap(b.Settings),
	}
	if b.Role != nil {
		role := dbgen.RepositoryRole(*b.Role)
		in.Role = &role
	}
	r, err := s.Repositories.Update(ctx, req.RepositoryId, in)
	if err != nil {
		return nil, err
	}
	return apigen.UpdateRepository200JSONResponse(toRepository(r)), nil
}

func (s *Server) DeleteRepository(ctx context.Context, req apigen.DeleteRepositoryRequestObject) (apigen.DeleteRepositoryResponseObject, error) {
	if err := s.Repositories.Delete(ctx, req.RepositoryId); err != nil {
		return nil, err
	}
	return apigen.DeleteRepository204Response{}, nil
}

func (s *Server) InspectRepository(ctx context.Context, req apigen.InspectRepositoryRequestObject) (apigen.InspectRepositoryResponseObject, error) {
	r, err := s.Repositories.Inspect(ctx, req.RepositoryId)
	if err != nil {
		return nil, err
	}
	return apigen.InspectRepository200JSONResponse(toRepository(r)), nil
}

func (s *Server) RequestRepositoryIndex(ctx context.Context, req apigen.RequestRepositoryIndexRequestObject) (apigen.RequestRepositoryIndexResponseObject, error) {
	r, err := s.Repositories.RequestIndex(ctx, req.RepositoryId)
	if err != nil {
		return nil, err
	}
	return apigen.RequestRepositoryIndex202JSONResponse(toRepositoryStatus(r)), nil
}

func (s *Server) GetRepositoryStatus(ctx context.Context, req apigen.GetRepositoryStatusRequestObject) (apigen.GetRepositoryStatusResponseObject, error) {
	r, err := s.Repositories.Get(ctx, req.RepositoryId)
	if err != nil {
		return nil, err
	}
	return apigen.GetRepositoryStatus200JSONResponse(toRepositoryStatus(r)), nil
}

func (s *Server) ListRepositoryRelationships(ctx context.Context, req apigen.ListRepositoryRelationshipsRequestObject) (apigen.ListRepositoryRelationshipsResponseObject, error) {
	rs, err := s.Repositories.ListRelationships(ctx, req.ProjectId)
	if err != nil {
		return nil, err
	}
	return apigen.ListRepositoryRelationships200JSONResponse{Items: mapSlice(rs, toRelationship)}, nil
}

func (s *Server) CreateRepositoryRelationship(ctx context.Context, req apigen.CreateRepositoryRelationshipRequestObject) (apigen.CreateRepositoryRelationshipResponseObject, error) {
	b := req.Body
	rel, err := s.Repositories.CreateRelationship(ctx, req.ProjectId, repositories.CreateRelationshipInput{
		From: b.FromRepositoryId, To: b.ToRepositoryId,
		Kind: dbgen.RepositoryRelationshipKind(b.Kind), Note: deref(b.Note),
	})
	if err != nil {
		return nil, err
	}
	return apigen.CreateRepositoryRelationship201JSONResponse(toRelationship(rel)), nil
}

func (s *Server) DeleteRepositoryRelationship(ctx context.Context, req apigen.DeleteRepositoryRelationshipRequestObject) (apigen.DeleteRepositoryRelationshipResponseObject, error) {
	if err := s.Repositories.DeleteRelationship(ctx, req.RelationshipId); err != nil {
		return nil, err
	}
	return apigen.DeleteRepositoryRelationship204Response{}, nil
}
