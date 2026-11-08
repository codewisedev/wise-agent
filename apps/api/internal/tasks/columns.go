package tasks

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"wise/apps/api/internal/apperr"
	"wise/apps/api/internal/audit"
	"wise/apps/api/internal/db"
	"wise/apps/api/internal/db/dbgen"
)

// SeedColumns gives a new project the panel-configured default columns.
func (s *Service) SeedColumns(ctx context.Context, q *dbgen.Queries, projectID uuid.UUID) error {
	for i, c := range s.BoardDefaultColumns() {
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		if _, err := q.InsertBoardColumn(ctx, dbgen.InsertBoardColumnParams{
			ID: id, ProjectID: projectID, Name: strings.TrimSpace(c.Name), Position: float64(i + 1),
			Role: strPtrOrNil(c.Role), Color: strPtrOrNil(strings.ToLower(c.Color)),
		}); err != nil {
			return err
		}
	}
	return nil
}

// DefaultColumn is where new cards go: the backlog column, else the first one.
func DefaultColumn(ctx context.Context, q *dbgen.Queries, projectID uuid.UUID) (dbgen.BoardColumn, error) {
	if c, err := q.GetColumnByRole(ctx, dbgen.GetColumnByRoleParams{ProjectID: projectID, Role: strPtr(RoleBacklog)}); err == nil {
		return c, nil
	} else if !db.IsNotFound(err) {
		return c, err
	}
	cols, err := q.ListBoardColumns(ctx, projectID)
	if err != nil {
		return dbgen.BoardColumn{}, err
	}
	if len(cols) == 0 {
		return dbgen.BoardColumn{}, apperr.Conflict("this project has no board columns; add one on its board")
	}
	return cols[0], nil
}

// ColumnForRole finds the project's column with a role (ok=false if none).
func ColumnForRole(ctx context.Context, q *dbgen.Queries, projectID uuid.UUID, role string) (dbgen.BoardColumn, bool, error) {
	c, err := q.GetColumnByRole(ctx, dbgen.GetColumnByRoleParams{ProjectID: projectID, Role: &role})
	if db.IsNotFound(err) {
		return c, false, nil
	}
	return c, err == nil, err
}

// projectColumn checks that a column belongs to the project.
func projectColumn(ctx context.Context, q *dbgen.Queries, projectID, columnID uuid.UUID, field string) (dbgen.BoardColumn, error) {
	c, err := q.GetBoardColumn(ctx, columnID)
	if db.IsNotFound(err) || (err == nil && c.ProjectID != projectID) {
		return c, apperr.Validation(apperr.FieldError{Field: field, Message: "not a column of this project's board"})
	}
	return c, err
}

// Columns lists a project's columns in order.
func (s *Service) Columns(ctx context.Context, projectID uuid.UUID) ([]dbgen.BoardColumn, error) {
	if _, err := s.db.GetProject(ctx, projectID); db.IsNotFound(err) {
		return nil, apperr.NotFound("project not found")
	} else if err != nil {
		return nil, err
	}
	return s.db.ListBoardColumns(ctx, projectID)
}

// ColumnInput is a column's editable fields.
type ColumnInput struct {
	Name     string
	Role     *string
	Color    *string
	WIPLimit *int32
	Position *float64
}

func (in *ColumnInput) validate() error {
	var errs []apperr.FieldError
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || utf8.RuneCountInString(in.Name) > 40 {
		errs = append(errs, apperr.FieldError{Field: "name", Message: "must be 1-40 characters"})
	}
	if in.Role != nil && *in.Role == "" {
		in.Role = nil
	}
	if in.Role != nil && !contains(Roles, *in.Role) {
		errs = append(errs, apperr.FieldError{Field: "role", Message: "one of " + strings.Join(Roles, ", ")})
	}
	if in.Color != nil && *in.Color == "" {
		in.Color = nil
	}
	if in.Color != nil {
		if !colorRe.MatchString(*in.Color) {
			errs = append(errs, apperr.FieldError{Field: "color", Message: "must be #rrggbb"})
		}
		c := strings.ToLower(*in.Color)
		in.Color = &c
	}
	if in.WIPLimit != nil && (*in.WIPLimit < 1 || *in.WIPLimit > 1000) {
		errs = append(errs, apperr.FieldError{Field: "wipLimit", Message: "must be 1-1000"})
	}
	if len(errs) > 0 {
		return apperr.Validation(errs...)
	}
	return nil
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// checkName refuses a duplicate column name within the project.
func checkName(cols []dbgen.BoardColumn, name string, self uuid.UUID) error {
	for _, c := range cols {
		if c.ID != self && strings.EqualFold(c.Name, name) {
			return apperr.Conflict(fmt.Sprintf("this board already has a column named %q", c.Name))
		}
	}
	return nil
}

// CreateColumn adds a column (at the end unless a position is given). Taking a
// role moves it from the column that had it.
func (s *Service) CreateColumn(ctx context.Context, projectID uuid.UUID, in ColumnInput) (dbgen.BoardColumn, error) {
	if err := in.validate(); err != nil {
		return dbgen.BoardColumn{}, err
	}
	var col dbgen.BoardColumn
	err := s.db.InTx(ctx, func(q *dbgen.Queries) error {
		if _, err := q.GetProject(ctx, projectID); db.IsNotFound(err) {
			return apperr.NotFound("project not found")
		} else if err != nil {
			return err
		}
		cols, err := q.LockBoardColumns(ctx, projectID)
		if err != nil {
			return err
		}
		if len(cols) >= 20 {
			return apperr.Conflict("a board can have at most 20 columns")
		}
		if err := checkName(cols, in.Name, uuid.Nil); err != nil {
			return err
		}
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		pos := 1.0
		if in.Position != nil {
			pos = *in.Position
		} else if pos, err = q.NextColumnPosition(ctx, projectID); err != nil {
			return err
		}
		if in.Role != nil {
			if err := q.ClearColumnRole(ctx, dbgen.ClearColumnRoleParams{ProjectID: projectID, Role: in.Role, ID: id}); err != nil {
				return err
			}
		}
		if col, err = q.InsertBoardColumn(ctx, dbgen.InsertBoardColumnParams{
			ID: id, ProjectID: projectID, Name: in.Name, Position: pos, Role: in.Role, Color: in.Color, WipLimit: in.WIPLimit,
		}); err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{Action: "board_column.create", EntityType: "board_column", EntityID: audit.Ptr(id),
			Payload: map[string]any{"projectId": projectID, "name": in.Name}})
	})
	return col, err
}

