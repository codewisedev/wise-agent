package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/google/uuid"

	"wise/apps/api/internal/apperr"
	"wise/apps/api/internal/audit"
	"wise/apps/api/internal/db"
	"wise/apps/api/internal/db/dbgen"
	"wise/apps/api/internal/projects"
)

// RunCanceller stops a workflow's agent runs (implemented by agents.Service).
type RunCanceller interface {
	CancelWorkflowRuns(ctx context.Context, workflowRunID uuid.UUID) error
}

type Service struct {
	db    *db.DB
	cfgMu sync.RWMutex
	cfg   WorkflowConfig
	board BoardDefaults
	// Runs, when set, lets CancelTask stop running agents immediately instead of
	// waiting for the workflow's activity to notice the cancellation.
	Runs RunCanceller
	// Agents and Accounts validate workflow plans.
	Agents   AgentLookup
	Accounts AccountChecker
}

func NewService(d *db.DB, cfg WorkflowConfig) *Service { return &Service{db: d, cfg: cfg} }

// Detail is a task with its latest workflow run and everything the dashboard shows.
type Detail struct {
	Task      dbgen.Task
	Workflow  *dbgen.WorkflowRun
	Steps     []dbgen.WorkflowStep
	Artifacts []dbgen.Artifact // latest version of each kind
	Approvals []dbgen.Approval
	// Plan is the latest run's frozen plan, or what running the task would do.
	Plan   Plan
	Issues []Issue
}

// CreateTask creates a task and, when start is true, its first workflow run.
// CreateOptions are optional task settings at creation.
type CreateOptions struct {
	TemplateID *uuid.UUID
	Start      bool
	StepByStep bool
	Column     *uuid.UUID // board column; default: the backlog column (a started run moves it)
	Priority   string
	Labels     []string
}

func (s *Service) CreateTask(ctx context.Context, projectID uuid.UUID, title, description string, opts CreateOptions, userID *uuid.UUID) (dbgen.Task, *dbgen.WorkflowRun, error) {
	title = strings.TrimSpace(title)
	// Agents require a request of at least 3 characters; the title alone must satisfy it.
	if len([]rune(title)) < 3 || len(title) > 200 {
		return dbgen.Task{}, nil, apperr.Validation(apperr.FieldError{Field: "title", Message: "must be 3-200 characters"})
	}
	id, err := uuid.NewV7()
	if err != nil {
		return dbgen.Task{}, nil, err
	}
	var task dbgen.Task
	var run *dbgen.WorkflowRun
	err = s.db.InTx(ctx, func(q *dbgen.Queries) error {
		if err := projects.LockActive(ctx, q, projectID); err != nil {
			return err
		}
		var col dbgen.BoardColumn
		var err error
		if opts.Column != nil {
			col, err = projectColumn(ctx, q, projectID, *opts.Column, "column")
		} else {
			col, err = DefaultColumn(ctx, q, projectID)
		}
		if err != nil {
			return err
		}
		// Every new card goes to the bottom of its column.
		pos, err := q.NextBoardPosition(ctx, col.ID)
		if err != nil {
			return err
		}
		if task, err = q.CreateTask(ctx, dbgen.CreateTaskParams{
			ID: id, ProjectID: projectID, Title: title, Description: strings.TrimSpace(description), CreatedBy: userID,
			ColumnID: col.ID, Position: pos,
		}); err != nil {
			return err
		}
		if err := s.placeCard(ctx, q, &task, CardPatch{Priority: strPtrOrNil(opts.Priority), Labels: opts.Labels}); err != nil {
			return err
		}
		if opts.TemplateID != nil {
			if _, err := q.GetWorkflowTemplate(ctx, *opts.TemplateID); db.IsNotFound(err) {
				return apperr.Validation(apperr.FieldError{Field: "templateId", Message: "no such template"})
			} else if err != nil {
				return err
			}
			if task, err = q.SetTaskWorkflow(ctx, dbgen.SetTaskWorkflowParams{ID: id, TemplateID: opts.TemplateID, WorkflowOverrides: []byte("{}")}); err != nil {
				return err
			}
		}
		if err := audit.Record(ctx, q, audit.Entry{
			Action: "task.create", EntityType: "task", EntityID: audit.Ptr(id),
			Payload: map[string]any{"projectId": projectID, "title": title},
		}); err != nil {
			return err
		}
		if !opts.Start {
			return nil
		}
		r, err := s.startInTx(ctx, q, task, userID, opts.StepByStep)
		run = &r
		return err
	})
	return task, run, err
}

