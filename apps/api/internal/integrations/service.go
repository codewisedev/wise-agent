package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"wise/apps/api/internal/accounts"
	"wise/apps/api/internal/apperr"
	"wise/apps/api/internal/audit"
	"wise/apps/api/internal/db"
	"wise/apps/api/internal/db/dbgen"
	"wise/apps/api/internal/projects"
	"wise/apps/api/internal/tasks"
)

type Service struct {
	db       *db.DB
	accounts *accounts.Service
	tasks    *tasks.Service
	log      *slog.Logger
	// newImporter is replaceable in tests.
	newImporter func(kind string, cfg map[string]any, cred Credentials) (Importer, error)
}

func NewService(d *db.DB, acc *accounts.Service, t *tasks.Service, log *slog.Logger) *Service {
	return &Service{db: d, accounts: acc, tasks: t, log: log, newImporter: New}
}

// Input creates or updates an integration.
type Input struct {
	Name              string
	Kind              string
	AccountID         uuid.UUID
	Config            map[string]any
	StatusMap         map[string]string
	DefaultTemplateID *uuid.UUID
	AutoStart         bool
	IntervalMinutes   int
	Enabled           bool
	SprintID          *uuid.UUID // fixed sprint for its tasks (follow modes create their own)
}

// Result summarizes one sync.
type Result struct {
	Total     int       `json:"total"`
	Created   int       `json:"created"`
	Updated   int       `json:"updated"`
	Unchanged int       `json:"unchanged"`
	Started   int       `json:"started"`
	Archived  int       `json:"archived"` // no longer returned by the source (e.g. unassigned)
	Restored  int       `json:"restored"` // archived earlier, returned again
	Errors    []string  `json:"errors,omitempty"`
	At        time.Time `json:"at"`
}

func (s *Service) List(ctx context.Context, projectID uuid.UUID) ([]dbgen.Integration, error) {
	return s.db.ListIntegrations(ctx, projectID)
}

func (s *Service) Get(ctx context.Context, id uuid.UUID) (dbgen.Integration, error) {
	it, err := s.db.GetIntegration(ctx, id)
	if db.IsNotFound(err) {
		return it, apperr.NotFound("integration not found")
	}
	return it, err
}

func (s *Service) validate(ctx context.Context, projectID uuid.UUID, in *Input) error {
	var errs []apperr.FieldError
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 100 {
		errs = append(errs, apperr.FieldError{Field: "name", Message: "must be 1-100 characters"})
	}
	if in.IntervalMinutes == 0 {
		in.IntervalMinutes = 15
	}
	if in.IntervalMinutes < 5 || in.IntervalMinutes > 1440 {
		errs = append(errs, apperr.FieldError{Field: "intervalMinutes", Message: "between 5 and 1440"})
	}
	acct, err := s.accounts.Get(ctx, in.AccountID)
	switch {
	case err != nil:
		errs = append(errs, apperr.FieldError{Field: "accountId", Message: "no such account"})
	case acct.Kind != in.Kind:
		errs = append(errs, apperr.FieldError{Field: "accountId", Message: fmt.Sprintf("a %s integration needs a %s account", in.Kind, in.Kind)})
	}
	norm := map[string]string{}
	for k, v := range in.StatusMap {
		colID, perr := uuid.Parse(v)
		if perr == nil {
			if c, err := s.db.GetBoardColumn(ctx, colID); err != nil || c.ProjectID != projectID {
				perr = errors.New("not a column of this project")
			}
		}
		if perr != nil {
			errs = append(errs, apperr.FieldError{Field: "statusMap", Message: fmt.Sprintf("%q maps to %q, which is not a column of this project's board", k, v)})
			continue
		}
		if k = strings.ToLower(strings.TrimSpace(k)); k != "" {
			norm[k] = v
		}
	}
	in.StatusMap = norm
	if in.SprintID != nil {
		if sp, err := s.db.GetSprint(ctx, *in.SprintID); err != nil || sp.ProjectID != projectID {
			errs = append(errs, apperr.FieldError{Field: "sprintId", Message: "not a sprint of this project"})
		}
	}
	if in.Config == nil {
		in.Config = map[string]any{}
	}
	if err == nil && acct.Kind == in.Kind {
		// Configuration errors (no JQL, list or board id) surface at save time.
		if _, ierr := New(in.Kind, in.Config, Credentials{BaseURL: acct.BaseUrl, Secret: "x", Meta: map[string]any{"email": "x@x", "apiKey": "x"}}); ierr != nil {
			errs = append(errs, apperr.FieldError{Field: "config", Message: ierr.Error()})
		}
	}
	if in.DefaultTemplateID != nil {
		if _, err := s.tasks.GetTemplate(ctx, *in.DefaultTemplateID); err != nil {
			errs = append(errs, apperr.FieldError{Field: "defaultTemplateId", Message: "no such template"})
		}
	}
	if len(errs) > 0 {
		return apperr.Validation(errs...)
	}
	return nil
}

