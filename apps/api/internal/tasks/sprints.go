package tasks

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"wise/apps/api/internal/apperr"
	"wise/apps/api/internal/audit"
	"wise/apps/api/internal/db"
	"wise/apps/api/internal/db/dbgen"
)

// SprintInput is a sprint's editable fields.
type SprintInput struct {
	Name     string
	StartsOn *time.Time
	EndsOn   *time.Time
}

func (in *SprintInput) validate() error {
	in.Name = strings.TrimSpace(in.Name)
	var errs []apperr.FieldError
	if in.Name == "" || utf8.RuneCountInString(in.Name) > 100 {
		errs = append(errs, apperr.FieldError{Field: "name", Message: "must be 1-100 characters"})
	}
	if in.StartsOn != nil && in.EndsOn != nil && in.EndsOn.Before(*in.StartsOn) {
		errs = append(errs, apperr.FieldError{Field: "endsOn", Message: "must not be before the start"})
	}
	if len(errs) > 0 {
		return apperr.Validation(errs...)
	}
	return nil
}

func pgDate(t *time.Time) pgtype.Date {
	if t == nil {
		return pgtype.Date{}
	}
	return pgtype.Date{Time: time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC), Valid: true}
}

// Sprints lists a project's sprints (open first, newest first).
func (s *Service) Sprints(ctx context.Context, projectID uuid.UUID) ([]dbgen.ListSprintsRow, error) {
	if _, err := s.db.GetProject(ctx, projectID); db.IsNotFound(err) {
		return nil, apperr.NotFound("project not found")
	} else if err != nil {
		return nil, err
	}
	return s.db.ListSprints(ctx, projectID)
}

// CreateSprint adds a sprint; the project's first sprint becomes current.
func (s *Service) CreateSprint(ctx context.Context, projectID uuid.UUID, in SprintInput) (dbgen.Sprint, error) {
	if err := in.validate(); err != nil {
		return dbgen.Sprint{}, err
	}
	var sp dbgen.Sprint
	err := s.db.InTx(ctx, func(q *dbgen.Queries) error {
		if _, err := q.GetProject(ctx, projectID); db.IsNotFound(err) {
			return apperr.NotFound("project not found")
		} else if err != nil {
			return err
		}
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		if sp, err = q.InsertSprint(ctx, dbgen.InsertSprintParams{ID: id, ProjectID: projectID, Name: in.Name,
			StartsOn: pgDate(in.StartsOn), EndsOn: pgDate(in.EndsOn)}); err != nil {
			return err
		}
		if cur, err := q.GetCurrentSprintID(ctx, projectID); err == nil && cur == nil {
			if err := q.SetCurrentSprint(ctx, dbgen.SetCurrentSprintParams{SprintID: &id, ProjectID: projectID}); err != nil {
				return err
			}
		}
		return audit.Record(ctx, q, audit.Entry{Action: "sprint.create", EntityType: "sprint", EntityID: audit.Ptr(id),
			Payload: map[string]any{"projectId": projectID, "name": in.Name}})
	})
	return sp, err
}

func lockSprint(ctx context.Context, q *dbgen.Queries, id uuid.UUID) (dbgen.Sprint, error) {
	sp, err := q.GetSprint(ctx, id)
	if db.IsNotFound(err) {
		return sp, apperr.NotFound("sprint not found")
	}
	return sp, err
}

// UpdateSprint renames or re-dates a sprint.
func (s *Service) UpdateSprint(ctx context.Context, id uuid.UUID, in SprintInput) (dbgen.Sprint, error) {
	if err := in.validate(); err != nil {
		return dbgen.Sprint{}, err
	}
	var sp dbgen.Sprint
	err := s.db.InTx(ctx, func(q *dbgen.Queries) error {
		cur, err := lockSprint(ctx, q, id)
		if err != nil {
			return err
		}
		if sp, err = q.UpdateSprint(ctx, dbgen.UpdateSprintParams{ID: id, Name: in.Name, StartsOn: pgDate(in.StartsOn),
			EndsOn: pgDate(in.EndsOn), Closed: cur.Closed}); err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{Action: "sprint.update", EntityType: "sprint", EntityID: audit.Ptr(id),
			Payload: map[string]any{"name": in.Name}})
	})
	return sp, err
}

