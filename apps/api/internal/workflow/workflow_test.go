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
	decisions  map[uuid.UUID]string // approval -> "approved" | "rejected" (default pending)
	failStages map[string]int       // stage -> failures before success
	stageCalls []StageRequest
	gates      []ApprovalRequest
	steps      []tasks.StepUpdate
	states     []StateUpdate
	reports    int
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
	env.OnActivity(a.RecordStep, mock.Anything, mock.Anything).Return(func(_ context.Context, r StepRecord) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.steps = append(s.steps, r.Update)
		return nil
	})
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
		s.gates = append(s.gates, r)
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
	env.OnActivity(a.BuildFinalReport, mock.Anything, mock.Anything).Return(func(_ context.Context, _ ReportRequest) (uuid.UUID, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.reports++
		return uuid.New(), nil
	})
	env.OnActivity(a.MarkCancelled, mock.Anything, mock.Anything).Return(func(_ context.Context, _ CancelRequest) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.cancelled = true
		return nil
	})
	// Decisions are signalled up front: the workflow must buffer them until it
	// reaches the matching review and ignore signals for other approvals.
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

// engineering mirrors the seeded Engineering template.
func engineering() tasks.Plan {
	return tasks.Plan{Type: "pipeline", FinalReport: true, MaxRevisions: 5, Destination: "done", Steps: []tasks.PlanStep{
		{ID: "analysis", Name: "Analysis", Agent: "product-agent", Kind: "product_analysis", Enabled: true},
		{ID: "requirements", Name: "Requirements", Agent: "requirements-agent", Kind: "requirements", Enabled: true,
			Review: true, ReviewKinds: []string{"requirements", "product_analysis"}},
		{ID: "planning", Name: "Planning", Agent: "planner-agent", Kind: "plan", Enabled: true},
		{ID: "architecture", Name: "Architecture", Agent: "architect-agent", Kind: "architecture_review", Enabled: true,
			Review: true, ReviewKinds: []string{"architecture_review", "requirements", "plan"}, ReviseFrom: "planning"},
	}}
}

func input(p tasks.Plan) Input {
	return Input{WorkflowRunID: uuid.New(), TaskID: uuid.New(), ProjectID: uuid.New(), Plan: p}
}

func newEnv() *testsuite.TestWorkflowEnvironment {
	var ts testsuite.WorkflowTestSuite
	return ts.NewTestWorkflowEnvironment()
}

func TestPipelineHappyPath(t *testing.T) {
	env := newEnv()
	r1, r2 := uuid.New(), uuid.New()
	s := &script{approvals: []uuid.UUID{r1, r2}, decisions: map[uuid.UUID]string{r1: "approved", r2: "approved"}}
	s.setup(env)

	env.ExecuteWorkflow(PipelineWorkflow, input(engineering()))
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	require.Equal(t, []string{"analysis", "requirements", "planning", "architecture"}, s.stagesRun())
	require.Equal(t, "requirements", s.gates[0].Gate)
	require.Equal(t, []string{"requirements", "product_analysis"}, s.gates[0].Kinds)
	require.Equal(t, "architecture", s.gates[1].Gate)
	require.Equal(t, 1, s.reports)
	last := s.states[len(s.states)-1]
	require.Equal(t, tasks.RunCompleted, last.Status)
}

func TestRejectionRevisesFromReviseFrom(t *testing.T) {
	env := newEnv()
	r1, r2, r3 := uuid.New(), uuid.New(), uuid.New()
	s := &script{approvals: []uuid.UUID{r1, r2, r3}, decisions: map[uuid.UUID]string{r1: "approved", r2: "rejected", r3: "approved"}}
	s.setup(env)

	env.ExecuteWorkflow(PipelineWorkflow, input(engineering()))
	require.NoError(t, env.GetWorkflowError())
	// Rejecting architecture re-runs planning and architecture, not earlier steps.
	require.Equal(t, []string{"analysis", "requirements", "planning", "architecture", "planning", "architecture"}, s.stagesRun())
	revised := s.stageCalls[4]
	require.Equal(t, 1, revised.Revision)
	require.Equal(t, "please add audit logging", revised.Feedback)
	require.Equal(t, int32(2), revised.Step, "the second execution of a step is row 2")
}