func (s *Service) Create(ctx context.Context, projectID uuid.UUID, in Input, userID *uuid.UUID) (dbgen.Integration, error) {
	switch in.Kind {
	case "jira", "clickup", "trello":
	default:
		return dbgen.Integration{}, apperr.Validation(apperr.FieldError{Field: "kind", Message: "jira, clickup or trello"})
	}
	if err := s.validate(ctx, projectID, &in); err != nil {
		return dbgen.Integration{}, err
	}
	cfg, _ := json.Marshal(in.Config)
	sm, _ := json.Marshal(in.StatusMap)
	id, err := uuid.NewV7()
	if err != nil {
		return dbgen.Integration{}, err
	}
	var it dbgen.Integration
	err = s.db.InTx(ctx, func(q *dbgen.Queries) error {
		if err := projects.LockActive(ctx, q, projectID); err != nil {
			return err
		}
		if it, err = q.CreateIntegration(ctx, dbgen.CreateIntegrationParams{
			ID: id, ProjectID: projectID, Kind: in.Kind, Name: in.Name, AccountID: in.AccountID, Config: cfg, StatusMap: sm,
			DefaultTemplateID: in.DefaultTemplateID, AutoStart: in.AutoStart, IntervalMinutes: int32(in.IntervalMinutes),
			Enabled: in.Enabled, CreatedBy: userID, SprintID: in.SprintID,
		}); err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{Action: "integration.create", EntityType: "integration", EntityID: audit.Ptr(id),
			Payload: map[string]any{"kind": in.Kind, "name": in.Name, "projectId": projectID}})
	})
	return it, err
}

func (s *Service) Update(ctx context.Context, id uuid.UUID, in Input) (dbgen.Integration, error) {
	cur, err := s.Get(ctx, id)
	if err != nil {
		return cur, err
	}
	in.Kind = cur.Kind
	if err := s.validate(ctx, cur.ProjectID, &in); err != nil {
		return dbgen.Integration{}, err
	}
	cfg, _ := json.Marshal(in.Config)
	sm, _ := json.Marshal(in.StatusMap)
	var it dbgen.Integration
	err = s.db.InTx(ctx, func(q *dbgen.Queries) error {
		if it, err = q.UpdateIntegration(ctx, dbgen.UpdateIntegrationParams{
			ID: id, Name: in.Name, AccountID: in.AccountID, Config: cfg, StatusMap: sm, DefaultTemplateID: in.DefaultTemplateID,
			AutoStart: in.AutoStart, IntervalMinutes: int32(in.IntervalMinutes), Enabled: in.Enabled, SprintID: in.SprintID,
		}); err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{Action: "integration.update", EntityType: "integration", EntityID: audit.Ptr(id)})
	})
	return it, err
}

func (s *Service) Delete(ctx context.Context, id uuid.UUID) error {
	return s.db.InTx(ctx, func(q *dbgen.Queries) error {
		n, err := q.DeleteIntegration(ctx, id)
		if err != nil {
			return err
		}
		if n == 0 {
			return apperr.NotFound("integration not found")
		}
		// Imported tasks stay on the board (their integration_id becomes NULL).
		return audit.Record(ctx, q, audit.Entry{Action: "integration.delete", EntityType: "integration", EntityID: audit.Ptr(id)})
	})
}

// ErrBusy means another sync of the same integration is running.
var ErrBusy = errors.New("a sync of this integration is already running")

// Sync imports the source's current tasks. New tasks land in the column their
// status maps to; existing tasks take the source's title, description, labels
// and priority, and follow a status change unless a workflow is running on them.
func (s *Service) Sync(ctx context.Context, id uuid.UUID) (Result, error) {
	it, err := s.db.LeaseIntegrationSync(ctx, id)
	if db.IsNotFound(err) {
		if _, gerr := s.Get(ctx, id); gerr != nil {
			return Result{}, gerr
		}
		return Result{}, apperr.Conflict(ErrBusy.Error()).WithCause(ErrBusy)
	}
	if err != nil {
		return Result{}, err
	}
	if p, perr := s.db.GetProject(ctx, it.ProjectID); perr == nil && p.Project.ArchivedAt != nil {
		syncErr := apperr.Conflict("the project is archived; its task sources do not sync")
		_ = s.db.FinishIntegrationSync(context.WithoutCancel(ctx), dbgen.FinishIntegrationSyncParams{ID: id, LastResult: it.LastResult, LastError: it.LastError})
		return Result{}, syncErr
	}
	res, syncErr := s.sync(ctx, it)
	res.At = time.Now().UTC()
	raw, _ := json.Marshal(res)
	var msg *string
	if syncErr != nil {
		m := syncErr.Error()
		msg = &m
	}
	// Record the outcome even if the caller's request was cancelled.
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := s.db.FinishIntegrationSync(fctx, dbgen.FinishIntegrationSyncParams{ID: id, LastResult: raw, LastError: msg}); err != nil {
		s.log.Warn("record integration sync", "integration", id, "err", err)
	}
	return res, syncErr
}

