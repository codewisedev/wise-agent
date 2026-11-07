package workflow

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"wise/apps/api/internal/tasks"
)

// TaskQueue is the Temporal task queue of the engineering worker.
const TaskQueue = "wise-engineering"

// WorkflowID is the Temporal workflow ID of a Wise workflow run.
func WorkflowID(runID uuid.UUID) string { return "wf-" + runID.String() }

// Input starts an EngineeringWorkflow. Config is frozen at start.
type Input struct {
	WorkflowRunID uuid.UUID
	TaskID        uuid.UUID
	ProjectID     uuid.UUID
	CreatedBy     *uuid.UUID
	Config        tasks.WorkflowConfig
}

// Result is the workflow's return value.
type Result struct {
	ReportArtifactID uuid.UUID
}

// ApprovalSignal is the payload of the approval-decided signal.
type ApprovalSignal struct {
	ApprovalID uuid.UUID `json:"approvalId"`
}

// a is a typed nil used to reference activity methods by name.
var a *Activities

type engineering struct {
	in         Input
	approvalCh workflow.ReceiveChannel
	retryCh    workflow.ReceiveChannel
	notes      []string
	runs       map[string]int32 // executions per stage, numbering workflow_steps rows
}

// EngineeringWorkflow drives a task through analysis, requirements, scope
// approval, planning, architecture review, plan approval and the final report
// (implementation-plan.md §10). Every state change is mirrored to Postgres via
// activities; approvals and retries arrive as signals from the outbox dispatcher.
//
// Phase 5 adds workspace, implementation, quality and git stages after
// PLAN_APPROVAL; guard them with workflow.GetVersion so running workflows replay.
func EngineeringWorkflow(ctx workflow.Context, in Input) (Result, error) {
	w := &engineering{
		in:         in,
		approvalCh: workflow.GetSignalChannel(ctx, tasks.SignalApprovalDecided),
		retryCh:    workflow.GetSignalChannel(ctx, tasks.SignalRetry),
		runs:       map[string]int32{},
	}
	res, err := w.run(ctx)
	if err == nil {
		return res, nil
	}
	dctx, cancel := workflow.NewDisconnectedContext(ctx)
	defer cancel()
	dctx = workflow.WithActivityOptions(dctx, shortOpts)
	if temporal.IsCanceledError(err) || errors.Is(ctx.Err(), workflow.ErrCanceled) {
		_ = workflow.ExecuteActivity(dctx, a.MarkCancelled, CancelRequest{WorkflowRunID: in.WorkflowRunID, Reason: "cancelled by user"}).Get(dctx, nil)
		return Result{}, err
	}
	msg := errMessage(err)
	_ = workflow.ExecuteActivity(dctx, a.SetState, StateUpdate{WorkflowRunID: in.WorkflowRunID, Status: tasks.RunFailed, Error: &msg}).Get(dctx, nil)
	return Result{}, err
}

var shortOpts = workflow.ActivityOptions{
	StartToCloseTimeout: 30 * time.Second,
	// Database writes retry until Postgres is back; nothing is lost meanwhile.
	RetryPolicy: &temporal.RetryPolicy{InitialInterval: time.Second, BackoffCoefficient: 2, MaximumInterval: time.Minute},
}

var agentOpts = workflow.ActivityOptions{
	StartToCloseTimeout: 4 * time.Hour,    // queue wait + every agent attempt
	HeartbeatTimeout:    20 * time.Second, // heartbeats (and cancellation) reach the activity within ~16s
	WaitForCancellation: true,
	RetryPolicy: &temporal.RetryPolicy{
		InitialInterval: 2 * time.Second, BackoffCoefficient: 2, MaximumInterval: time.Minute,
		NonRetryableErrorTypes: []string{ErrTypeAgentFailed, "InvalidStage"},
	},
}