// StartWorkflow starts a new workflow run for a task that has none active.
func (s *Service) StartWorkflow(ctx context.Context, taskID uuid.UUID, userID *uuid.UUID, stepByStep bool) (dbgen.WorkflowRun, error) {
	var run dbgen.WorkflowRun
	err := s.db.InTx(ctx, func(q *dbgen.Queries) error {
		task, err := q.LockTask(ctx, taskID)
		if db.IsNotFound(err) {
			return apperr.NotFound("task not found")
		}
		if err != nil {
			return err
		}
		if err := projects.LockActive(ctx, q, task.ProjectID); err != nil {
			return err
		}
		run, err = s.startInTx(ctx, q, task, userID, stepByStep)
		return err
	})
	return run, err
}

func (s *Service) startInTx(ctx context.Context, q *dbgen.Queries, task dbgen.Task, userID *uuid.UUID, stepByStep bool) (dbgen.WorkflowRun, error) {
	if latest, err := q.GetLatestWorkflowRunForTask(ctx, task.ID); err == nil && IsActiveRun(latest.Status) {
		return dbgen.WorkflowRun{}, apperr.Conflict("this task already has an active workflow")
	} else if err != nil && !db.IsNotFound(err) {
		return dbgen.WorkflowRun{}, err
	}
	tw, err := s.planFor(ctx, q, task, stepByStep)
	if err != nil {
		return dbgen.WorkflowRun{}, err
	}
	if len(tw.Issues) > 0 {
		return dbgen.WorkflowRun{}, issuesError(tw.Issues)
	}
	cfg, err := json.Marshal(tw.Plan)
	if err != nil {
		return dbgen.WorkflowRun{}, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return dbgen.WorkflowRun{}, err
	}
	run, err := q.CreateWorkflowRun(ctx, dbgen.CreateWorkflowRunParams{
		ID: id, TaskID: task.ID, WorkflowType: PipelineType, Config: cfg, CreatedBy: userID,
	})
	if db.IsUniqueViolation(err) {
		return dbgen.WorkflowRun{}, apperr.Conflict("this task already has an active workflow")
	}
	if err != nil {
		return dbgen.WorkflowRun{}, err
	}
	if err := q.SetTaskStatus(ctx, dbgen.SetTaskStatusParams{ID: task.ID, Status: "in_progress"}); err != nil {
		return dbgen.WorkflowRun{}, err
	}
	if err := moveWithWorkflow(ctx, q, run); err != nil { // card → In progress
		return dbgen.WorkflowRun{}, err
	}
	if err := recordEvent(ctx, q, id, "workflow.created", map[string]any{
		"taskId": task.ID, "type": PipelineType, "template": tw.Plan.TemplateName, "stepByStep": stepByStep,
	}); err != nil {
		return dbgen.WorkflowRun{}, err
	}
	if err := enqueueCommand(ctx, q, id, CmdStart, nil); err != nil {
		return dbgen.WorkflowRun{}, err
	}
	return run, audit.Record(ctx, q, audit.Entry{
		Action: "workflow.start", EntityType: "workflow_run", EntityID: audit.Ptr(id),
		Payload: map[string]any{"taskId": task.ID},
	})
}

func (s *Service) GetTask(ctx context.Context, id uuid.UUID) (Detail, error) {
	task, err := s.db.GetTask(ctx, id)
	if db.IsNotFound(err) {
		return Detail{}, apperr.NotFound("task not found")
	}
	if err != nil {
		return Detail{}, err
	}
	d := Detail{Task: task, Steps: []dbgen.WorkflowStep{}, Artifacts: []dbgen.Artifact{}, Approvals: []dbgen.Approval{}}
	run, err := s.db.GetLatestWorkflowRunForTask(ctx, id)
	if err != nil && !db.IsNotFound(err) {
		return Detail{}, err
	}
	if err == nil {
		d.Workflow = &run
		if d.Steps, err = s.db.ListWorkflowSteps(ctx, run.ID); err != nil {
			return Detail{}, err
		}
	}
	if p, ok := ParsePlan(run.Config); ok && d.Workflow != nil && IsActiveRun(run.Status) {
		d.Plan = p
	} else {
		tw, err := s.planFor(ctx, s.db.Queries, task, false)
		if err != nil && !isConflict(err) {
			return Detail{}, err
		}
		d.Plan, d.Issues = tw.Plan, tw.Issues
		if p, ok := ParsePlan(run.Config); ok && d.Workflow != nil {
			d.Plan = p // finished runs show what actually ran
		}
	}
	all, err := s.db.ListArtifacts(ctx, dbgen.ListArtifactsParams{TaskID: id})
	if err != nil {
		return Detail{}, err
	}
	seen := map[string]bool{}
	for _, a := range all { // ordered by kind, version DESC
		if !seen[a.Kind] {
			seen[a.Kind] = true
			d.Artifacts = append(d.Artifacts, a)
		}
	}
	rows, err := s.db.ListApprovals(ctx, dbgen.ListApprovalsParams{TaskID: &id, RowLimit: 200})
	if err != nil {
		return Detail{}, err
	}
	for _, r := range rows {
		d.Approvals = append(d.Approvals, r.Approval)
	}
	return d, nil
}

