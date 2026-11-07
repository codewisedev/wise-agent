package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

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
	db  *db.DB
	cfg WorkflowConfig
	// Runs, when set, lets CancelTask stop running agents immediately instead of
	// waiting for the workflow's activity to notice the cancellation.
	Runs RunCanceller
}

func NewService(d *db.DB, cfg WorkflowConfig) *Service { return &Service{db: d, cfg: cfg} }

// Detail is a task with its latest workflow run and everything the dashboard shows.
type Detail struct {
	Task      dbgen.Task
	Workflow  *dbgen.WorkflowRun
	Steps     []dbgen.WorkflowStep
	Artifacts []dbgen.Artifact // latest version of each kind
	Approvals []dbgen.Approval
}

// CreateTask creates a task and, when start is true, its first workflow run.
func (s *Service) CreateTask(ctx context.Context, projectID uuid.UUID, title, description string, start bool, userID *uuid.UUID) (dbgen.Task, *dbgen.WorkflowRun, error) {
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
		if task, err = q.CreateTask(ctx, dbgen.CreateTaskParams{
			ID: id, ProjectID: projectID, Title: title, Description: strings.TrimSpace(description), CreatedBy: userID,
		}); err != nil {
			return err
		}
		if err := audit.Record(ctx, q, audit.Entry{
			Action: "task.create", EntityType: "task", EntityID: audit.Ptr(id),
			Payload: map[string]any{"projectId": projectID, "title": title},
		}); err != nil {
			return err
		}
		if !start {
			return nil
		}
		r, err := s.startInTx(ctx, q, task, userID)
		run = &r
		return err
	})
	return task, run, err
}

// StartWorkflow starts a new workflow run for a task that has none active.
func (s *Service) StartWorkflow(ctx context.Context, taskID uuid.UUID, userID *uuid.UUID) (dbgen.WorkflowRun, error) {
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
		run, err = s.startInTx(ctx, q, task, userID)
		return err
	})
	return run, err
}

func (s *Service) startInTx(ctx context.Context, q *dbgen.Queries, task dbgen.Task, userID *uuid.UUID) (dbgen.WorkflowRun, error) {
	if latest, err := q.GetLatestWorkflowRunForTask(ctx, task.ID); err == nil && IsActiveRun(latest.Status) {
		return dbgen.WorkflowRun{}, apperr.Conflict("this task already has an active workflow")
	} else if err != nil && !db.IsNotFound(err) {
		return dbgen.WorkflowRun{}, err
	}
	cfg, err := json.Marshal(s.cfg)
	if err != nil {
		return dbgen.WorkflowRun{}, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return dbgen.WorkflowRun{}, err
	}
	run, err := q.CreateWorkflowRun(ctx, dbgen.CreateWorkflowRunParams{
		ID: id, TaskID: task.ID, WorkflowType: WorkflowType, Config: cfg, CreatedBy: userID,
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
	if err := recordEvent(ctx, q, id, "workflow.created", map[string]any{"taskId": task.ID, "type": WorkflowType}); err != nil {
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
