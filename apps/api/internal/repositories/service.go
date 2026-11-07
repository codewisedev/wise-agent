// Package repositories manages repositories registered to projects and the
// relationships between them.
package repositories

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/google/uuid"

	"wise/apps/api/internal/apperr"
	"wise/apps/api/internal/audit"
	"wise/apps/api/internal/db"
	"wise/apps/api/internal/db/dbgen"
	"wise/apps/api/internal/gitinspect"
	"wise/apps/api/internal/projects"
)

type CreateInput struct {
	Name          string
	Role          dbgen.RepositoryRole
	SourceType    dbgen.RepositorySourceType
	LocalPath     string
	RemoteURL     string
	DefaultBranch string
	Settings      map[string]any
}

type UpdateInput struct {
	Name          *string
	Role          *dbgen.RepositoryRole
	RemoteURL     *string
	DefaultBranch *string
	Settings      map[string]any
}

type CreateRelationshipInput struct {
	From, To uuid.UUID
	Kind     dbgen.RepositoryRelationshipKind
	Note     string
}

// GitInspector is the subset of gitinspect.Inspector used here.
type GitInspector interface {
	ResolvePath(path string) (string, error)
	Inspect(ctx context.Context, path string) (gitinspect.Info, error)
}

type Service struct {
	db  *db.DB
	git GitInspector
}

func NewService(d *db.DB, git GitInspector) *Service { return &Service{db: d, git: git} }

var errRepoNotFound = apperr.NotFound("repository not found")

func (s *Service) Create(ctx context.Context, projectID uuid.UUID, in CreateInput) (dbgen.Repository, error) {
	in.Name = strings.TrimSpace(in.Name)
	var fields []apperr.FieldError
	if in.Name == "" {
		fields = append(fields, apperr.FieldError{Field: "name", Message: "must not be blank"})
	}
	if in.Role == "" {
		in.Role = dbgen.RepositoryRoleOther
	}
	if !in.Role.Valid() {
		fields = append(fields, apperr.FieldError{Field: "role", Message: "unknown role"})
	}
	params := dbgen.CreateRepositoryParams{ProjectID: projectID, Name: in.Name, Role: in.Role, SourceType: in.SourceType}
	switch in.SourceType {
	case dbgen.RepositorySourceTypeLocal:
		if in.RemoteURL != "" {
			fields = append(fields, apperr.FieldError{Field: "remoteUrl", Message: "not allowed for local repositories; it is read from git"})
		}
		resolved, err := s.git.ResolvePath(in.LocalPath)
		if err != nil {
			fields = append(fields, apperr.FieldError{Field: "localPath", Message: err.Error()})
		}
		params.LocalPath = &resolved
	case dbgen.RepositorySourceTypeRemote:
		if in.LocalPath != "" {
			fields = append(fields, apperr.FieldError{Field: "localPath", Message: "not allowed for remote repositories"})
		}
		if err := ValidateRemoteURL(in.RemoteURL); err != nil {
			fields = append(fields, apperr.FieldError{Field: "remoteUrl", Message: err.Error()})
		}
		params.RemoteUrl = &in.RemoteURL
	default:
		fields = append(fields, apperr.FieldError{Field: "sourceType", Message: "must be local or remote"})
	}
	if len(fields) > 0 {
		return dbgen.Repository{}, apperr.Validation(fields...)
	}
	if b := strings.TrimSpace(in.DefaultBranch); b != "" {
		params.DefaultBranch = &b
	}
	settings, err := projects.MarshalObject(in.Settings)
	if err != nil {
		return dbgen.Repository{}, err
	}
	params.Settings = settings
	if params.ID, err = uuid.NewV7(); err != nil {
		return dbgen.Repository{}, err
	}

	var repo dbgen.Repository
	err = s.db.InTx(ctx, func(q *dbgen.Queries) error {
		if err := projects.LockActive(ctx, q, projectID); err != nil {
			return err
		}
		repo, err = q.CreateRepository(ctx, params)
		if db.IsUniqueViolation(err) {
			return apperr.Conflict(fmt.Sprintf("project already has a repository named %q", in.Name))
		}
		if err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{
			Action: "repository.create", EntityType: "repository", EntityID: audit.Ptr(repo.ID),
			Payload: map[string]any{"projectId": projectID, "name": repo.Name, "sourceType": repo.SourceType, "localPath": repo.LocalPath, "remoteUrl": repo.RemoteUrl},
		})
	})
	if err != nil {
		return dbgen.Repository{}, err
	}
	if repo.SourceType == dbgen.RepositorySourceTypeLocal {
		return s.refreshGit(ctx, repo)
	}
	return repo, nil
}

func (s *Service) List(ctx context.Context, projectID uuid.UUID) ([]dbgen.Repository, error) {
	if _, err := s.db.GetProject(ctx, projectID); err != nil {
		if db.IsNotFound(err) {
			return nil, apperr.NotFound("project not found")
		}
		return nil, err
	}
	return s.db.ListRepositories(ctx, projectID)
}

