package workflow

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"

	"wise/apps/api/internal/tasks"
)

// script drives the mocked activities and records what the workflow did.
type script struct {
	mu         sync.Mutex
	approvals  []uuid.UUID          // returned by CreateApproval, in order
	decisions  map[uuid.UUID]string // approval -> "approved" | "rejected"
	failStages map[string]int       // stage -> number of failures before success
	stageCalls []StageRequest
	states     []StateUpdate
	cancelled  bool
	nextAp     int
}

func (s *script) setup(env *testsuite.TestWorkflowEnvironment) {
	env.RegisterActivity(&Activities{})
	env.OnActivity(a.SetState, mock.Anything, mock.Anything).Return(func(_ context.Context, u StateUpdate) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.states = append(s.states, u)
		return nil
	})
	env.OnActivity(a.RecordStep, mock.Anything, mock.Anything).Return(nil)
	env.OnActivity(a.RunStage, mock.Anything, mock.Anything).Return(func(_ context.Context, r StageRequest) (StageResult, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.stageCalls = append(s.stageCalls, r)
		if s.failStages[r.Stage] > 0 {
			s.failStages[r.Stage]--
			return StageResult{}, temporal.NewNonRetryableApplicationError("agent exploded", ErrTypeAgentFailed, nil)
		}
		return StageResult{AgentRunID: uuid.New(), ArtifactID: uuid.New(), Version: 1}, nil
	})
	env.OnActivity(a.CreateApproval, mock.Anything, mock.Anything).Return(func(_ context.Context, r ApprovalRequest) (ApprovalInfo, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		id := s.approvals[s.nextAp]
		s.nextAp++
		return ApprovalInfo{ID: id, Round: int32(s.nextAp)}, nil
	})
	env.OnActivity(a.ResolveApproval, mock.Anything, mock.Anything).Return(func(_ context.Context, id uuid.UUID) (tasks.Decision, error) {
		st, ok := s.decisions[id]
		if !ok {
			st = "pending"
		}
		comment := ""
		if st == "rejected" {
			comment = "please add audit logging"
		}
		return tasks.Decision{Status: st, Comment: comment}, nil
	})
	env.OnActivity(a.BuildFinalReport, mock.Anything, mock.Anything).Return(uuid.New(), nil)
	env.OnActivity(a.MarkCancelled, mock.Anything, mock.Anything).Return(func(_ context.Context, _ CancelRequest) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.cancelled = true
		return nil
	})
	// Decisions are signalled up front: the workflow must buffer them until it
	// reaches the matching gate and ignore signals for other approvals.
	env.RegisterDelayedCallback(func() {
		for _, id := range s.approvals {
			env.SignalWorkflow(tasks.SignalApprovalDecided, ApprovalSignal{ApprovalID: id})
		}
	}, 0)
}

func (s *script) stagesRun() []string {
	var out []string
	for _, c := range s.stageCalls {
		out = append(out, c.Stage)
	}
	return out
}

func input(cfg tasks.WorkflowConfig) Input {
	return Input{WorkflowRunID: uuid.New(), TaskID: uuid.New(), ProjectID: uuid.New(), Config: cfg}
}

func TestHappyPathThroughBothGates(t *testing.T) {
	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()
	scope, plan := uuid.New(), uuid.New()
	s := &script{approvals: []uuid.UUID{scope, plan}, decisions: map[uuid.UUID]string{scope: "approved", plan: "approved"}}
	s.setup(env)

	env.ExecuteWorkflow(EngineeringWorkflow, input(tasks.DefaultWorkflowConfig()))
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	var res Result
	require.NoError(t, env.GetWorkflowResult(&res))
	require.NotEqual(t, uuid.Nil, res.ReportArtifactID)
	require.Equal(t, []string{tasks.StageProductAnalysis, tasks.StageRequirements, tasks.StagePlanning, tasks.StageArchitectureReview}, s.stagesRun())
	last := s.states[len(s.states)-1]
	require.Equal(t, tasks.RunCompleted, last.Status)
	require.Equal(t, tasks.StageCompleted, last.Stage)
}

