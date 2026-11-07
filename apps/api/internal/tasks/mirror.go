package tasks

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"wise/apps/api/internal/db"
	"wise/apps/api/internal/db/dbgen"
)

// This file holds the writes made on behalf of the Temporal workflow (through
// activities). Each is idempotent, because Temporal may retry an activity after
// its transaction committed.

// StartInfo is what the orchestrator needs to start a workflow run.
type StartInfo struct {
	Run     dbgen.WorkflowRun
	Task    dbgen.Task
	Config  WorkflowConfig
	Project uuid.UUID
}

func (s *Service) StartInfo(ctx context.Context, runID uuid.UUID) (StartInfo, error) {
	run, err := s.db.GetWorkflowRun(ctx, runID)
	if err != nil {
		return StartInfo{}, fmt.Errorf("load workflow run: %w", err)
	}
	task, err := s.db.GetTask(ctx, run.TaskID)
	if err != nil {
		return StartInfo{}, fmt.Errorf("load task: %w", err)
	}
	cfg := DefaultWorkflowConfig()
	if err := json.Unmarshal(run.Config, &cfg); err != nil {
		return StartInfo{}, fmt.Errorf("decode workflow config: %w", err)
	}
	return StartInfo{Run: run, Task: task, Config: cfg, Project: task.ProjectID}, nil
}

func (s *Service) SetTemporalRunID(ctx context.Context, runID uuid.UUID, temporalRunID string) error {
	return s.db.SetWorkflowTemporalRunID(ctx, dbgen.SetWorkflowTemporalRunIDParams{ID: runID, TemporalRunID: &temporalRunID})
}

// MarkStarted moves a pending run to running. Any other status is left alone,
// because the workflow may already have moved past it (e.g. awaiting_retry).
func (s *Service) MarkStarted(ctx context.Context, runID uuid.UUID) error {
	return s.db.InTx(ctx, func(q *dbgen.Queries) error {
		cur, err := q.LockWorkflowRun(ctx, runID)
		if err != nil || cur.Status != RunPending {
			return err
		}
		run, err := q.UpdateWorkflowRunState(ctx, dbgen.UpdateWorkflowRunStateParams{ID: runID, Status: RunRunning})
		if err != nil {
			return err
		}
		if err := q.SetTaskStatus(ctx, dbgen.SetTaskStatusParams{ID: run.TaskID, Status: taskStatusFor(RunRunning)}); err != nil {
			return err
		}
		return recordEvent(ctx, q, runID, "workflow.status", map[string]any{"status": RunRunning, "stage": run.CurrentStage})
	})
}

// SetState updates the workflow run status (and its task) and records an event
// when the status or stage actually changed.
func (s *Service) SetState(ctx context.Context, runID uuid.UUID, status, stage string, errMsg *string) error {
	return s.db.InTx(ctx, func(q *dbgen.Queries) error {
		cur, err := q.LockWorkflowRun(ctx, runID)
		if err != nil {
			return err
		}
		if !IsActiveRun(cur.Status) && cur.Status != status {
			return nil // terminal states are final
		}
		var st *string
		if stage != "" {
			st = &stage
		}
		run, err := q.UpdateWorkflowRunState(ctx, dbgen.UpdateWorkflowRunStateParams{ID: runID, Status: status, CurrentStage: st, Error: errMsg})
		if err != nil {
			return err
		}
		if err := q.SetTaskStatus(ctx, dbgen.SetTaskStatusParams{ID: run.TaskID, Status: taskStatusFor(status)}); err != nil {
			return err
		}
		if cur.Status == run.Status && ptrEq(cur.CurrentStage, run.CurrentStage) && ptrEq(cur.Error, run.Error) {
			return nil
		}
		payload := map[string]any{"status": status, "stage": run.CurrentStage}
		if errMsg != nil {
			payload["error"] = *errMsg
		}
		return recordEvent(ctx, q, runID, "workflow.status", payload)
	})
}

// StepUpdate is one stage attempt's state change.
type StepUpdate struct {
	Stage      string
	Attempt    int32
	Status     string // running|awaiting_approval|succeeded|failed|cancelled|skipped
	AgentRunID *uuid.UUID
	ArtifactID *uuid.UUID
	Error      *string
}

