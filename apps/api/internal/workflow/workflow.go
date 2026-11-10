package workflow

import (
	"errors"
	"fmt"
	"strings"
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

// Stage names besides step ids.
const (
	StageFinalReport = "report"
	StageCompleted   = "completed"
)

// Input starts a PipelineWorkflow. The plan is frozen at start (step order,
// agents, review gates); a step's account and model are read when it runs, so
// they can still be changed while the workflow waits.
type Input struct {
	WorkflowRunID uuid.UUID
	TaskID        uuid.UUID
	ProjectID     uuid.UUID
	CreatedBy     *uuid.UUID
	Plan          tasks.Plan
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

type pipeline struct {
	in         Input
	approvalCh workflow.ReceiveChannel
	retryCh    workflow.ReceiveChannel
	notes      map[string][]string // step id → notes for its review
	runs       map[string]int32    // executions per step, numbering workflow_steps rows
}

// PipelineWorkflow runs a task through the steps of its workflow template:
// each enabled step runs its agent; a step with review pauses until the user
// passes its result on (approve) or asks for changes (re-run with feedback).
// Every state change is mirrored to Postgres through activities.
func PipelineWorkflow(ctx workflow.Context, in Input) (Result, error) {
	w := &pipeline{
		in:         in,
		approvalCh: workflow.GetSignalChannel(ctx, tasks.SignalApprovalDecided),
		retryCh:    workflow.GetSignalChannel(ctx, tasks.SignalRetry),
		notes:      map[string][]string{},
		runs:       map[string]int32{},
	}
	res, err := w.run(ctx)
	if err == nil {
		return res, nil
	}
	dctx, cancel := workflow.NewDisconnectedContext(ctx)
	defer cancel()
	dctx = workflow.WithActivityOptions(dctx, shortOpts)
	w.stopWorkspace(dctx)
	if isCanceled(ctx, err) {
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
	StartToCloseTimeout: 4 * time.Hour, // queue wait + every agent attempt
	HeartbeatTimeout:    20 * time.Second,
	WaitForCancellation: true,
	RetryPolicy: &temporal.RetryPolicy{
		InitialInterval: 2 * time.Second, BackoffCoefficient: 2, MaximumInterval: time.Minute,
		NonRetryableErrorTypes: []string{ErrTypeAgentFailed, "InvalidStage"},
	},
}

func (w *pipeline) run(ctx workflow.Context) (Result, error) {
	steps := w.in.Plan.Steps
	for i, st := range steps {
		if !st.Enabled {
			if err := w.step(ctx, tasks.StepUpdate{Stage: st.ID, Attempt: 1, Status: "skipped"}); err != nil {
				return Result{}, err
			}
			continue
		}
		if err := w.runStep(ctx, st, 0, ""); err != nil {
			return Result{}, err
		}
		if !st.Review {
			continue
		}
		from := i
		if st.ReviseFrom != "" {
			if _, j, ok := w.in.Plan.Step(st.ReviseFrom); ok && j <= i {
				from = j
			}
		}
		if err := w.review(ctx, st, func(rev int, feedback string) error {
			for _, s := range steps[from : i+1] {
				if s.Enabled {
					if err := w.runStep(ctx, s, rev, feedback); err != nil {
						return err
					}
				}
			}
			return nil
		}); err != nil {
			return Result{}, err
		}
	}

	var reportID uuid.UUID
	if w.in.Plan.FinalReport {
		if err := w.setState(ctx, tasks.RunRunning, StageFinalReport, nil); err != nil {
			return Result{}, err
		}
		if err := w.step(ctx, tasks.StepUpdate{Stage: StageFinalReport, Attempt: 1, Status: "running"}); err != nil {
			return Result{}, err
		}
		sctx := workflow.WithActivityOptions(ctx, shortOpts)
		if err := workflow.ExecuteActivity(sctx, a.BuildFinalReport, ReportRequest{WorkflowRunID: w.in.WorkflowRunID, TaskID: w.in.TaskID}).Get(ctx, &reportID); err != nil {
			return Result{}, err
		}
		if err := w.step(ctx, tasks.StepUpdate{Stage: StageFinalReport, Attempt: 1, Status: "succeeded", ArtifactID: &reportID}); err != nil {
			return Result{}, err
		}
	}
	w.stopWorkspace(ctx)
	if err := w.setState(ctx, tasks.RunCompleted, StageCompleted, nil); err != nil {
		return Result{}, err
	}
	return Result{ReportArtifactID: reportID}, nil
}

// runStep runs one step. A failed step parks the workflow in awaiting_retry
// until a human sends the retry signal (or cancels).
func (w *pipeline) runStep(ctx workflow.Context, st tasks.PlanStep, revision int, feedback string) error {
	if !st.IsAgent() {
		return w.runEngineering(ctx, st, revision)
	}
	return w.runAgent(ctx, st, revision, feedback, 0, "")
}

// runAgent runs an agent step (fixRound > 0: a quality-gate fix round).
func (w *pipeline) runAgent(ctx workflow.Context, st tasks.PlanStep, revision int, feedback string, fixRound int, fixFeedback string) error {
	for attempt := int32(1); ; attempt++ {
		if err := w.setState(ctx, tasks.RunRunning, st.ID, nil); err != nil {
			return err
		}
		w.runs[st.ID]++
		actx := workflow.WithActivityOptions(ctx, agentOpts)
		var res StageResult
		err := workflow.ExecuteActivity(actx, a.RunStage, StageRequest{
			WorkflowRunID: w.in.WorkflowRunID, TaskID: w.in.TaskID, ProjectID: w.in.ProjectID, CreatedBy: w.in.CreatedBy,
			Stage: st.ID, Agent: st.Agent, Kind: st.Kind, Attempt: attempt, Revision: revision, Feedback: feedback,
			Step: w.runs[st.ID], FixRound: fixRound, FixFeedback: fixFeedback,
		}).Get(ctx, &res)
		if err == nil {
			w.notes[st.ID] = res.Notes
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
		if err := w.setState(ctx, tasks.RunAwaitingRetry, st.ID, &msg); err != nil {
			return err
		}
		if err := w.await(ctx, w.retryCh, nil); err != nil {
			return err
		}
	}
}

// review asks for approval of the step's output (and the outputs it includes).
// A rejection runs revise with the reviewer's comment, which produces new
// artifact versions (superseding the old approval), then asks again — up to
// Plan.MaxRevisions times.
func (w *pipeline) review(ctx workflow.Context, st tasks.PlanStep, revise func(rev int, feedback string) error) error {
	sctx := workflow.WithActivityOptions(ctx, shortOpts)
	for revisions := 0; ; {
		var notes []string
		for _, k := range st.ReviewKinds {
			for _, s := range w.in.Plan.Steps {
				if s.Kind == k {
					notes = append(notes, w.notes[s.ID]...)
				}
			}
		}
		var ap ApprovalInfo
		if err := workflow.ExecuteActivity(sctx, a.CreateApproval, ApprovalRequest{
			WorkflowRunID: w.in.WorkflowRunID, TaskID: w.in.TaskID, Gate: st.ID, Kinds: st.ReviewKinds, Notes: notes,
		}).Get(ctx, &ap); err != nil {
			return err
		}
		if err := w.setState(ctx, tasks.RunAwaitingApproval, st.ID, nil); err != nil {
			return err
		}
		decision, err := w.awaitDecision(ctx, ap.ID)
		if err != nil {
			return err
		}
		switch decision.Status {
		case "approved":
			return nil
		case "rejected":
			revisions++
			if revisions > w.in.Plan.MaxRevisions {
				return temporal.NewNonRetryableApplicationError(
					fmt.Sprintf("%s rejected %d times; revision limit reached", st.Name, revisions), "RevisionLimit", nil)
			}
			if err := revise(revisions, decision.Comment); err != nil {
				return err
			}
		default: // superseded or cancelled out from under us: ask again
		}
	}
}

// awaitDecision waits for the signal naming this approval, then reads the
// decision from the database (the source of truth).
func (w *pipeline) awaitDecision(ctx workflow.Context, id uuid.UUID) (tasks.Decision, error) {
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
func (w *pipeline) await(ctx workflow.Context, ch workflow.ReceiveChannel, into any) error {
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

func (w *pipeline) setState(ctx workflow.Context, status, stage string, errMsg *string) error {
	sctx := workflow.WithActivityOptions(ctx, shortOpts)
	return workflow.ExecuteActivity(sctx, a.SetState, StateUpdate{
		WorkflowRunID: w.in.WorkflowRunID, Status: status, Stage: stage, Error: errMsg,
	}).Get(ctx, nil)
}

func (w *pipeline) step(ctx workflow.Context, u tasks.StepUpdate) error {
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

// engOpts run engineering steps: preparing a workspace and running checks
// can take long; they heartbeat while they wait.
var engOpts = workflow.ActivityOptions{
	StartToCloseTimeout: 8 * time.Hour,
	HeartbeatTimeout:    2 * time.Minute,
	WaitForCancellation: true,
	RetryPolicy: &temporal.RetryPolicy{
		InitialInterval: 2 * time.Second, BackoffCoefficient: 2, MaximumInterval: time.Minute,
		NonRetryableErrorTypes: []string{ErrTypeAgentFailed},
	},
}

// retrying runs an engineering activity call, parking in awaiting_retry when
// it fails until a human retries.
func (w *pipeline) retrying(ctx workflow.Context, st tasks.PlanStep, call func(actx workflow.Context, req EngRequest) error) error {
	for {
		if err := w.setState(ctx, tasks.RunRunning, st.ID, nil); err != nil {
			return err
		}
		w.runs[st.ID]++
		req := EngRequest{WorkflowRunID: w.in.WorkflowRunID, TaskID: w.in.TaskID, CreatedBy: w.in.CreatedBy, Stage: st.ID, Step: w.runs[st.ID]}
		err := call(workflow.WithActivityOptions(ctx, engOpts), req)
		if err == nil {
			return nil
		}
		if isCanceled(ctx, err) {
			return err
		}
		if err := w.park(ctx, st.ID, errMessage(err)); err != nil {
			return err
		}
	}
}

// park waits in awaiting_retry for the retry signal.
func (w *pipeline) park(ctx workflow.Context, stage, msg string) error {
	for w.retryCh.ReceiveAsync(nil) {
	}
	if err := w.setState(ctx, tasks.RunAwaitingRetry, stage, &msg); err != nil {
		return err
	}
	return w.await(ctx, w.retryCh, nil)
}

// runEngineering runs a workspace, quality, gate or deliver step.
func (w *pipeline) runEngineering(ctx workflow.Context, st tasks.PlanStep, revision int) error {
	switch st.Type {
	case tasks.StepWorkspace:
		return w.retrying(ctx, st, func(actx workflow.Context, req EngRequest) error {
			return workflow.ExecuteActivity(actx, a.PrepareWorkspace, req).Get(ctx, nil)
		})
	case tasks.StepQuality:
		return w.retrying(ctx, st, func(actx workflow.Context, req EngRequest) error {
			return workflow.ExecuteActivity(actx, a.RunQuality, req).Get(ctx, nil)
		})
	case tasks.StepGate:
		return w.runGate(ctx, st, revision)
	case tasks.StepDeliver:
		gate := ""
		_, idx, _ := w.in.Plan.Step(st.ID)
		for j := idx - 1; j >= 0; j-- {
			if s := w.in.Plan.Steps[j]; s.Enabled && s.Type == tasks.StepGate && s.Review {
				gate = s.ID
				break
			}
		}
		return w.retrying(ctx, st, func(actx workflow.Context, req EngRequest) error {
			return workflow.ExecuteActivity(actx, a.Deliver, DeliverRequest{EngRequest: req, Gate: gate}).Get(ctx, nil)
		})
	}
	return temporal.NewNonRetryableApplicationError("unknown step type "+st.Type, "InvalidStage", nil)
}

// runGate evaluates the checks and reviews between the implementing step and
// the gate. While they fail it runs fix rounds: the implementing agent gets
// the failures (and the diagnosis), then the checks and reviews run again.
// After the configured rounds it waits for a human retry. When everything
// passes it snapshots the verified changeset that the gate's review approves.
func (w *pipeline) runGate(ctx workflow.Context, st tasks.PlanStep, revision int) error {
	steps := w.in.Plan.Steps
	_, fixIdx, _ := w.in.Plan.Step(st.FixFrom)
	_, gateIdx, _ := w.in.Plan.Step(st.ID)
	var kinds []string
	for _, s := range steps[fixIdx+1 : gateIdx] {
		if s.Enabled && s.Kind != "" && s.Kind != tasks.KindImplementation {
			kinds = append(kinds, s.Kind)
		}
	}
	for round := 0; ; round++ {
		var g GateResult
		if err := w.retrying(ctx, st, func(actx workflow.Context, req EngRequest) error {
			req.Kinds, req.Round = kinds, round
			return workflow.ExecuteActivity(actx, a.EvaluateGate, req).Get(ctx, &g)
		}); err != nil {
			return err
		}
		w.notes[st.ID] = []string{"Quality gate: " + g.Summary}
		feedback, diagnose := g.Feedback, true
		if g.Passed {
			// Commit and verify the change; a credential in it fails the round.
			var snap SnapshotResult
			if err := w.retrying(ctx, st, func(actx workflow.Context, req EngRequest) error {
				return workflow.ExecuteActivity(actx, a.Snapshot, req).Get(ctx, &snap)
			}); err != nil {
				return err
			}
			if !snap.SecretFound {
				return nil
			}
			g.Summary = "the change contains credentials."
			feedback, diagnose = secretFeedback(snap.Secrets), false
			w.notes[st.ID] = []string{"Quality gate: " + g.Summary}
		}
		if round >= g.MaxRounds {
			msg := fmt.Sprintf("quality gate failed after %d fix round(s): %s Retry to run another fix round.", round, g.Summary)
			if err := w.park(ctx, st.ID, msg); err != nil {
				return err
			}
		}
		if diagnose && st.DiagnoseWith != "" {
			diag := tasks.PlanStep{ID: st.ID + tasks.DiagnoseSuffix, Name: st.Name + " (diagnosis)", Agent: st.DiagnoseWith, Kind: tasks.KindQA, Enabled: true}
			if err := w.runAgent(ctx, diag, revision, "", round+1, ""); err != nil {
				return err
			}
			var qa string
			sctx := workflow.WithActivityOptions(ctx, shortOpts)
			if err := workflow.ExecuteActivity(sctx, a.QAFeedback, w.in.TaskID).Get(ctx, &qa); err != nil {
				return err
			}
			if qa != "" {
				feedback = qa + "\n\n" + feedback
			}
		}
		for _, s := range steps[fixIdx:gateIdx] {
			if !s.Enabled {
				continue
			}
			var err error
			switch {
			case s.ID == st.FixFrom:
				err = w.runAgent(ctx, s, revision, "", round+1, feedback)
			case s.IsAgent():
				err = w.runAgent(ctx, s, revision, "", round+1, "")
			default:
				err = w.runEngineering(ctx, s, revision)
			}
			if err != nil {
				return err
			}
		}
	}
}

func secretFeedback(files []string) string {
	msg := "### The change contains credentials\nA token, key or credential file is in the diff. Remove it: read secrets from configuration or the environment, as the repository already does, and use obvious placeholders in tests."
	if len(files) > 0 {
		msg += "\nCredential-like files in the change: " + strings.Join(files, ", ")
	}
	return msg
}

// stopWorkspace stops the task sandbox when a workflow with a workspace ends.
func (w *pipeline) stopWorkspace(ctx workflow.Context) {
	if !w.in.Plan.HasType(tasks.StepWorkspace) {
		return
	}
	sctx := workflow.WithActivityOptions(ctx, shortOpts)
	_ = workflow.ExecuteActivity(sctx, a.StopWorkspace, w.in.TaskID).Get(ctx, nil)
}