func TestRejectionRevisesWithFeedback(t *testing.T) {
	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()
	scope1, scope2, plan := uuid.New(), uuid.New(), uuid.New()
	s := &script{
		approvals: []uuid.UUID{scope1, scope2, plan},
		decisions: map[uuid.UUID]string{scope1: "rejected", scope2: "approved", plan: "approved"},
	}
	s.setup(env)

	env.ExecuteWorkflow(EngineeringWorkflow, input(tasks.DefaultWorkflowConfig()))
	require.NoError(t, env.GetWorkflowError())
	require.Equal(t, []string{
		tasks.StageProductAnalysis, tasks.StageRequirements, tasks.StageRequirements,
		tasks.StagePlanning, tasks.StageArchitectureReview,
	}, s.stagesRun())
	revised := s.stageCalls[2]
	require.Equal(t, 1, revised.Revision)
	require.Equal(t, "please add audit logging", revised.Feedback)
	require.Equal(t, int32(2), revised.Step, "the second execution of a stage is step 2")
}

func TestRevisionLimitFailsWorkflow(t *testing.T) {
	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()
	r1, r2 := uuid.New(), uuid.New()
	s := &script{approvals: []uuid.UUID{r1, r2}, decisions: map[uuid.UUID]string{r1: "rejected", r2: "rejected"}}
	s.setup(env)
	cfg := tasks.DefaultWorkflowConfig()
	cfg.MaxRevisions = 1

	env.ExecuteWorkflow(EngineeringWorkflow, input(cfg))
	err := env.GetWorkflowError()
	require.Error(t, err)
	var appErr *temporal.ApplicationError
	require.True(t, errors.As(err, &appErr))
	require.Equal(t, "RevisionLimit", appErr.Type())
	last := s.states[len(s.states)-1]
	require.Equal(t, tasks.RunFailed, last.Status)
	require.Contains(t, *last.Error, "revision limit")
}

func TestAgentFailureWaitsForRetry(t *testing.T) {
	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()
	scope, plan := uuid.New(), uuid.New()
	s := &script{
		approvals:  []uuid.UUID{scope, plan},
		decisions:  map[uuid.UUID]string{scope: "approved", plan: "approved"},
		failStages: map[string]int{tasks.StagePlanning: 1},
	}
	s.setup(env)
	env.RegisterDelayedCallback(func() { env.SignalWorkflow(tasks.SignalRetry, nil) }, time.Hour)

	env.ExecuteWorkflow(EngineeringWorkflow, input(tasks.DefaultWorkflowConfig()))
	require.NoError(t, env.GetWorkflowError())
	var retrying *StateUpdate
	for i := range s.states {
		if s.states[i].Status == tasks.RunAwaitingRetry {
			retrying = &s.states[i]
		}
	}
	require.NotNil(t, retrying, "workflow must park in awaiting_retry")
	require.Equal(t, tasks.StagePlanning, retrying.Stage)
	require.Equal(t, "agent exploded", *retrying.Error)
	var attempts []int32
	for _, c := range s.stageCalls {
		if c.Stage == tasks.StagePlanning {
			attempts = append(attempts, c.Attempt)
		}
	}
	require.Equal(t, []int32{1, 2}, attempts)
}

func TestCancelDuringGate(t *testing.T) {
	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()
	s := &script{approvals: []uuid.UUID{uuid.New()}, decisions: map[uuid.UUID]string{}}
	s.setup(env)
	// The scope decision is never made ("pending"); cancel while waiting.
	env.RegisterDelayedCallback(func() { env.CancelWorkflow() }, time.Hour)

	env.ExecuteWorkflow(EngineeringWorkflow, input(tasks.DefaultWorkflowConfig()))
	err := env.GetWorkflowError()
	require.Error(t, err)
	require.True(t, temporal.IsCanceledError(err), "got %v", err)
	require.True(t, s.cancelled, "MarkCancelled must run on cancellation")
}

func TestGateSkippedWhenNotRequired(t *testing.T) {
	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()
	plan := uuid.New()
	s := &script{approvals: []uuid.UUID{plan}, decisions: map[uuid.UUID]string{plan: "approved"}}
	s.setup(env)
	cfg := tasks.DefaultWorkflowConfig()
	cfg.Gates[tasks.GateScope] = tasks.GateConfig{Required: false}

	env.ExecuteWorkflow(EngineeringWorkflow, input(cfg))
	require.NoError(t, env.GetWorkflowError())
	require.Equal(t, 1, s.nextAp, "only the plan gate asks for approval")
}