func (s *Service) RecordStep(ctx context.Context, runID uuid.UUID, u StepUpdate) error {
	id, err := uuid.NewV7()
	if err != nil {
		return err
	}
	return s.db.InTx(ctx, func(q *dbgen.Queries) error {
		if _, err := q.LockWorkflowRun(ctx, runID); err != nil {
			return err
		}
		if _, err := q.UpsertWorkflowStep(ctx, dbgen.UpsertWorkflowStepParams{
			ID: id, WorkflowRunID: runID, Stage: u.Stage, Attempt: u.Attempt, Status: u.Status,
			AgentRunID: u.AgentRunID, ArtifactID: u.ArtifactID, Error: u.Error,
		}); err != nil {
			return err
		}
		payload := map[string]any{"stage": u.Stage, "attempt": u.Attempt, "status": u.Status}
		if u.AgentRunID != nil {
			payload["agentRunId"] = u.AgentRunID
		}
		if u.ArtifactID != nil {
			payload["artifactId"] = u.ArtifactID
		}
		if u.Error != nil {
			payload["error"] = *u.Error
		}
		return recordEvent(ctx, q, runID, "stage."+u.Status, payload)
	})
}

// SaveArtifact stores a new version of an artifact kind and supersedes any open
// approval that pinned an older version. It is idempotent for retried
// activities: an agent run produces at most one artifact, and a workflow run at
// most one final report.
func (s *Service) SaveArtifact(ctx context.Context, runID, taskID uuid.UUID, kind string, content json.RawMessage, agentRunID *uuid.UUID) (dbgen.Artifact, error) {
	hash, err := HashContent(content)
	if err != nil {
		return dbgen.Artifact{}, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return dbgen.Artifact{}, err
	}
	var art dbgen.Artifact
	err = s.db.InTx(ctx, func(q *dbgen.Queries) error {
		if _, err := q.LockWorkflowRun(ctx, runID); err != nil {
			return err
		}
		var existing dbgen.Artifact
		err := db.ErrNoRows
		switch {
		case agentRunID != nil:
			existing, err = q.GetArtifactByAgentRun(ctx, agentRunID)
		case kind == KindFinalReport:
			existing, err = q.GetWorkflowArtifactOfKind(ctx, dbgen.GetWorkflowArtifactOfKindParams{WorkflowRunID: &runID, Kind: kind})
		}
		if err == nil && existing.Kind == kind {
			art = existing
			return nil
		}
		if err != nil && !db.IsNotFound(err) {
			return err
		}
		if art, err = q.InsertArtifact(ctx, dbgen.InsertArtifactParams{
			ID: id, TaskID: taskID, WorkflowRunID: &runID, Kind: kind, Content: content, ContentHash: hash, AgentRunID: agentRunID,
		}); err != nil {
			return err
		}
		superseded, err := q.SupersedeApprovalsForArtifactKind(ctx, dbgen.SupersedeApprovalsForArtifactKindParams{TaskID: taskID, Kind: kind})
		if err != nil {
			return err
		}
		for _, ap := range superseded {
			if err := recordEvent(ctx, q, runID, "approval.superseded", map[string]any{
				"approvalId": ap.ID, "gate": ap.Gate, "round": ap.Round, "reason": fmt.Sprintf("new %s version", kind),
			}); err != nil {
				return err
			}
		}
		return recordEvent(ctx, q, runID, "artifact.created", map[string]any{
			"artifactId": art.ID, "kind": kind, "version": art.Version, "hash": hash,
		})
	})
	return art, err
}