func (w *engineering) run(ctx workflow.Context) (Result, error) {
	if err := w.stage(ctx, tasks.StageProductAnalysis, "product-agent", tasks.KindProductAnalysis, 0, ""); err != nil {
		return Result{}, err
	}
	if err := w.stage(ctx, tasks.StageRequirements, "requirements-agent", tasks.KindRequirements, 0, ""); err != nil {
		return Result{}, err
	}
	if err := w.gate(ctx, tasks.GateScope, tasks.StageScopeApproval, func(rev int, feedback string) error {
		return w.stage(ctx, tasks.StageRequirements, "requirements-agent", tasks.KindRequirements, rev, feedback)
	}); err != nil {
		return Result{}, err
	}
	planAndReview := func(rev int, feedback string) error {
		w.notes = nil
		if err := w.stage(ctx, tasks.StagePlanning, "planner-agent", tasks.KindPlan, rev, feedback); err != nil {
			return err
		}
		return w.stage(ctx, tasks.StageArchitectureReview, "architect-agent", tasks.KindArchitectureReview, rev, feedback)
	}
	if err := planAndReview(0, ""); err != nil {
		return Result{}, err
	}
	if err := w.gate(ctx, tasks.GatePlan, tasks.StagePlanApproval, planAndReview); err != nil {
		return Result{}, err
	}

	if err := w.setState(ctx, tasks.RunRunning, tasks.StageFinalReport, nil); err != nil {
		return Result{}, err
	}
	if err := w.step(ctx, tasks.StepUpdate{Stage: tasks.StageFinalReport, Attempt: 1, Status: "running"}); err != nil {
		return Result{}, err
	}
	var reportID uuid.UUID
	sctx := workflow.WithActivityOptions(ctx, shortOpts)
	if err := workflow.ExecuteActivity(sctx, a.BuildFinalReport, ReportRequest{WorkflowRunID: w.in.WorkflowRunID, TaskID: w.in.TaskID}).Get(ctx, &reportID); err != nil {
		return Result{}, err
	}
	if err := w.step(ctx, tasks.StepUpdate{Stage: tasks.StageFinalReport, Attempt: 1, Status: "succeeded", ArtifactID: &reportID}); err != nil {
		return Result{}, err
	}
	if err := w.setState(ctx, tasks.RunCompleted, tasks.StageCompleted, nil); err != nil {
		return Result{}, err
	}
	return Result{ReportArtifactID: reportID}, nil
}

// stage runs one agent stage. A failed agent run parks the workflow in
// awaiting_retry until a human sends the retry signal (or cancels).
func (w *engineering) stage(ctx workflow.Context, stage, agent, kind string, revision int, feedback string) error {
	for attempt := int32(1); ; attempt++ {
		if err := w.setState(ctx, tasks.RunRunning, stage, nil); err != nil {
			return err
		}
		w.runs[stage]++
		actx := workflow.WithActivityOptions(ctx, agentOpts)
		var res StageResult
		err := workflow.ExecuteActivity(actx, a.RunStage, StageRequest{
			WorkflowRunID: w.in.WorkflowRunID, TaskID: w.in.TaskID, ProjectID: w.in.ProjectID, CreatedBy: w.in.CreatedBy,
			Stage: stage, Agent: agent, Kind: kind, Attempt: attempt, Revision: revision, Feedback: feedback,
			Step: w.runs[stage],
		}).Get(ctx, &res)
		if err == nil {
			w.notes = append(w.notes, res.Notes...)
			return nil
		}
		if isCanceled(ctx, err) {
			return err
		}
		// Discard retry signals sent before this failure; only a retry requested
		// while the workflow is awaiting_retry counts.
		for w.retryCh.ReceiveAsync(nil) {
		}
		msg := errMessage(err)
		if err := w.setState(ctx, tasks.RunAwaitingRetry, stage, &msg); err != nil {
			return err
		}
		if err := w.await(ctx, w.retryCh, nil); err != nil {
			return err
		}
	}
}