func (s *Service) ListTasks(ctx context.Context, projectID *uuid.UUID, status *string) ([]dbgen.ListTasksRow, error) {
	return s.db.ListTasks(ctx, dbgen.ListTasksParams{ProjectID: projectID, Status: status, RowLimit: 200})
}

// CancelTask asks the task's active workflow to stop. A task without an active
// workflow is cancelled directly.
func (s *Service) CancelTask(ctx context.Context, id uuid.UUID) (dbgen.Task, error) {
	var task dbgen.Task
	var activeRun *uuid.UUID
	err := s.db.InTx(ctx, func(q *dbgen.Queries) error {
		var err error
		task, err = q.LockTask(ctx, id)
		if db.IsNotFound(err) {
			return apperr.NotFound("task not found")
		}
		if err != nil {
			return err
		}
		switch task.Status {
		case "completed", "failed", "cancelled":
			return apperr.Conflict(fmt.Sprintf("task already %s", task.Status))
		}
		run, err := q.GetLatestWorkflowRunForTask(ctx, id)
		if err != nil && !db.IsNotFound(err) {
			return err
		}
		if err == nil && IsActiveRun(run.Status) {
			if _, err := q.LockWorkflowRun(ctx, run.ID); err != nil {
				return err
			}
			if err := recordEvent(ctx, q, run.ID, "workflow.cancel_requested", nil); err != nil {
				return err
			}
			if err := enqueueCommand(ctx, q, run.ID, CmdCancel, nil); err != nil {
				return err
			}
			activeRun = &run.ID
		} else {
			if err := q.SetTaskStatus(ctx, dbgen.SetTaskStatusParams{ID: id, Status: "cancelled"}); err != nil {
				return err
			}
			task.Status = "cancelled"
		}
		return audit.Record(ctx, q, audit.Entry{Action: "task.cancel", EntityType: "task", EntityID: audit.Ptr(id)})
	})
	if err == nil && activeRun != nil && s.Runs != nil {
		// The workflow cancel goes through the outbox; stopping its agents here
		// avoids waiting for a heartbeat to carry the cancellation.
		_ = s.Runs.CancelWorkflowRuns(ctx, *activeRun)
	}
	return task, err
}

func (s *Service) GetWorkflow(ctx context.Context, id uuid.UUID) (dbgen.WorkflowRun, []dbgen.WorkflowStep, error) {
	run, err := s.db.GetWorkflowRun(ctx, id)
	if db.IsNotFound(err) {
		return run, nil, apperr.NotFound("workflow not found")
	}
	if err != nil {
		return run, nil, err
	}
	steps, err := s.db.ListWorkflowSteps(ctx, id)
	return run, steps, err
}

func (s *Service) ListWorkflowEvents(ctx context.Context, id uuid.UUID, afterSeq int32) ([]dbgen.WorkflowEvent, error) {
	if _, err := s.db.GetWorkflowRun(ctx, id); db.IsNotFound(err) {
		return nil, apperr.NotFound("workflow not found")
	} else if err != nil {
		return nil, err
	}
	return s.db.ListWorkflowEvents(ctx, dbgen.ListWorkflowEventsParams{WorkflowRunID: id, Seq: afterSeq})
}