// UpdateColumn renames, recolors, reorders or re-roles a column.
func (s *Service) UpdateColumn(ctx context.Context, id uuid.UUID, in ColumnInput) (dbgen.BoardColumn, error) {
	if err := in.validate(); err != nil {
		return dbgen.BoardColumn{}, err
	}
	var col dbgen.BoardColumn
	err := s.db.InTx(ctx, func(q *dbgen.Queries) error {
		cur, err := q.GetBoardColumn(ctx, id)
		if db.IsNotFound(err) {
			return apperr.NotFound("column not found")
		}
		if err != nil {
			return err
		}
		cols, err := q.LockBoardColumns(ctx, cur.ProjectID)
		if err != nil {
			return err
		}
		if err := checkName(cols, in.Name, id); err != nil {
			return err
		}
		pos := cur.Position
		if in.Position != nil {
			pos = *in.Position
		}
		if in.Role != nil {
			if err := q.ClearColumnRole(ctx, dbgen.ClearColumnRoleParams{ProjectID: cur.ProjectID, Role: in.Role, ID: id}); err != nil {
				return err
			}
		}
		if col, err = q.UpdateBoardColumn(ctx, dbgen.UpdateBoardColumnParams{
			ID: id, Name: in.Name, Position: pos, Role: in.Role, Color: in.Color, WipLimit: in.WIPLimit,
		}); err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{Action: "board_column.update", EntityType: "board_column", EntityID: audit.Ptr(id),
			Payload: map[string]any{"name": in.Name, "role": in.Role}})
	})
	return col, err
}

// DeleteColumn removes a column; its cards move to moveTo (required when it has cards).
func (s *Service) DeleteColumn(ctx context.Context, id uuid.UUID, moveTo *uuid.UUID) error {
	return s.db.InTx(ctx, func(q *dbgen.Queries) error {
		cur, err := q.GetBoardColumn(ctx, id)
		if db.IsNotFound(err) {
			return apperr.NotFound("column not found")
		}
		if err != nil {
			return err
		}
		cols, err := q.LockBoardColumns(ctx, cur.ProjectID)
		if err != nil {
			return err
		}
		if len(cols) == 1 {
			return apperr.Conflict("a board needs at least one column")
		}
		n, err := q.CountColumnCards(ctx, id)
		if err != nil {
			return err
		}
		var to *string
		if moveTo != nil {
			if *moveTo == id {
				return apperr.Validation(apperr.FieldError{Field: "moveTo", Message: "must be another column"})
			}
			if _, err := projectColumn(ctx, q, cur.ProjectID, *moveTo, "moveTo"); err != nil {
				return err
			}
			t := moveTo.String()
			to = &t
		}
		if n > 0 {
			if moveTo == nil {
				return apperr.Conflict(fmt.Sprintf("%q has %d cards; choose a column to move them to", cur.Name, n))
			}
			base, err := q.NextBoardPosition(ctx, *moveTo)
			if err != nil {
				return err
			}
			if err := q.MoveColumnCards(ctx, dbgen.MoveColumnCardsParams{ToColumn: *moveTo, Base: base, FromColumn: id}); err != nil {
				return err
			}
		}
		if err := q.RemapIntegrationColumn(ctx, dbgen.RemapIntegrationColumnParams{FromColumn: id.String(), ToColumn: to, ProjectID: cur.ProjectID}); err != nil {
			return err
		}
		if err := q.DeleteBoardColumn(ctx, id); err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{Action: "board_column.delete", EntityType: "board_column", EntityID: audit.Ptr(id),
			Payload: map[string]any{"name": cur.Name, "cardsMoved": n, "moveTo": to}})
	})
}

func strPtr(s string) *string { return &s }