// CreateApproval opens an approval pinning the latest version of each artifact
// the gate covers. If an identical pending approval exists (activity retry), it
// is returned instead.
func (s *Service) CreateApproval(ctx context.Context, runID, taskID uuid.UUID, gate string, notes []string) (dbgen.Approval, error) {
	kinds, ok := GateArtifacts[gate]
	if !ok {
		return dbgen.Approval{}, fmt.Errorf("unknown gate %q", gate)
	}
	id, err := uuid.NewV7()
	if err != nil {
		return dbgen.Approval{}, err
	}
	if notes == nil {
		notes = []string{}
	}
	notesJSON, err := json.Marshal(notes)
	if err != nil {
		return dbgen.Approval{}, err
	}
	var ap dbgen.Approval
	err = s.db.InTx(ctx, func(q *dbgen.Queries) error {
		if _, err := q.LockWorkflowRun(ctx, runID); err != nil {
			return err
		}
		refs := make([]ArtifactRef, 0, len(kinds))
		for _, k := range kinds {
			a, err := q.GetLatestArtifact(ctx, dbgen.GetLatestArtifactParams{TaskID: taskID, Kind: k})
			if err != nil {
				return fmt.Errorf("gate %s needs a %s artifact: %w", gate, k, err)
			}
			refs = append(refs, ArtifactRef{ArtifactID: a.ID.String(), Kind: k, Version: a.Version, Hash: a.ContentHash})
		}
		combined := CombinedHash(refs)
		pending, err := q.ListApprovals(ctx, dbgen.ListApprovalsParams{Status: ptr("pending"), TaskID: &taskID, RowLimit: 50})
		if err != nil {
			return err
		}
		for _, p := range pending {
			if p.Approval.WorkflowRunID == runID && p.Approval.Gate == gate && p.Approval.CombinedHash == combined {
				ap = p.Approval
				return nil
			}
		}
		refsJSON, err := json.Marshal(refs)
		if err != nil {
			return err
		}
		if ap, err = q.CreateApproval(ctx, dbgen.CreateApprovalParams{
			ID: id, WorkflowRunID: runID, TaskID: taskID, Gate: gate, Artifacts: refsJSON, CombinedHash: combined, Notes: notesJSON,
		}); err != nil {
			return err
		}
		return recordEvent(ctx, q, runID, "approval.requested", map[string]any{
			"approvalId": ap.ID, "gate": gate, "round": ap.Round, "combinedHash": combined, "notes": notes,
		})
	})
	return ap, err
}

// Decision is what the workflow learns about an approval (the database is the source of truth).
type Decision struct {
	Status  string `json:"status"`
	Comment string `json:"comment"`
}

// ResolveApproval reads an approval's current decision and, once decided,
// marks it consumed so it is never acted on twice.
func (s *Service) ResolveApproval(ctx context.Context, id uuid.UUID) (Decision, error) {
	ap, err := s.db.GetApproval(ctx, id)
	if err != nil {
		return Decision{}, err
	}
	d := Decision{Status: ap.Status}
	if ap.Comment != nil {
		d.Comment = *ap.Comment
	}
	if ap.Status != "pending" {
		if err := s.db.ConsumeApproval(ctx, id); err != nil {
			return Decision{}, err
		}
	}
	return d, nil
}

// MarkCancelled finalizes a cancelled workflow: open steps and approvals are
// cancelled, then the run and its task.
func (s *Service) MarkCancelled(ctx context.Context, runID uuid.UUID, reason string) error {
	return s.db.InTx(ctx, func(q *dbgen.Queries) error {
		cur, err := q.LockWorkflowRun(ctx, runID)
		if err != nil {
			return err
		}
		if !IsActiveRun(cur.Status) {
			return nil
		}
		if err := q.CancelOpenWorkflowSteps(ctx, runID); err != nil {
			return err
		}
		cancelled, err := q.CancelPendingApprovals(ctx, runID)
		if err != nil {
			return err
		}
		for _, ap := range cancelled {
			if err := recordEvent(ctx, q, runID, "approval.cancelled", map[string]any{"approvalId": ap.ID, "gate": ap.Gate}); err != nil {
				return err
			}
		}
		run, err := q.UpdateWorkflowRunState(ctx, dbgen.UpdateWorkflowRunStateParams{ID: runID, Status: RunCancelled, Error: &reason})
		if err != nil {
			return err
		}
		if err := q.SetTaskStatus(ctx, dbgen.SetTaskStatusParams{ID: run.TaskID, Status: "cancelled"}); err != nil {
			return err
		}
		return recordEvent(ctx, q, runID, "workflow.status", map[string]any{"status": RunCancelled, "stage": run.CurrentStage, "error": reason})
	})
}

// LatestArtifacts returns the latest content of each requested kind (missing kinds are omitted).
func (s *Service) LatestArtifacts(ctx context.Context, taskID uuid.UUID, kinds ...string) (map[string]dbgen.Artifact, error) {
	out := map[string]dbgen.Artifact{}
	for _, k := range kinds {
		a, err := s.db.GetLatestArtifact(ctx, dbgen.GetLatestArtifactParams{TaskID: taskID, Kind: k})
		if db.IsNotFound(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out[k] = a
	}
	return out, nil
}

func ptr[T any](v T) *T { return &v }

func ptrEq[T comparable](a, b *T) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