// RetryWorkflow resumes a workflow that stopped on a failed stage.
func (s *Service) RetryWorkflow(ctx context.Context, id uuid.UUID) (dbgen.WorkflowRun, error) {
	var run dbgen.WorkflowRun
	err := s.db.InTx(ctx, func(q *dbgen.Queries) error {
		var err error
		run, err = q.LockWorkflowRun(ctx, id)
		if db.IsNotFound(err) {
			return apperr.NotFound("workflow not found")
		}
		if err != nil {
			return err
		}
		if run.Status != RunAwaitingRetry {
			return apperr.Conflict(fmt.Sprintf("workflow is %s; only a workflow awaiting retry can be retried", run.Status))
		}
		if err := recordEvent(ctx, q, id, "workflow.retry_requested", map[string]any{"stage": run.CurrentStage}); err != nil {
			return err
		}
		if err := enqueueCommand(ctx, q, id, CmdRetry, nil); err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{Action: "workflow.retry", EntityType: "workflow_run", EntityID: audit.Ptr(id)})
	})
	return run, err
}

func (s *Service) GetArtifact(ctx context.Context, id uuid.UUID) (dbgen.Artifact, error) {
	a, err := s.db.GetArtifact(ctx, id)
	if db.IsNotFound(err) {
		return a, apperr.NotFound("artifact not found")
	}
	return a, err
}

func (s *Service) ListArtifacts(ctx context.Context, taskID uuid.UUID, kind *string) ([]dbgen.Artifact, error) {
	if _, err := s.db.GetTask(ctx, taskID); db.IsNotFound(err) {
		return nil, apperr.NotFound("task not found")
	} else if err != nil {
		return nil, err
	}
	return s.db.ListArtifacts(ctx, dbgen.ListArtifactsParams{TaskID: taskID, Kind: kind})
}

func (s *Service) GetApproval(ctx context.Context, id uuid.UUID) (dbgen.Approval, error) {
	a, err := s.db.GetApproval(ctx, id)
	if db.IsNotFound(err) {
		return a, apperr.NotFound("approval not found")
	}
	return a, err
}

func (s *Service) ListApprovals(ctx context.Context, status *string, taskID *uuid.UUID) ([]dbgen.ListApprovalsRow, error) {
	return s.db.ListApprovals(ctx, dbgen.ListApprovalsParams{Status: status, TaskID: taskID, RowLimit: 200})
}

// ErrStaleApproval means the reviewer did not see the current artifact versions.
var ErrStaleApproval = errors.New("stale approval")

// Decide approves or rejects an approval. reviewedHash must equal the approval's
// combined hash, and every pinned artifact must still be the latest version of its
// kind with unchanged content — otherwise the decision is refused (409), so an
// approval only ever applies to exactly what was reviewed.
func (s *Service) Decide(ctx context.Context, id uuid.UUID, approve bool, reviewedHash, comment string, userID *uuid.UUID) (dbgen.Approval, error) {
	comment = strings.TrimSpace(comment)
	if !approve && len(comment) < 3 {
		return dbgen.Approval{}, apperr.Validation(apperr.FieldError{Field: "comment", Message: "explain what should change (at least 3 characters)"})
	}
	if len(comment) > 10000 {
		return dbgen.Approval{}, apperr.Validation(apperr.FieldError{Field: "comment", Message: "must be at most 10000 characters"})
	}
	head, err := s.db.GetApproval(ctx, id)
	if db.IsNotFound(err) {
		return dbgen.Approval{}, apperr.NotFound("approval not found")
	}
	if err != nil {
		return dbgen.Approval{}, err
	}
	var out dbgen.Approval
	err = s.db.InTx(ctx, func(q *dbgen.Queries) error {
		// Lock order everywhere: workflow run, then approval.
		if _, err := q.LockWorkflowRun(ctx, head.WorkflowRunID); err != nil {
			return err
		}
		ap, err := q.LockApproval(ctx, id)
		if err != nil {
			return err
		}
		if ap.Status != "pending" {
			return apperr.Conflict(fmt.Sprintf("approval is already %s", ap.Status))
		}
		if reviewedHash != ap.CombinedHash {
			return apperr.Conflict("the artifacts changed since you reviewed them; reload and review again").WithCause(ErrStaleApproval)
		}
		if err := verifyPinned(ctx, q, ap); err != nil {
			return err
		}
		status, action := "rejected", "approval.reject"
		if approve {
			status, action = "approved", "approval.approve"
		}
		var c *string
		if comment != "" {
			c = &comment
		}
		if out, err = q.DecideApproval(ctx, dbgen.DecideApprovalParams{ID: id, Status: status, Comment: c, DecidedBy: userID}); err != nil {
			return err
		}
		if err := recordEvent(ctx, q, ap.WorkflowRunID, "approval."+status, map[string]any{
			"approvalId": id, "gate": ap.Gate, "round": ap.Round, "comment": comment,
		}); err != nil {
			return err
		}
		if err := enqueueCommand(ctx, q, ap.WorkflowRunID, CmdDecision, map[string]any{"approvalId": id}); err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{
			Action: action, EntityType: "approval", EntityID: audit.Ptr(id),
			Payload: map[string]any{"gate": ap.Gate, "round": ap.Round, "combinedHash": ap.CombinedHash, "comment": comment},
		})
	})
	return out, err
}