func (s *Service) Get(ctx context.Context, id uuid.UUID) (dbgen.Repository, error) {
	repo, err := s.db.GetRepository(ctx, id)
	if db.IsNotFound(err) {
		return dbgen.Repository{}, errRepoNotFound
	}
	return repo, err
}

func (s *Service) Update(ctx context.Context, id uuid.UUID, in UpdateInput) (dbgen.Repository, error) {
	current, err := s.Get(ctx, id)
	if err != nil {
		return dbgen.Repository{}, err
	}
	var fields []apperr.FieldError
	if in.Name != nil {
		n := strings.TrimSpace(*in.Name)
		if n == "" {
			fields = append(fields, apperr.FieldError{Field: "name", Message: "must not be blank"})
		}
		in.Name = &n
	}
	if in.Role != nil && !in.Role.Valid() {
		fields = append(fields, apperr.FieldError{Field: "role", Message: "unknown role"})
	}
	if in.RemoteURL != nil {
		if current.SourceType != dbgen.RepositorySourceTypeRemote {
			fields = append(fields, apperr.FieldError{Field: "remoteUrl", Message: "only remote repositories have an editable remoteUrl"})
		} else if err := ValidateRemoteURL(*in.RemoteURL); err != nil {
			fields = append(fields, apperr.FieldError{Field: "remoteUrl", Message: err.Error()})
		}
	}
	if len(fields) > 0 {
		return dbgen.Repository{}, apperr.Validation(fields...)
	}
	var settings []byte
	if in.Settings != nil {
		if settings, err = projects.MarshalObject(in.Settings); err != nil {
			return dbgen.Repository{}, err
		}
	}
	var repo dbgen.Repository
	err = s.db.InTx(ctx, func(q *dbgen.Queries) error {
		repo, err = q.UpdateRepository(ctx, dbgen.UpdateRepositoryParams{
			ID: id, Name: in.Name, Role: in.Role, RemoteUrl: in.RemoteURL, DefaultBranch: in.DefaultBranch, Settings: settings,
		})
		if db.IsNotFound(err) {
			return errRepoNotFound
		}
		if db.IsUniqueViolation(err) {
			return apperr.Conflict("project already has a repository with that name")
		}
		if err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{
			Action: "repository.update", EntityType: "repository", EntityID: audit.Ptr(id),
			Payload: map[string]any{"name": in.Name, "role": in.Role, "remoteUrl": in.RemoteURL, "defaultBranch": in.DefaultBranch, "settingsChanged": settings != nil},
		})
	})
	return repo, err
}

func (s *Service) Delete(ctx context.Context, id uuid.UUID) error {
	return s.db.InTx(ctx, func(q *dbgen.Queries) error {
		repo, err := q.DeleteRepository(ctx, id)
		if db.IsNotFound(err) {
			return errRepoNotFound
		}
		if err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{
			Action: "repository.delete", EntityType: "repository", EntityID: audit.Ptr(id),
			Payload: map[string]any{"projectId": repo.ProjectID, "name": repo.Name},
		})
	})
}

// Inspect refreshes Git metadata of a local repository. Inspection failures
// (e.g. the directory was moved) are recorded on the repository, not returned.
func (s *Service) Inspect(ctx context.Context, id uuid.UUID) (dbgen.Repository, error) {
	repo, err := s.Get(ctx, id)
	if err != nil {
		return dbgen.Repository{}, err
	}
	if repo.SourceType != dbgen.RepositorySourceTypeLocal {
		return dbgen.Repository{}, apperr.Unprocessable("only local repositories can be inspected; remote repositories are cloned in a later phase")
	}
	return s.refreshGit(ctx, repo)
}

func (s *Service) refreshGit(ctx context.Context, repo dbgen.Repository) (dbgen.Repository, error) {
	info, ierr := s.git.Inspect(ctx, *repo.LocalPath)
	params := dbgen.UpdateRepositoryGitInfoParams{ID: repo.ID, GitRemotes: json.RawMessage("[]")}
	if ierr != nil {
		msg := ierr.Error()
		params.GitError = &msg
		// keep last known values so the UI still shows something useful
		params.CurrentBranch, params.HeadSha, params.GitRemotes = repo.CurrentBranch, repo.HeadSha, repo.GitRemotes
	} else {
		params.CurrentBranch = nonEmpty(info.CurrentBranch)
		params.HeadSha = nonEmpty(info.HeadSHA)
		params.DetectedDefaultBranch = nonEmpty(info.DefaultBranch)
		remotes := info.Remotes
		if remotes == nil {
			remotes = []gitinspect.Remote{}
		}
		b, err := json.Marshal(remotes)
		if err != nil {
			return dbgen.Repository{}, err
		}
		params.GitRemotes = b
	}
	var updated dbgen.Repository
	err := s.db.InTx(ctx, func(q *dbgen.Queries) error {
		var err error
		updated, err = q.UpdateRepositoryGitInfo(ctx, params)
		if db.IsNotFound(err) {
			return errRepoNotFound
		}
		if err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{
			Action: "repository.inspect", EntityType: "repository", EntityID: audit.Ptr(repo.ID),
			Payload: map[string]any{"headSha": params.HeadSha, "error": params.GitError},
		})
	})
	return updated, err
}