// switchCurrent makes a sprint current, carrying unfinished Wise tasks over from the previous one.
func switchCurrent(ctx context.Context, q *dbgen.Queries, projectID uuid.UUID, to *uuid.UUID) (int64, error) {
	from, err := q.GetCurrentSprintID(ctx, projectID)
	if err != nil {
		return 0, err
	}
	if (from == nil && to == nil) || (from != nil && to != nil && *from == *to) {
		return 0, nil
	}
	var moved int64
	if from != nil {
		if moved, err = q.CarryOverTasks(ctx, dbgen.CarryOverTasksParams{ToSprint: to, FromSprint: from}); err != nil {
			return 0, err
		}
	}
	if err := q.SetCurrentSprint(ctx, dbgen.SetCurrentSprintParams{SprintID: to, ProjectID: projectID}); err != nil {
		return 0, err
	}
	return moved, audit.Record(ctx, q, audit.Entry{Action: "sprint.current", EntityType: "project", EntityID: audit.Ptr(projectID),
		Payload: map[string]any{"from": from, "to": to, "carriedOver": moved}})
}

// StartSprint makes a sprint the current one (reopening it if closed).
func (s *Service) StartSprint(ctx context.Context, id uuid.UUID) error {
	return s.db.InTx(ctx, func(q *dbgen.Queries) error {
		sp, err := lockSprint(ctx, q, id)
		if err != nil {
			return err
		}
		if sp.Closed {
			if _, err := q.UpdateSprint(ctx, dbgen.UpdateSprintParams{ID: id, Name: sp.Name, StartsOn: sp.StartsOn, EndsOn: sp.EndsOn}); err != nil {
				return err
			}
		}
		_, err = switchCurrent(ctx, q, sp.ProjectID, &id)
		return err
	})
}

// CompleteSprint closes a sprint; if it was current, the next open sprint
// (by start date) takes over and unfinished Wise tasks move to it.
func (s *Service) CompleteSprint(ctx context.Context, id uuid.UUID) error {
	return s.db.InTx(ctx, func(q *dbgen.Queries) error {
		sp, err := lockSprint(ctx, q, id)
		if err != nil {
			return err
		}
		if _, err := q.UpdateSprint(ctx, dbgen.UpdateSprintParams{ID: id, Name: sp.Name, StartsOn: sp.StartsOn, EndsOn: sp.EndsOn, Closed: true}); err != nil {
			return err
		}
		cur, err := q.GetCurrentSprintID(ctx, sp.ProjectID)
		if err != nil {
			return err
		}
		if cur != nil && *cur == id {
			next, err := nextOpenSprint(ctx, q, sp.ProjectID, id, time.Now())
			if err != nil {
				return err
			}
			if _, err := switchCurrent(ctx, q, sp.ProjectID, next); err != nil {
				return err
			}
		}
		return audit.Record(ctx, q, audit.Entry{Action: "sprint.complete", EntityType: "sprint", EntityID: audit.Ptr(id),
			Payload: map[string]any{"name": sp.Name}})
	})
}

// DeleteSprint removes a sprint; its tasks keep existing without a sprint.
func (s *Service) DeleteSprint(ctx context.Context, id uuid.UUID) error {
	return s.db.InTx(ctx, func(q *dbgen.Queries) error {
		sp, err := lockSprint(ctx, q, id)
		if err != nil {
			return err
		}
		if err := q.DeleteSprint(ctx, id); err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{Action: "sprint.delete", EntityType: "sprint", EntityID: audit.Ptr(id),
			Payload: map[string]any{"name": sp.Name, "projectId": sp.ProjectID}})
	})
}

func dateOf(d pgtype.Date) (time.Time, bool) { return d.Time, d.Valid }

func today(now time.Time) time.Time {
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
}

// running reports whether a sprint's dates include the day.
func running(sp dbgen.ListSprintsRow, day time.Time) bool {
	start, okS := dateOf(sp.StartsOn)
	end, okE := dateOf(sp.EndsOn)
	return okS && !start.After(day) && (!okE || !end.Before(day))
}

// nextOpenSprint picks the open sprint to continue with: one running today
// (latest start), else the earliest upcoming one, else none.
func nextOpenSprint(ctx context.Context, q *dbgen.Queries, projectID, except uuid.UUID, now time.Time) (*uuid.UUID, error) {
	rows, err := q.ListSprints(ctx, projectID)
	if err != nil {
		return nil, err
	}
	day := today(now)
	var best, upcoming *dbgen.ListSprintsRow
	for i := range rows {
		r := &rows[i]
		if r.Closed || r.ID == except {
			continue
		}
		if running(*r, day) && (best == nil || r.StartsOn.Time.After(best.StartsOn.Time)) {
			best = r
		}
		if start, ok := dateOf(r.StartsOn); ok && start.After(day) && (upcoming == nil || start.Before(upcoming.StartsOn.Time)) {
			upcoming = r
		}
	}
	if best == nil {
		best = upcoming
	}
	if best == nil {
		return nil, nil
	}
	return &best.ID, nil
}