// verifyPinned checks every artifact an approval pins is still current and intact.
func verifyPinned(ctx context.Context, q *dbgen.Queries, ap dbgen.Approval) error {
	var refs []ArtifactRef
	if err := json.Unmarshal(ap.Artifacts, &refs); err != nil {
		return fmt.Errorf("decode approval artifacts: %w", err)
	}
	for _, r := range refs {
		latest, err := q.GetLatestArtifact(ctx, dbgen.GetLatestArtifactParams{TaskID: ap.TaskID, Kind: r.Kind})
		if err != nil {
			return fmt.Errorf("load %s: %w", r.Kind, err)
		}
		if latest.ID.String() != r.ArtifactID {
			return apperr.Conflict(fmt.Sprintf("%s has a newer version (v%d); this approval no longer applies", r.Kind, latest.Version)).WithCause(ErrStaleApproval)
		}
		h, err := HashContent(latest.Content)
		if err != nil {
			return err
		}
		if h != r.Hash {
			return apperr.Conflict(fmt.Sprintf("%s v%d content does not match its reviewed hash", r.Kind, r.Version)).WithCause(ErrStaleApproval)
		}
	}
	return nil
}

// recordEvent appends a workflow event; the caller must hold the workflow run's
// row lock so sequence numbers stay gap-free. pg_notify fires on commit.
func recordEvent(ctx context.Context, q *dbgen.Queries, runID uuid.UUID, typ string, payload any) error {
	b := []byte("{}")
	if payload != nil {
		var err error
		if b, err = json.Marshal(payload); err != nil {
			return fmt.Errorf("marshal workflow event: %w", err)
		}
	}
	ev, err := q.InsertWorkflowEvent(ctx, dbgen.InsertWorkflowEventParams{WorkflowRunID: runID, Type: typ, Payload: b})
	if err != nil {
		return fmt.Errorf("insert workflow event: %w", err)
	}
	return q.NotifyWorkflowEvent(ctx, fmt.Sprint(ev.ID))
}

// enqueueCommand writes an outbox command in the caller's transaction.
func enqueueCommand(ctx context.Context, q *dbgen.Queries, runID uuid.UUID, kind string, payload any) error {
	b := []byte("{}")
	if payload != nil {
		var err error
		if b, err = json.Marshal(payload); err != nil {
			return err
		}
	}
	id, err := uuid.NewV7()
	if err != nil {
		return err
	}
	if err := q.InsertWorkflowCommand(ctx, dbgen.InsertWorkflowCommandParams{ID: id, WorkflowRunID: runID, Kind: kind, Payload: b}); err != nil {
		return err
	}
	return q.NotifyWorkflowCommand(ctx)
}

func isConflict(err error) bool {
	e, ok := apperr.As(err)
	return ok && e.Status == 409
}

// ---- board ----

// CardPatch edits a task card; nil fields are kept.
type CardPatch struct {
	Title       *string
	Description *string
	Priority    *string
	Labels      []string
	Column      *uuid.UUID
	Position    *float64
}

var priorities = []string{"none", "low", "medium", "high", "urgent"}

func strPtrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// placeCard validates and applies a card patch; a column change without a
// position puts the card at the bottom of its new column.
func (s *Service) placeCard(ctx context.Context, q *dbgen.Queries, task *dbgen.Task, p CardPatch) error {
	var errs []apperr.FieldError
	if p.Title != nil {
		t := strings.TrimSpace(*p.Title)
		if len([]rune(t)) < 3 || len(t) > 200 {
			errs = append(errs, apperr.FieldError{Field: "title", Message: "must be 3-200 characters"})
		}
		p.Title = &t
	}
	if p.Description != nil && len(*p.Description) > 20000 {
		errs = append(errs, apperr.FieldError{Field: "description", Message: "must be at most 20000 characters"})
	}
	if p.Priority != nil && !slices.Contains(priorities, *p.Priority) {
		errs = append(errs, apperr.FieldError{Field: "priority", Message: "none, low, medium, high or urgent"})
	}
	var labels []string
	for _, l := range p.Labels {
		l = strings.TrimSpace(l)
		switch {
		case l == "" || slices.Contains(labels, l):
		case len([]rune(l)) > 40:
			errs = append(errs, apperr.FieldError{Field: "labels", Message: fmt.Sprintf("label %q is longer than 40 characters", l)})
		default:
			labels = append(labels, l)
		}
	}
	if len(labels) > 20 {
		errs = append(errs, apperr.FieldError{Field: "labels", Message: "at most 20 labels"})
	}
	if len(errs) > 0 {
		return apperr.Validation(errs...)
	}
	if p.Column != nil {
		if _, err := projectColumn(ctx, q, task.ProjectID, *p.Column, "column"); err != nil {
			return err
		}
	}
	if p.Column != nil && *p.Column != task.ColumnID && p.Position == nil {
		pos, err := q.NextBoardPosition(ctx, *p.Column)
		if err != nil {
			return err
		}
		p.Position = &pos
	}
	params := dbgen.UpdateTaskCardParams{ID: task.ID, Title: p.Title, Description: p.Description, Priority: p.Priority,
		ColumnID: p.Column, Position: p.Position}
	if p.Labels != nil {
		if labels == nil {
			labels = []string{}
		}
		params.Labels = labels
	}
	updated, err := q.UpdateTaskCard(ctx, params)
	if err != nil {
		return err
	}
	*task = updated
	return nil
}

// UpdateCard edits or moves a task card.
func (s *Service) UpdateCard(ctx context.Context, id uuid.UUID, p CardPatch) (dbgen.Task, error) {
	var task dbgen.Task
	err := s.db.InTx(ctx, func(q *dbgen.Queries) error {
		var err error
		task, err = q.LockTask(ctx, id)
		if db.IsNotFound(err) {
			return apperr.NotFound("task not found")
		}
		if err != nil {
			return err
		}
		from := task.ColumnID
		if err := s.placeCard(ctx, q, &task, p); err != nil {
			return err
		}
		payload := map[string]any{}
		if p.Column != nil && *p.Column != from {
			payload["from"], payload["to"] = from, *p.Column
		}
		if p.Title != nil || p.Description != nil || p.Priority != nil || p.Labels != nil {
			payload["edited"] = true
		}
		if len(payload) == 0 {
			return nil // reorder within a column: not audit-worthy
		}
		return audit.Record(ctx, q, audit.Entry{Action: "task.update", EntityType: "task", EntityID: audit.Ptr(id), Payload: payload})
	})
	return task, err
}

// Board lists a project's cards with their latest workflow state.
func (s *Service) Board(ctx context.Context, projectID uuid.UUID) ([]dbgen.ListBoardTasksRow, error) {
	if _, err := s.db.GetProject(ctx, projectID); db.IsNotFound(err) {
		return nil, apperr.NotFound("project not found")
	} else if err != nil {
		return nil, err
	}
	return s.db.ListBoardTasks(ctx, projectID)
}

// roleFor is the column role a workflow state moves its card to ("" = leave it).
func roleFor(status, destination string) string {
	switch status {
	case RunPending, RunRunning:
		return RoleInProgress
	case RunAwaitingApproval:
		return RoleReview
	case RunCompleted:
		if destination == "" {
			return RoleDone
		}
		return destination
	}
	return "" // awaiting retry, failed, cancelled: the card stays where it is
}

// SetWorkflowConfig applies panel-edited workflow defaults.
func (s *Service) SetWorkflowConfig(c WorkflowConfig) {
	s.cfgMu.Lock()
	s.cfg = c
	s.cfgMu.Unlock()
}

func (s *Service) maxRevisions() int {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return s.cfg.MaxRevisions
}

// SetBoardDefaults applies the panel-edited column set for new projects.
func (s *Service) SetBoardDefaults(b BoardDefaults) {
	s.cfgMu.Lock()
	s.board = b
	s.cfgMu.Unlock()
}

// BoardDefaultColumns is the column set new projects start with.
func (s *Service) BoardDefaultColumns() []ColumnDef {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return append([]ColumnDef(nil), s.board.Columns...)
}
