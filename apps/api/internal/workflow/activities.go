// Package workflow runs Wise's engineering pipeline on Temporal: the workflow
// definition, its activities, the outbox dispatcher and the orchestrator worker.
package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"

	"wise/apps/api/internal/agents"
	"wise/apps/api/internal/apperr"
	"wise/apps/api/internal/db/dbgen"
	"wise/apps/api/internal/tasks"
)

// ErrTypeAgentFailed marks a stage failure that needs a human retry, not an automatic one.
const ErrTypeAgentFailed = "AgentRunFailed"

// Activities are the workflow's only side effects. All are idempotent.
type Activities struct {
	Tasks  *tasks.Service
	Agents *agents.Service
	// Poll is how often RunStage checks the agent run (default 2s).
	Poll time.Duration
}

// StateUpdate is SetState's input.
type StateUpdate struct {
	WorkflowRunID uuid.UUID
	Status        string
	Stage         string
	Error         *string
}

func (a *Activities) SetState(ctx context.Context, u StateUpdate) error {
	return a.Tasks.SetState(ctx, u.WorkflowRunID, u.Status, u.Stage, u.Error)
}

// StepRecord is RecordStep's input.
type StepRecord struct {
	WorkflowRunID uuid.UUID
	Update        tasks.StepUpdate
}

func (a *Activities) RecordStep(ctx context.Context, r StepRecord) error {
	return a.Tasks.RecordStep(ctx, r.WorkflowRunID, r.Update)
}

// StageRequest runs one agent stage.
type StageRequest struct {
	WorkflowRunID uuid.UUID
	TaskID        uuid.UUID
	ProjectID     uuid.UUID
	Stage         string
	Agent         string
	Kind          string // artifact kind the agent produces
	Attempt       int32  // human retries of this stage (1-based)
	Revision      int    // 0 = first pass, n = after the n-th rejection
	Feedback      string // reviewer comment that triggered this revision
	Step          int32  // n-th execution of this stage in the workflow (numbers workflow_steps)
	CreatedBy     *uuid.UUID
}

// StageResult is what a finished stage produced.
type StageResult struct {
	AgentRunID uuid.UUID
	ArtifactID uuid.UUID
	Version    int32
	Notes      []string
}