// gate asks for approval of the gate's artifacts. A rejection runs revise with
// the reviewer's comment, which produces new artifact versions (superseding the
// old approval), then asks again — up to Config.MaxRevisions times.
func (w *engineering) gate(ctx workflow.Context, gate, stage string, revise func(rev int, feedback string) error) error {
	if !w.in.Config.GateRequired(gate) {
		return w.step(ctx, tasks.StepUpdate{Stage: stage, Attempt: 1, Status: "skipped"})
	}
	sctx := workflow.WithActivityOptions(ctx, shortOpts)
	for revisions := 0; ; {
		if err := w.setState(ctx, tasks.RunRunning, stage, nil); err != nil {
			return err
		}
		var ap ApprovalInfo
		if err := workflow.ExecuteActivity(sctx, a.CreateApproval, ApprovalRequest{
			WorkflowRunID: w.in.WorkflowRunID, TaskID: w.in.TaskID, Gate: gate, Notes: w.gateNotes(gate),
		}).Get(ctx, &ap); err != nil {
			return err
		}
		if err := w.step(ctx, tasks.StepUpdate{Stage: stage, Attempt: ap.Round, Status: "awaiting_approval"}); err != nil {
			return err
		}
		if err := w.setState(ctx, tasks.RunAwaitingApproval, stage, nil); err != nil {
			return err
		}
		decision, err := w.awaitDecision(ctx, ap.ID)
		if err != nil {
			return err
		}
		switch decision.Status {
		case "approved":
			return w.step(ctx, tasks.StepUpdate{Stage: stage, Attempt: ap.Round, Status: "succeeded"})
		case "rejected":
			reason := "rejected: " + decision.Comment
			if err := w.step(ctx, tasks.StepUpdate{Stage: stage, Attempt: ap.Round, Status: "failed", Error: &reason}); err != nil {
				return err
			}
			revisions++
			if revisions > w.in.Config.MaxRevisions {
				return temporal.NewNonRetryableApplicationError(
					fmt.Sprintf("%s gate rejected %d times; revision limit reached", gate, revisions), "RevisionLimit", nil)
			}
			if err := revise(revisions, decision.Comment); err != nil {
				return err
			}
		default: // superseded or cancelled out from under us: ask again
		}
	}
}

func (w *engineering) gateNotes(gate string) []string {
	if gate == tasks.GatePlan {
		return w.notes
	}
	return nil
}

// awaitDecision waits for the signal naming this approval, then reads the
// decision from the database (the source of truth).
func (w *engineering) awaitDecision(ctx workflow.Context, id uuid.UUID) (tasks.Decision, error) {
	sctx := workflow.WithActivityOptions(ctx, shortOpts)
	for {
		var sig ApprovalSignal
		if err := w.await(ctx, w.approvalCh, &sig); err != nil {
			return tasks.Decision{}, err
		}
		if sig.ApprovalID != id {
			continue // stale signal for an earlier round
		}
		var d tasks.Decision
		if err := workflow.ExecuteActivity(sctx, a.ResolveApproval, id).Get(ctx, &d); err != nil {
			return tasks.Decision{}, err
		}
		if d.Status != "pending" {
			return d, nil
		}
	}
}

// await blocks on a signal channel until a signal arrives or the workflow is cancelled.
func (w *engineering) await(ctx workflow.Context, ch workflow.ReceiveChannel, into any) error {
	sel := workflow.NewSelector(ctx)
	got := false
	sel.AddReceive(ch, func(c workflow.ReceiveChannel, _ bool) {
		c.Receive(ctx, into)
		got = true
	})
	sel.AddReceive(ctx.Done(), func(workflow.ReceiveChannel, bool) {})
	sel.Select(ctx)
	if !got {
		return temporal.NewCanceledError()
	}
	return nil
}

func (w *engineering) setState(ctx workflow.Context, status, stage string, errMsg *string) error {
	sctx := workflow.WithActivityOptions(ctx, shortOpts)
	return workflow.ExecuteActivity(sctx, a.SetState, StateUpdate{
		WorkflowRunID: w.in.WorkflowRunID, Status: status, Stage: stage, Error: errMsg,
	}).Get(ctx, nil)
}

func (w *engineering) step(ctx workflow.Context, u tasks.StepUpdate) error {
	sctx := workflow.WithActivityOptions(ctx, shortOpts)
	return workflow.ExecuteActivity(sctx, a.RecordStep, StepRecord{WorkflowRunID: w.in.WorkflowRunID, Update: u}).Get(ctx, nil)
}

func isCanceled(ctx workflow.Context, err error) bool {
	return temporal.IsCanceledError(err) || errors.Is(ctx.Err(), workflow.ErrCanceled)
}

// errMessage unwraps Temporal's activity error chain to the root message.
func errMessage(err error) string {
	var appErr *temporal.ApplicationError
	if errors.As(err, &appErr) {
		return appErr.Message()
	}
	for u := errors.Unwrap(err); u != nil; u = errors.Unwrap(u) {
		err = u
	}
	return err.Error()
}
