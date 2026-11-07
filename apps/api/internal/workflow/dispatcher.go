package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"

	"wise/apps/api/internal/db"
	"wise/apps/api/internal/db/dbgen"
	"wise/apps/api/internal/tasks"
)

// Temporal is the slice of the Temporal client the dispatcher uses (faked in tests).
type Temporal interface {
	ExecuteWorkflow(ctx context.Context, options client.StartWorkflowOptions, workflow any, args ...any) (client.WorkflowRun, error)
	SignalWorkflow(ctx context.Context, workflowID, runID, signalName string, arg any) error
	CancelWorkflow(ctx context.Context, workflowID, runID string) error
}

// Dispatcher delivers workflow_commands (the transactional outbox) to Temporal.
// Commands are claimed with SKIP LOCKED, so several orchestrators can run; a
// command is marked processed only after Temporal accepted it.
type Dispatcher struct {
	DB       *db.DB
	Tasks    *tasks.Service
	Temporal Temporal
	Log      *slog.Logger
	// Interval is the fallback poll period when no NOTIFY arrives (default 5s).
	Interval time.Duration
}

// errPermanent marks commands that can never succeed (e.g. the workflow is gone).
var errPermanent = errors.New("permanent")

func (d *Dispatcher) Run(ctx context.Context) {
	wake := make(chan struct{}, 1)
	go d.DB.Listen(ctx, "wise_workflow_commands", d.Log, func(string) {
		select {
		case wake <- struct{}{}:
		default:
		}
	})
	interval := d.Interval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		d.Drain(ctx)
		select {
		case <-ctx.Done():
			return
		case <-wake:
		case <-t.C:
		}
	}
}

// Drain delivers every command that is due.
func (d *Dispatcher) Drain(ctx context.Context) {
	for ctx.Err() == nil {
		n, err := d.batch(ctx)
		if err != nil {
			d.Log.Warn("dispatch workflow commands", "err", err)
			return
		}
		if n == 0 {
			return
		}
	}
}

func (d *Dispatcher) batch(ctx context.Context) (int, error) {
	n := 0
	err := d.DB.InTx(ctx, func(q *dbgen.Queries) error {
		cmds, err := q.ClaimWorkflowCommands(ctx, 20)
		if err != nil {
			return err
		}
		for _, c := range cmds {
			err := d.deliver(ctx, c)
			switch {
			case err == nil:
				n++
				if err := q.MarkWorkflowCommandProcessed(ctx, c.ID); err != nil {
					return err
				}
			case errors.Is(err, errPermanent):
				n++
				d.Log.Warn("dropping workflow command", "id", c.ID, "kind", c.Kind, "workflow_run", c.WorkflowRunID, "err", err)
				if err := q.MarkWorkflowCommandProcessed(ctx, c.ID); err != nil {
					return err
				}
			default:
				backoff := min(1<<min(c.Attempts, 8), 300)
				d.Log.Warn("workflow command failed; will retry", "id", c.ID, "kind", c.Kind, "err", err, "retry_in_s", backoff)
				msg := err.Error()
				if err := q.MarkWorkflowCommandFailed(ctx, dbgen.MarkWorkflowCommandFailedParams{
					ID: c.ID, LastError: &msg, BackoffS: int32(backoff),
				}); err != nil {
					return err
				}
			}
		}
		return nil
	})
	return n, err
}

func (d *Dispatcher) deliver(ctx context.Context, c dbgen.WorkflowCommand) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	wfID := WorkflowID(c.WorkflowRunID)
	switch c.Kind {
	case tasks.CmdStart:
		info, err := d.Tasks.StartInfo(ctx, c.WorkflowRunID)
		if err != nil {
			return err
		}
		if info.Run.Status != tasks.RunPending {
			return nil // already started, or cancelled before it could start
		}
		run, err := d.Temporal.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
			ID:                    wfID,
			TaskQueue:             TaskQueue,
			WorkflowIDReusePolicy: enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE,
		}, tasks.WorkflowType, Input{
			WorkflowRunID: c.WorkflowRunID, TaskID: info.Task.ID, ProjectID: info.Project,
			CreatedBy: info.Run.CreatedBy, Config: info.Config,
		})
		var started *serviceerror.WorkflowExecutionAlreadyStarted
		if errors.As(err, &started) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := d.Tasks.SetTemporalRunID(ctx, c.WorkflowRunID, run.GetRunID()); err != nil {
			return err
		}
		// The workflow records its own states; only promote a run that is still pending.
		return d.Tasks.MarkStarted(ctx, c.WorkflowRunID)
	case tasks.CmdDecision:
		var p ApprovalSignal
		if err := json.Unmarshal(c.Payload, &p); err != nil || p.ApprovalID == uuid.Nil {
			return fmt.Errorf("%w: bad decision payload", errPermanent)
		}
		return notFoundIsPermanent(d.Temporal.SignalWorkflow(ctx, wfID, "", tasks.SignalApprovalDecided, p))
	case tasks.CmdRetry:
		return notFoundIsPermanent(d.Temporal.SignalWorkflow(ctx, wfID, "", tasks.SignalRetry, nil))
	case tasks.CmdCancel:
		err := d.Temporal.CancelWorkflow(ctx, wfID, "")
		var nf *serviceerror.NotFound
		if errors.As(err, &nf) {
			// Never started (or already gone): finalize the mirror directly.
			return d.Tasks.MarkCancelled(ctx, c.WorkflowRunID, "cancelled by user")
		}
		return err
	default:
		return fmt.Errorf("%w: unknown command kind %q", errPermanent, c.Kind)
	}
}

func notFoundIsPermanent(err error) error {
	var nf *serviceerror.NotFound
	if errors.As(err, &nf) {
		return fmt.Errorf("%w: workflow not found: %v", errPermanent, err)
	}
	return err
}