// RunStage queues the stage's agent run (once, by idempotency key), waits for
// the runtime to finish it while heartbeating, and stores its output as a new
// artifact version. Cancelling the activity cancels the agent run.
func (a *Activities) RunStage(ctx context.Context, req StageRequest) (StageResult, error) {
	key := fmt.Sprintf("wf:%s:%s:a%d:r%d", req.WorkflowRunID, req.Stage, req.Attempt, req.Revision)
	// Read the step's live settings: its account and model may have been
	// changed while the workflow waited.
	step, err := a.Tasks.CurrentStep(ctx, req.WorkflowRunID, req.Stage)
	if err != nil {
		return StageResult{}, err
	}
	input, err := a.agentInput(ctx, req, step)
	if err != nil {
		return StageResult{}, err
	}
	opts := agents.RunOptions{
		ProjectID: &req.ProjectID, UserID: req.CreatedBy, WorkflowRunID: &req.WorkflowRunID, IdempotencyKey: key,
	}
	if step.Account != "" {
		opts.Model = &agents.ModelOverride{Account: step.Account, Model: step.Model}
	}
	run, err := a.Agents.Enqueue(ctx, req.Agent, input, opts)
	if err != nil {
		if e, ok := apperr.As(err); ok && e.Status < 500 {
			// Validation, disabled or unknown agent: retrying automatically cannot help.
			msg := e.Error()
			for _, f := range e.Fields {
				msg += fmt.Sprintf("; %s: %s", f.Field, f.Message)
			}
			return StageResult{}, a.failStage(ctx, req, nil, fmt.Sprintf("could not start %s: %s", req.Agent, msg))
		}
		return StageResult{}, err
	}
	if err := a.Tasks.RecordStep(ctx, req.WorkflowRunID, tasks.StepUpdate{
		Stage: req.Stage, Attempt: stepAttempt(req), Status: "running", AgentRunID: &run.ID,
	}); err != nil {
		return StageResult{}, err
	}

	poll := a.Poll
	if poll <= 0 {
		poll = 2 * time.Second
	}
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		cur, err := a.Agents.GetRun(ctx, run.ID)
		if err != nil && ctx.Err() == nil {
			return StageResult{}, err
		}
		if err == nil {
			activity.RecordHeartbeat(ctx, cur.Status)
			switch cur.Status {
			case dbgen.AgentRunStatusSucceeded:
				return a.finishStage(ctx, req, cur)
			case dbgen.AgentRunStatusFailed, dbgen.AgentRunStatusCancelled:
				msg := fmt.Sprintf("%s %s", req.Agent, cur.Status)
				if cur.Error != nil {
					msg += ": " + *cur.Error
				}
				return StageResult{}, a.failStage(ctx, req, &run.ID, msg)
			}
		}
		select {
		case <-ctx.Done():
			// Workflow cancelled (or heartbeat timed out): stop the agent too.
			cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			_, _ = a.Agents.CancelRun(cctx, run.ID)
			cancel()
			return StageResult{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

func stepAttempt(req StageRequest) int32 { return max(req.Step, 1) }

func (a *Activities) finishStage(ctx context.Context, req StageRequest, run dbgen.AgentRun) (StageResult, error) {
	if len(run.Output) == 0 {
		return StageResult{}, a.failStage(ctx, req, &run.ID, req.Agent+" finished without output")
	}
	art, err := a.Tasks.SaveArtifact(ctx, req.WorkflowRunID, req.TaskID, req.Kind, run.Output, &run.ID)
	if err != nil {
		return StageResult{}, err
	}
	if err := a.Tasks.RecordStep(ctx, req.WorkflowRunID, tasks.StepUpdate{
		Stage: req.Stage, Attempt: stepAttempt(req), Status: "succeeded", AgentRunID: &run.ID, ArtifactID: &art.ID,
	}); err != nil {
		return StageResult{}, err
	}
	return StageResult{AgentRunID: run.ID, ArtifactID: art.ID, Version: art.Version, Notes: stageNotes(req.Kind, run.Output)}, nil
}

// failStage records the failure and returns a non-retryable error; the workflow
// then waits for a human retry.
func (a *Activities) failStage(ctx context.Context, req StageRequest, runID *uuid.UUID, msg string) error {
	if err := a.Tasks.RecordStep(ctx, req.WorkflowRunID, tasks.StepUpdate{
		Stage: req.Stage, Attempt: stepAttempt(req), Status: "failed", AgentRunID: runID, Error: &msg,
	}); err != nil {
		return err // retryable: the failure must be recorded first
	}
	return temporal.NewNonRetryableApplicationError(msg, ErrTypeAgentFailed, nil)
}

// stageNotes surfaces facts the reviewer must see on the approval.
func stageNotes(kind string, output json.RawMessage) []string {
	if kind != tasks.KindArchitectureReview {
		return nil
	}
	var v struct {
		ScopeChange string   `json:"scope_change"`
		Concerns    []string `json:"concerns"`
	}
	if json.Unmarshal(output, &v) != nil {
		return nil
	}
	var notes []string
	if v.ScopeChange == "material" {
		notes = append(notes, "Architecture review flags a MATERIAL scope change — confirm the scope is still what you approved.")
	}
	for _, c := range v.Concerns {
		notes = append(notes, "Architect concern: "+c)
	}
	return notes
}

// agentInput builds an agent's input: the task as `request`, each consumed
// property from the latest artifact of its kind and, on a revision, the
// reviewer's feedback with the rejected output.
func (a *Activities) agentInput(ctx context.Context, req StageRequest, step tasks.PlanStep) (map[string]any, error) {
	info, err := a.Tasks.StartInfo(ctx, req.WorkflowRunID)
	if err != nil {
		return nil, err
	}
	request := info.Task.Title
	if info.Task.Description != "" {
		request += "\n\n" + info.Task.Description
	}
	kinds := []string{req.Kind}
	for _, k := range step.Consumes {
		kinds = append(kinds, k)
	}
	arts, err := a.Tasks.LatestArtifacts(ctx, req.TaskID, kinds...)
	if err != nil {
		return nil, err
	}
	content := func(kind string) (any, bool) {
		art, ok := arts[kind]
		if !ok {
			return nil, false
		}
		var v any
		if json.Unmarshal(art.Content, &v) != nil {
			return nil, false
		}
		return v, true
	}
	in := map[string]any{"request": request}
	for prop, kind := range step.Consumes {
		if v, ok := content(kind); ok {
			in[prop] = v
		}
	}
	if req.Revision > 0 && step.AcceptsFeedback {
		if req.Feedback != "" {
			in["feedback"] = req.Feedback
		}
		if v, ok := content(req.Kind); ok {
			in["previous_output"] = v
		}
	}
	return in, nil
}

// ApprovalRequest opens a gate.
type ApprovalRequest struct {
	WorkflowRunID uuid.UUID
	TaskID        uuid.UUID
	Gate          string   // the reviewed step's id
	Kinds         []string // artifact kinds pinned by the approval
	Notes         []string
}

// ApprovalInfo identifies the opened approval.
type ApprovalInfo struct {
	ID    uuid.UUID
	Round int32
}

func (a *Activities) CreateApproval(ctx context.Context, r ApprovalRequest) (ApprovalInfo, error) {
	ap, err := a.Tasks.CreateApproval(ctx, r.WorkflowRunID, r.TaskID, r.Gate, r.Kinds, r.Notes)
	if err != nil {
		return ApprovalInfo{}, err
	}
	return ApprovalInfo{ID: ap.ID, Round: ap.Round}, nil
}

func (a *Activities) ResolveApproval(ctx context.Context, id uuid.UUID) (tasks.Decision, error) {
	return a.Tasks.ResolveApproval(ctx, id)
}

// ReportRequest builds the final report.
type ReportRequest struct {
	WorkflowRunID uuid.UUID
	TaskID        uuid.UUID
}

func (a *Activities) BuildFinalReport(ctx context.Context, r ReportRequest) (uuid.UUID, error) {
	info, err := a.Tasks.StartInfo(ctx, r.WorkflowRunID)
	if err != nil {
		return uuid.Nil, err
	}
	var kinds []string
	for _, st := range info.Plan.Steps {
		if st.Enabled && st.Kind != "" {
			kinds = append(kinds, st.Kind)
		}
	}
	arts, err := a.Tasks.LatestArtifacts(ctx, r.TaskID, kinds...)
	if err != nil {
		return uuid.Nil, err
	}
	approvals, err := a.Tasks.ListApprovals(ctx, nil, &r.TaskID)
	if err != nil {
		return uuid.Nil, err
	}
	report := BuildReport(info.Task, info.Plan, arts, approvals, r.WorkflowRunID)
	raw, err := json.Marshal(report)
	if err != nil {
		return uuid.Nil, err
	}
	art, err := a.Tasks.SaveArtifact(ctx, r.WorkflowRunID, r.TaskID, tasks.KindFinalReport, raw, nil)
	if err != nil {
		return uuid.Nil, err
	}
	return art.ID, nil
}

// CancelRequest finalizes a cancelled workflow.
type CancelRequest struct {
	WorkflowRunID uuid.UUID
	Reason        string
}

func (a *Activities) MarkCancelled(ctx context.Context, r CancelRequest) error {
	return a.Tasks.MarkCancelled(ctx, r.WorkflowRunID, r.Reason)
}
