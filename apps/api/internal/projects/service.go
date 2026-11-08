// Package projects manages projects and the resources attached to them.
package projects

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/uuid"

	"wise/apps/api/internal/apperr"
	"wise/apps/api/internal/audit"
	"wise/apps/api/internal/db"
	"wise/apps/api/internal/db/dbgen"
)

// Project is a project with derived data.
type Project struct {
	dbgen.Project
	RepositoryCount int64
}

type CreateInput struct {
	Name        string
	Slug        string // optional
	Description string
	Metadata    map[string]any
}

type UpdateInput struct {
	Name        *string
	Description *string
	Metadata    map[string]any // nil = unchanged
}

type CreateResourceInput struct {
	Kind     dbgen.ProjectResourceKind
	Name     string
	URI      string
	Metadata map[string]any
}

type Service struct {
	db *db.DB
	// OnCreate runs inside the project's creation transaction (e.g. to seed its board columns).
	OnCreate func(ctx context.Context, q *dbgen.Queries, projectID uuid.UUID) error
}

func NewService(d *db.DB) *Service { return &Service{db: d} }

var slugRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// Slugify derives a URL-safe slug from a name.
func Slugify(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			dash = false
		case b.Len() > 0 && !dash:
			b.WriteByte('-')
			dash = true
		}
	}
	s := strings.TrimSuffix(b.String(), "-")
	if len(s) > 100 {
		s = strings.TrimSuffix(s[:100], "-")
	}
	return s
}

func (s *Service) Create(ctx context.Context, in CreateInput) (Project, error) {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return Project{}, apperr.Validation(apperr.FieldError{Field: "name", Message: "must not be blank"})
	}
	meta, err := marshalObject(in.Metadata)
	if err != nil {
		return Project{}, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return Project{}, err
	}
	// The tail of a UUIDv7 is random; it makes derived slugs unique.
	suffix := strings.ReplaceAll(id.String(), "-", "")[26:]

	slug, derived := in.Slug, in.Slug == ""
	if derived {
		if slug = Slugify(in.Name); slug == "" {
			slug = "project-" + suffix
		}
	}
	if !slugRe.MatchString(slug) {
		return Project{}, apperr.Validation(apperr.FieldError{Field: "slug", Message: "must be lowercase letters, digits and single dashes"})
	}
	p, err := s.insert(ctx, id, slug, in, meta)
	if derived && errors.Is(err, errSlugTaken) {
		// Same name as an existing project: keep the readable slug, add a short suffix.
		slug = strings.TrimSuffix(slug[:min(len(slug), 93)], "-") + "-" + suffix
		p, err = s.insert(ctx, id, slug, in, meta)
	}
	if errors.Is(err, errSlugTaken) {
		return Project{}, apperr.Conflict(fmt.Sprintf("a project with slug %q already exists", slug))
	}
	if err != nil {
		return Project{}, err
	}
	return Project{Project: p}, nil
}

var errSlugTaken = errors.New("slug taken")

func (s *Service) insert(ctx context.Context, id uuid.UUID, slug string, in CreateInput, meta []byte) (dbgen.Project, error) {
	var p dbgen.Project
	err := s.db.InTx(ctx, func(q *dbgen.Queries) error {
		var err error
		p, err = q.CreateProject(ctx, dbgen.CreateProjectParams{
			ID: id, Slug: slug, Name: in.Name, Description: in.Description, Metadata: meta,
		})
		if db.IsUniqueViolation(err) {
			return errSlugTaken
		}
		if err != nil {
			return err
		}
		if s.OnCreate != nil {
			if err := s.OnCreate(ctx, q, id); err != nil {
				return err
			}
		}
		return audit.Record(ctx, q, audit.Entry{
			Action: "project.create", EntityType: "project", EntityID: audit.Ptr(id),
			Payload: map[string]string{"slug": slug, "name": in.Name},
		})
	})
	return p, err
}

func (s *Service) List(ctx context.Context, includeArchived bool) ([]Project, error) {
	rows, err := s.db.ListProjects(ctx, includeArchived)
	if err != nil {
		return nil, err
	}
	out := make([]Project, len(rows))
	for i, r := range rows {
		out[i] = Project{Project: r.Project, RepositoryCount: r.RepositoryCount}
	}
	return out, nil
}

func (s *Service) Get(ctx context.Context, id uuid.UUID) (Project, error) {
	row, err := s.db.GetProject(ctx, id)
	if db.IsNotFound(err) {
		return Project{}, errProjectNotFound
	}
	if err != nil {
		return Project{}, err
	}
	return Project{Project: row.Project, RepositoryCount: row.RepositoryCount}, nil
}