func (s *Service) sync(ctx context.Context, it dbgen.Integration) (Result, error) {
	var res Result
	acct, secret, meta, err := s.accounts.Credential(ctx, it.AccountID)
	if err != nil {
		return res, fmt.Errorf("account: %w", err)
	}
	cfg := map[string]any{}
	_ = json.Unmarshal(it.Config, &cfg)
	statusMap := map[string]string{}
	_ = json.Unmarshal(it.StatusMap, &statusMap)
	imp, err := s.newImporter(it.Kind, cfg, Credentials{BaseURL: acct.BaseUrl, Secret: secret, Meta: meta})
	if err != nil {
		return res, err
	}
	items, err := imp.Fetch(ctx)
	if err != nil {
		return res, err
	}
	res.Total = len(items)
	var toStart []uuid.UUID
	for _, x := range items {
		created, changed, restored, err := s.upsert(ctx, it, x, statusMap)
		switch {
		case err != nil:
			res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", x.Key, err))
		case created != uuid.Nil:
			res.Created++
			if it.AutoStart {
				toStart = append(toStart, created)
			}
		case restored: // counted once, as restored (even if it also changed)
			res.Restored++
		case changed:
			res.Updated++
		default:
			res.Unchanged++
		}
		if len(res.Errors) > 20 {
			break
		}
	}
	// Tasks the source no longer returns are archived. Skipped when the fetch was
	// capped (missing items may just be beyond the cap).
	if len(items) < MaxItems {
		seen := make([]string, 0, len(items))
		for _, x := range items {
			seen = append(seen, x.ID)
		}
		params := dbgen.ArchiveMissingImportedParams{IntegrationID: it.ID, Seen: seen, Sprints: []string{}}
		if fs, ok := imp.(interface{ FetchedSprints() ([]string, bool) }); ok {
			if read, following := fs.FetchedSprints(); following {
				params.Scoped = true
				params.Sprints = append(params.Sprints, read...)
			}
		}
		n, err := s.db.ArchiveMissingImported(ctx, params)
		if err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("archive: %v", err))
		}
		res.Archived = int(n)
	}
	// A followed source may have brought a new sprint: advance the project if its sprint ended.
	if err := s.tasks.RefreshSprints(ctx, it.ProjectID, time.Now()); err != nil {
		res.Errors = append(res.Errors, fmt.Sprintf("sprints: %v", err))
	}
	for _, id := range toStart {
		if _, err := s.tasks.StartWorkflow(ctx, id, it.CreatedBy, false); err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("start %s: %v", id, err))
			continue
		}
		res.Started++
	}
	return res, nil
}

// activeTaskStatuses are task states where a workflow owns the card.
var activeTaskStatuses = []string{"in_progress", "awaiting_approval"}