func TestRevisionLimitFailsWorkflow(t *testing.T) {
	env := newEnv()
	r1, r2 := uuid.New(), uuid.New()
	s := &script{approvals: []uuid.UUID{r1, r2}, decisions: map[uuid.UUID]string{r1: "rejected", r2: "rejected"}}
	s.setup(env)
	p := engineering()
	p.MaxRevisions = 1

	env.ExecuteWorkflow(PipelineWorkflow, input(p))
	var appErr *temporal.ApplicationError
	require.True(t, errors.As(env.GetWorkflowError(), &appErr))
	require.Equal(t, "RevisionLimit", appErr.Type())
	last := s.states[len(s.states)-1]
	require.Equal(t, tasks.RunFailed, last.Status)
	require.Contains(t, *last.Error, "revision limit")
}

func TestAgentFailureWaitsForRetry(t *testing.T) {
	env := newEnv()
	r1, r2 := uuid.New(), uuid.New()
	s := &script{approvals: []uuid.UUID{r1, r2}, decisions: map[uuid.UUID]string{r1: "approved", r2: "approved"},
		failStages: map[string]int{"planning": 1}}
	s.setup(env)
	env.RegisterDelayedCallback(func() { env.SignalWorkflow(tasks.SignalRetry, nil) }, time.Hour)

	env.ExecuteWorkflow(PipelineWorkflow, input(engineering()))
	require.NoError(t, env.GetWorkflowError())
	var retrying *StateUpdate
	for i := range s.states {
		if s.states[i].Status == tasks.RunAwaitingRetry {
			retrying = &s.states[i]
		}
	}
	require.NotNil(t, retrying)
	require.Equal(t, "planning", retrying.Stage)
	require.Equal(t, "agent exploded", *retrying.Error)
	var attempts []int32
	for _, c := range s.stageCalls {
		if c.Stage == "planning" {
			attempts = append(attempts, c.Attempt)
		}
	}
	require.Equal(t, []int32{1, 2}, attempts)
}

func TestCancelDuringReview(t *testing.T) {
	env := newEnv()
	s := &script{approvals: []uuid.UUID{uuid.New()}, decisions: map[uuid.UUID]string{}}
	s.setup(env)
	env.RegisterDelayedCallback(func() { env.CancelWorkflow() }, time.Hour)

	env.ExecuteWorkflow(PipelineWorkflow, input(engineering()))
	require.True(t, temporal.IsCanceledError(env.GetWorkflowError()))
	require.True(t, s.cancelled)
}

func TestDisabledStepsSkipAndNoReviewMeansNoApproval(t *testing.T) {
	env := newEnv()
	s := &script{}
	s.setup(env)
	p := engineering()
	p.Steps[0].Enabled = false
	for i := range p.Steps {
		p.Steps[i].Review = false
	}
	p.FinalReport = false

	env.ExecuteWorkflow(PipelineWorkflow, input(p))
	require.NoError(t, env.GetWorkflowError())
	require.Equal(t, []string{"requirements", "planning", "architecture"}, s.stagesRun())
	require.Empty(t, s.gates)
	require.Zero(t, s.reports, "final report disabled")
	require.Equal(t, tasks.StepUpdate{Stage: "analysis", Attempt: 1, Status: "skipped"}, s.steps[0])
}

func TestStepByStepReviewsEveryStep(t *testing.T) {
	env := newEnv()
	ids := []uuid.UUID{uuid.New(), uuid.New(), uuid.New(), uuid.New()}
	dec := map[uuid.UUID]string{}
	for _, id := range ids {
		dec[id] = "approved"
	}
	s := &script{approvals: ids, decisions: dec}
	s.setup(env)
	p := engineering()
	for i := range p.Steps {
		p.Steps[i].Review = true
		if p.Steps[i].ReviewKinds == nil {
			p.Steps[i].ReviewKinds = []string{p.Steps[i].Kind}
		}
	}

	env.ExecuteWorkflow(PipelineWorkflow, input(p))
	require.NoError(t, env.GetWorkflowError())
	require.Len(t, s.gates, 4)
	for i, g := range s.gates {
		require.Equal(t, p.Steps[i].ID, g.Gate, "each step is reviewed right after it runs")
	}
}