func (s *Service) Update(ctx context.Context, id uuid.UUID, in UpdateInput) (Project, error) {
	if in.Name != nil {
		n := strings.TrimSpace(*in.Name)
		if n == "" {
			return Project{}, apperr.Validation(apperr.FieldError{Field: "name", Message: "must not be blank"})
		}
		in.Name = &n
	}
	var meta []byte
	if in.Metadata != nil {
		var err error
		if meta, err = marshalObject(in.Metadata); err != nil {
			return Project{}, err
		}
	}
	err := s.db.InTx(ctx, func(q *dbgen.Queries) error {
		if err := LockActive(ctx, q, id); err != nil {
			return err
		}
		if _, err := q.UpdateProject(ctx, dbgen.UpdateProjectParams{
			ID: id, Name: in.Name, Description: in.Description, Metadata: meta,
		}); err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{
			Action: "project.update", EntityType: "project", EntityID: audit.Ptr(id),
			Payload: map[string]any{"name": in.Name, "description": in.Description, "metadataChanged": meta != nil},
		})
	})
	if err != nil {
		return Project{}, err
	}
	return s.Get(ctx, id)
}

func (s *Service) Archive(ctx context.Context, id uuid.UUID) error {
	return s.db.InTx(ctx, func(q *dbgen.Queries) error {
		n, err := q.ArchiveProject(ctx, id)
		if err != nil {
			return err
		}
		if n == 0 {
			return errProjectNotFound
		}
		return audit.Record(ctx, q, audit.Entry{Action: "project.archive", EntityType: "project", EntityID: audit.Ptr(id)})
	})
}

func (s *Service) ListResources(ctx context.Context, projectID uuid.UUID) ([]dbgen.ProjectResource, error) {
	if _, err := s.Get(ctx, projectID); err != nil {
		return nil, err
	}
	return s.db.ListProjectResources(ctx, projectID)
}

func (s *Service) CreateResource(ctx context.Context, projectID uuid.UUID, in CreateResourceInput) (dbgen.ProjectResource, error) {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return dbgen.ProjectResource{}, apperr.Validation(apperr.FieldError{Field: "name", Message: "must not be blank"})
	}
	if !in.Kind.Valid() {
		return dbgen.ProjectResource{}, apperr.Validation(apperr.FieldError{Field: "kind", Message: "unknown resource kind"})
	}
	meta, err := marshalObject(in.Metadata)
	if err != nil {
		return dbgen.ProjectResource{}, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return dbgen.ProjectResource{}, err
	}
	var res dbgen.ProjectResource
	err = s.db.InTx(ctx, func(q *dbgen.Queries) error {
		if err := LockActive(ctx, q, projectID); err != nil {
			return err
		}
		res, err = q.CreateProjectResource(ctx, dbgen.CreateProjectResourceParams{
			ID: id, ProjectID: projectID, Kind: in.Kind, Name: in.Name, Uri: in.URI, Metadata: meta,
		})
		if err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{
			Action: "project_resource.create", EntityType: "project_resource", EntityID: audit.Ptr(id),
			Payload: map[string]any{"projectId": projectID, "kind": in.Kind, "name": in.Name},
		})
	})
	return res, err
}

func (s *Service) DeleteResource(ctx context.Context, id uuid.UUID) error {
	return s.db.InTx(ctx, func(q *dbgen.Queries) error {
		res, err := q.DeleteProjectResource(ctx, id)
		if db.IsNotFound(err) {
			return apperr.NotFound("resource not found")
		}
		if err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{
			Action: "project_resource.delete", EntityType: "project_resource", EntityID: audit.Ptr(id),
			Payload: map[string]any{"projectId": res.ProjectID, "name": res.Name},
		})
	})
}

var errProjectNotFound = apperr.NotFound("project not found")

// LockActive row-locks an active (non-archived) project inside a transaction,
// so children are never attached to a project being archived concurrently.
func LockActive(ctx context.Context, q *dbgen.Queries, id uuid.UUID) error {
	_, err := q.LockActiveProject(ctx, id)
	if db.IsNotFound(err) {
		return apperr.NotFound("project not found or archived")
	}
	return err
}

func marshalObject(m map[string]any) ([]byte, error) {
	if m == nil {
		return []byte("{}"), nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil, apperr.BadRequest("metadata must be a JSON object").WithCause(err)
	}
	return b, nil
}

// MarshalObject is exported for sibling packages.
func MarshalObject(m map[string]any) ([]byte, error) { return marshalObject(m) }