func (s *Service) upsert(ctx context.Context, it dbgen.Integration, x ExternalTask, statusMap map[string]string) (uuid.UUID, bool, bool, error) {
	if x.ID == "" {
		return uuid.Nil, false, false, errors.New("missing id")
	}
	restored := false
	title := clip(strings.TrimSpace(x.Title), 200)
	if utf8.RuneCountInString(title) < 3 {
		title = clip(strings.TrimSpace(x.Key+" "+title), 200)
	}
	desc := clip(x.Description, 20000)
	labels := x.Labels
	if labels == nil {
		labels = []string{}
	}
	if len(labels) > 20 {
		labels = labels[:20]
	}
	priority := x.Priority
	if priority == "" {
		priority = "none"
	}
	mapped := statusMap[strings.ToLower(x.Status)]
	var created uuid.UUID
	changed := false
	err := s.db.InTx(ctx, func(q *dbgen.Queries) error {
		column, err := resolveColumn(ctx, q, it, mapped, DefaultColumn(it.Kind, x))
		if err != nil {
			return err
		}
		// The task's sprint: the source's own (follow mode) or the source's fixed sprint.
		sprint, followed := it.SprintID, false
		if x.Sprint != nil {
			id, err := tasks.FollowSprint(ctx, q, it.ProjectID, it.ID, tasks.ExternalSprint{
				ID: x.Sprint.ID, Name: x.Sprint.Name, StartsOn: x.Sprint.StartsOn, EndsOn: x.Sprint.EndsOn, Closed: x.Sprint.Closed,
			})
			if err != nil {
				return err
			}
			sprint, followed = &id, true
		}
		cur, err := q.GetTaskByExternal(ctx, dbgen.GetTaskByExternalParams{IntegrationID: &it.ID, ExternalID: &x.ID})
		if db.IsNotFound(err) {
			id, err := uuid.NewV7()
			if err != nil {
				return err
			}
			pos, err := q.NextBoardPosition(ctx, column)
			if err != nil {
				return err
			}
			if _, err := q.CreateImportedTask(ctx, dbgen.CreateImportedTaskParams{
				ID: id, ProjectID: it.ProjectID, Title: title, Description: desc, ColumnID: column, Position: pos,
				Priority: priority, Labels: labels, Source: it.Kind, IntegrationID: &it.ID, ExternalID: &x.ID,
				ExternalUrl: strPtr(x.URL), ExternalStatus: strPtr(x.Status), TemplateID: it.DefaultTemplateID, SprintID: sprint,
			}); err != nil {
				return err
			}
			created = id
			return audit.Record(ctx, q, audit.Entry{Action: "task.import", EntityType: "task", EntityID: audit.Ptr(id),
				Payload: map[string]any{"integrationId": it.ID, "externalKey": x.Key}})
		}
		if err != nil {
			return err
		}
		if cur.ArchivedAt != nil {
			// The source returns it again (e.g. reassigned to you): back on the board.
			if err := q.RestoreTask(ctx, cur.ID); err != nil {
				return err
			}
			restored = true
		}
		col, pos := cur.ColumnID, cur.Position
		statusChanged := deref(cur.ExternalStatus) != x.Status
		// Follow the source's status change unless a workflow owns the card now.
		if statusChanged && column != col && !slices.Contains(activeTaskStatuses, cur.Status) {
			if pos, err = q.NextBoardPosition(ctx, column); err != nil {
				return err
			}
			col = column
		}
		// Followed tasks move with their source's sprint (carry-over follows the tool);
		// otherwise a sprint chosen in Wise is kept.
		sprintID := cur.SprintID
		sprintChanged := followed && !sameUUID(cur.SprintID, sprint)
		if sprintChanged {
			sprintID = sprint
		}
		if cur.Title == title && cur.Description == desc && slices.Equal(cur.Labels, labels) && cur.Priority == priority &&
			deref(cur.ExternalUrl) == x.URL && !statusChanged && !sprintChanged {
			changed = restored
			return nil
		}
		changed = true
		_, err = q.UpdateImportedTask(ctx, dbgen.UpdateImportedTaskParams{
			ID: cur.ID, Title: title, Description: desc, Labels: labels, Priority: priority,
			ExternalUrl: strPtr(x.URL), ExternalStatus: strPtr(x.Status), ColumnID: col, Position: pos, SprintID: sprintID,
		})
		return err
	})
	return created, changed, restored, err
}

// RunScheduler syncs due integrations every interval until ctx is done.
func (s *Service) RunScheduler(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		// Sprints roll over by date even without a sync.
		if err := s.tasks.RefreshAllSprints(ctx, time.Now()); err != nil {
			s.log.Warn("refresh sprints", "err", err)
		}
		ids, err := s.db.ListDueIntegrations(ctx)
		if err != nil {
			s.log.Warn("list due integrations", "err", err)
			continue
		}
		for _, id := range ids {
			res, err := s.Sync(ctx, id)
			if errors.Is(err, ErrBusy) {
				continue // a manual or long sync holds the lease
			}
			if err != nil {
				s.log.Warn("integration sync failed", "integration", id, "err", err)
				continue
			}
			s.log.Info("integration synced", "integration", id, "total", res.Total, "created", res.Created, "updated", res.Updated)
		}
	}
}

func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// WithImporter replaces how importers are built (tests).
func (s *Service) WithImporter(fn func(kind string, cfg map[string]any, cred Credentials) (Importer, error)) *Service {
	s.newImporter = fn
	return s
}

// resolveColumn picks the column for an imported item: the status map's
// column, else the project's column for the default role, else the backlog
// (or first) column.
func resolveColumn(ctx context.Context, q *dbgen.Queries, it dbgen.Integration, mapped, role string) (uuid.UUID, error) {
	if id, err := uuid.Parse(mapped); err == nil {
		if c, err := q.GetBoardColumn(ctx, id); err == nil && c.ProjectID == it.ProjectID {
			return c.ID, nil
		}
	}
	if c, ok, err := tasks.ColumnForRole(ctx, q, it.ProjectID, role); err != nil {
		return uuid.Nil, err
	} else if ok {
		return c.ID, nil
	}
	c, err := tasks.DefaultColumn(ctx, q, it.ProjectID)
	return c.ID, err
}

func sameUUID(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