// RefreshSprints moves a project to the sprint running today once the current
// one has ended (or is closed, or there is none). A sprint started by hand
// stays current until its end date passes.
func (s *Service) RefreshSprints(ctx context.Context, projectID uuid.UUID, now time.Time) error {
	return s.db.InTx(ctx, func(q *dbgen.Queries) error {
		cur, err := q.GetCurrentSprintID(ctx, projectID)
		if err != nil {
			return err
		}
		day := today(now)
		if cur != nil {
			sp, err := q.GetSprint(ctx, *cur)
			if err != nil {
				return err
			}
			end, hasEnd := dateOf(sp.EndsOn)
			if !sp.Closed && (!hasEnd || !end.Before(day)) {
				return nil // still running
			}
		}
		rows, err := q.ListSprints(ctx, projectID)
		if err != nil {
			return err
		}
		var next *dbgen.ListSprintsRow
		for i := range rows {
			r := &rows[i]
			if !r.Closed && running(*r, day) && (cur == nil || r.ID != *cur) && (next == nil || r.StartsOn.Time.After(next.StartsOn.Time)) {
				next = r
			}
		}
		if next == nil {
			return nil
		}
		_, err = switchCurrent(ctx, q, projectID, &next.ID)
		return err
	})
}

// RefreshAllSprints refreshes every project with open sprints.
func (s *Service) RefreshAllSprints(ctx context.Context, now time.Time) error {
	ids, err := s.db.ListProjectsWithSprints(ctx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := s.RefreshSprints(ctx, id, now); err != nil {
			return fmt.Errorf("project %s: %w", id, err)
		}
	}
	return nil
}

// ExternalSprint is a sprint as a source reports it (a ClickUp sprint list, a Jira sprint).
type ExternalSprint struct {
	ID       string
	Name     string
	StartsOn *time.Time
	EndsOn   *time.Time
	Closed   bool
}

// FollowSprint creates or updates the Wise sprint mirroring a source's sprint.
func FollowSprint(ctx context.Context, q *dbgen.Queries, projectID, integrationID uuid.UUID, x ExternalSprint) (uuid.UUID, error) {
	name := strings.TrimSpace(x.Name)
	if name == "" {
		name = "Sprint " + x.ID
	}
	if utf8.RuneCountInString(name) > 100 {
		name = string([]rune(name)[:100])
	}
	start, end := pgDate(x.StartsOn), pgDate(x.EndsOn)
	if start.Valid && end.Valid && end.Time.Before(start.Time) {
		end = start
	}
	cur, err := q.GetSprintByExternal(ctx, dbgen.GetSprintByExternalParams{IntegrationID: &integrationID, ExternalID: &x.ID})
	if db.IsNotFound(err) {
		id, err := uuid.NewV7()
		if err != nil {
			return uuid.Nil, err
		}
		sp, err := q.InsertSprint(ctx, dbgen.InsertSprintParams{ID: id, ProjectID: projectID, Name: name, StartsOn: start, EndsOn: end,
			IntegrationID: &integrationID, ExternalID: &x.ID, Closed: x.Closed})
		if err != nil {
			return uuid.Nil, err
		}
		return sp.ID, audit.Record(ctx, q, audit.Entry{Action: "sprint.follow", EntityType: "sprint", EntityID: audit.Ptr(sp.ID),
			Payload: map[string]any{"integrationId": integrationID, "name": name}})
	}
	if err != nil {
		return uuid.Nil, err
	}
	if cur.Name != name || cur.StartsOn != start || cur.EndsOn != end || cur.Closed != x.Closed {
		if _, err := q.UpdateSprint(ctx, dbgen.UpdateSprintParams{ID: cur.ID, Name: name, StartsOn: start, EndsOn: end, Closed: x.Closed}); err != nil {
			return uuid.Nil, err
		}
	}
	return cur.ID, nil
}

// projectSprint checks that a sprint belongs to the project.
func projectSprint(ctx context.Context, q *dbgen.Queries, projectID, sprintID uuid.UUID, field string) error {
	sp, err := q.GetSprint(ctx, sprintID)
	if db.IsNotFound(err) || (err == nil && sp.ProjectID != projectID) {
		return apperr.Validation(apperr.FieldError{Field: field, Message: "not a sprint of this project"})
	}
	return err
}

// ProjectSprint is projectSprint for other packages.
func ProjectSprint(ctx context.Context, q *dbgen.Queries, projectID, sprintID uuid.UUID, field string) error {
	return projectSprint(ctx, q, projectID, sprintID, field)
}