// RequestIndex marks the repository as queued for indexing (indexer: Phase 4).
func (s *Service) RequestIndex(ctx context.Context, id uuid.UUID) (dbgen.Repository, error) {
	var repo dbgen.Repository
	err := s.db.InTx(ctx, func(q *dbgen.Queries) error {
		var err error
		repo, err = q.QueueRepositoryIndex(ctx, id)
		if db.IsNotFound(err) {
			return errRepoNotFound
		}
		if err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{
			Action: "repository.index_requested", EntityType: "repository", EntityID: audit.Ptr(id),
			Payload: map[string]any{"headSha": repo.HeadSha},
		})
	})
	return repo, err
}

func (s *Service) ListRelationships(ctx context.Context, projectID uuid.UUID) ([]dbgen.RepositoryRelationship, error) {
	if _, err := s.db.GetProject(ctx, projectID); err != nil {
		if db.IsNotFound(err) {
			return nil, apperr.NotFound("project not found")
		}
		return nil, err
	}
	return s.db.ListRepositoryRelationships(ctx, projectID)
}

func (s *Service) CreateRelationship(ctx context.Context, projectID uuid.UUID, in CreateRelationshipInput) (dbgen.RepositoryRelationship, error) {
	if in.From == in.To {
		return dbgen.RepositoryRelationship{}, apperr.Validation(apperr.FieldError{Field: "toRepositoryId", Message: "a repository cannot relate to itself"})
	}
	if !in.Kind.Valid() {
		return dbgen.RepositoryRelationship{}, apperr.Validation(apperr.FieldError{Field: "kind", Message: "unknown relationship kind"})
	}
	id, err := uuid.NewV7()
	if err != nil {
		return dbgen.RepositoryRelationship{}, err
	}
	var rel dbgen.RepositoryRelationship
	err = s.db.InTx(ctx, func(q *dbgen.Queries) error {
		if err := projects.LockActive(ctx, q, projectID); err != nil {
			return err
		}
		for field, rid := range map[string]uuid.UUID{"fromRepositoryId": in.From, "toRepositoryId": in.To} {
			r, err := q.GetRepository(ctx, rid)
			if db.IsNotFound(err) || (err == nil && r.ProjectID != projectID) {
				return apperr.Validation(apperr.FieldError{Field: field, Message: "repository not found in this project"})
			}
			if err != nil {
				return err
			}
		}
		rel, err = q.CreateRepositoryRelationship(ctx, dbgen.CreateRepositoryRelationshipParams{
			ID: id, ProjectID: projectID, FromRepositoryID: in.From, ToRepositoryID: in.To, Kind: in.Kind, Note: in.Note,
		})
		if db.IsUniqueViolation(err) {
			return apperr.Conflict("this relationship already exists")
		}
		if err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{
			Action: "repository_relationship.create", EntityType: "repository_relationship", EntityID: audit.Ptr(id),
			Payload: map[string]any{"projectId": projectID, "from": in.From, "to": in.To, "kind": in.Kind},
		})
	})
	return rel, err
}

func (s *Service) DeleteRelationship(ctx context.Context, id uuid.UUID) error {
	return s.db.InTx(ctx, func(q *dbgen.Queries) error {
		rel, err := q.DeleteRepositoryRelationship(ctx, id)
		if db.IsNotFound(err) {
			return apperr.NotFound("relationship not found")
		}
		if err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{
			Action: "repository_relationship.delete", EntityType: "repository_relationship", EntityID: audit.Ptr(id),
			Payload: map[string]any{"projectId": rel.ProjectID, "kind": rel.Kind},
		})
	})
}

var scpLikeRe = regexp.MustCompile(`^[A-Za-z0-9._-]+@[A-Za-z0-9.-]+:[A-Za-z0-9._~/-]+$`)

// ValidateRemoteURL accepts https://, ssh:// and scp-like (git@host:path) URLs
// and rejects URLs carrying embedded credentials.
func ValidateRemoteURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return errors.New("is required")
	}
	if scpLikeRe.MatchString(raw) {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("is not a valid URL")
	}
	switch u.Scheme {
	case "https", "ssh":
	default:
		return errors.New("must use https://, ssh:// or git@host:path")
	}
	if u.Host == "" {
		return errors.New("must include a host")
	}
	if _, hasPass := u.User.Password(); hasPass || (u.Scheme == "https" && u.User != nil) {
		return errors.New("must not contain credentials")
	}
	return nil
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
