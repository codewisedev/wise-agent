package workflow

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"

	"wise/apps/api/internal/tasks"
)

// codePlan mirrors the seeded "Engineering → code" template.
func codePlan() tasks.Plan {
	p := engineering()
	p.Steps = append(p.Steps,
		tasks.PlanStep{ID: "workspace", Name: "Workspace", Type: tasks.StepWorkspace, Enabled: true},
		tasks.PlanStep{ID: "implement", Name: "Implement", Agent: "backend-agent", Kind: "implementation", Enabled: true, AcceptsFeedback: true,
			Inputs: []string{"fix_feedback", "quality_commands"}},
		tasks.PlanStep{ID: "quality", Name: "Quality", Type: tasks.StepQuality, Kind: "quality_report", Enabled: true},
		tasks.PlanStep{ID: "code-review", Name: "Code review", Agent: "reviewer-agent", Kind: "code_review", Enabled: true},
		tasks.PlanStep{ID: "gate", Name: "Gate", Type: tasks.StepGate, Kind: "changeset", Enabled: true, FixFrom: "implement",
			DiagnoseWith: "qa-agent", Review: true, ReviewKinds: []string{"changeset", "implementation", "quality_report", "code_review"}, ReviseFrom: "implement"},
		tasks.PlanStep{ID: "deliver", Name: "Deliver", Type: tasks.StepDeliver, Kind: "delivery", Enabled: true},
	)
	return p
}

// eng drives the engineering activities.
type eng struct {
	gateFails   int // evaluations that fail before one passes
	maxRounds   int
	prepared    int
	quality     int
	evaluations []EngRequest
	snapshots   int
	delivered   []DeliverRequest
	stopped     int
}

func (e *eng) setup(env *testsuite.TestWorkflowEnvironment) {
	env.OnActivity(a.PrepareWorkspace, mock.Anything, mock.Anything).Return(func(context.Context, EngRequest) error { e.prepared++; return nil })
	env.OnActivity(a.RunQuality, mock.Anything, mock.Anything).Return(func(context.Context, EngRequest) error { e.quality++; return nil })
	env.OnActivity(a.EvaluateGate, mock.Anything, mock.Anything).Return(func(_ context.Context, r EngRequest) (GateResult, error) {
		e.evaluations = append(e.evaluations, r)
		if e.gateFails > 0 {
			e.gateFails--
			return GateResult{Passed: false, Summary: "1 of 2 checks failed.", Feedback: "### Check \"test\" failed", MaxRounds: e.maxRounds}, nil
		}
		return GateResult{Passed: true, Summary: "2 checks passed.", MaxRounds: e.maxRounds}, nil
	})
	env.OnActivity(a.QAFeedback, mock.Anything, mock.Anything).Return("## Diagnosis\nnil map in handler", nil)
	env.OnActivity(a.Snapshot, mock.Anything, mock.Anything).Return(func(context.Context, EngRequest) (uuid.UUID, error) {
		e.snapshots++
		return uuid.New(), nil
	})
	env.OnActivity(a.Deliver, mock.Anything, mock.Anything).Return(func(_ context.Context, r DeliverRequest) error {
		e.delivered = append(e.delivered, r)
		return nil
	})
	env.OnActivity(a.StopWorkspace, mock.Anything, mock.Anything).Return(func(context.Context, uuid.UUID) error { e.stopped++; return nil })
}

func approvals(n int) ([]uuid.UUID, map[uuid.UUID]string) {
	ids := make([]uuid.UUID, n)
	dec := map[uuid.UUID]string{}
	for i := range ids {
		ids[i] = uuid.New()
		dec[ids[i]] = "approved"
	}
	return ids, dec
}

func TestCodeWorkflowHappyPath(t *testing.T) {
	env := newEnv()
	ids, dec := approvals(3)
	s := &script{approvals: ids, decisions: dec}
	s.setup(env)
	e := &eng{maxRounds: 3}
	e.setup(env)

	env.ExecuteWorkflow(PipelineWorkflow, input(codePlan()))
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	require.Equal(t, []string{"analysis", "requirements", "planning", "architecture", "implement", "code-review"}, s.stagesRun())
	require.Equal(t, 1, e.prepared)
	require.Equal(t, 1, e.quality)
	require.Len(t, e.evaluations, 1)
	require.Equal(t, []string{"quality_report", "code_review"}, e.evaluations[0].Kinds)
	require.Equal(t, 1, e.snapshots)
	require.Equal(t, "gate", s.gates[2].Gate)
	require.Equal(t, []string{"changeset", "implementation", "quality_report", "code_review"}, s.gates[2].Kinds)
	require.Contains(t, strings.Join(s.gates[2].Notes, " "), "2 checks passed")
	require.Len(t, e.delivered, 1)
	require.Equal(t, "gate", e.delivered[0].Gate)
	require.Equal(t, 1, e.stopped)
}

func TestCodeWorkflowFixRounds(t *testing.T) {
	env := newEnv()
	ids, dec := approvals(3)
	s := &script{approvals: ids, decisions: dec}
	s.setup(env)
	e := &eng{gateFails: 2, maxRounds: 3}
	e.setup(env)

	env.ExecuteWorkflow(PipelineWorkflow, input(codePlan()))
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	// Two failing evaluations: each runs the diagnosis, then the implementing
	// agent with the failures, the checks and the review again.
	var impl []StageRequest
	diagnoses := 0
	for _, c := range s.stageCalls {
		switch c.Stage {
		case "implement":
			impl = append(impl, c)
		case "gate" + tasks.DiagnoseSuffix:
			diagnoses++
			require.Equal(t, "qa-agent", c.Agent)
		}
	}
	require.Len(t, impl, 3)
	require.Equal(t, 0, impl[0].FixRound)
	require.Equal(t, 1, impl[1].FixRound)
	require.Equal(t, 2, impl[2].FixRound)
	require.Contains(t, impl[1].FixFeedback, "nil map in handler")
	require.Contains(t, impl[1].FixFeedback, "Check \"test\" failed")
	require.Equal(t, 2, diagnoses)
	require.Equal(t, 3, e.quality)
	require.Len(t, e.evaluations, 3)
	require.Equal(t, 1, e.snapshots)
	require.Len(t, e.delivered, 1)
}

func TestCodeWorkflowParksAfterMaxRounds(t *testing.T) {
	env := newEnv()
	ids, dec := approvals(3)
	s := &script{approvals: ids, decisions: dec}
	s.setup(env)
	e := &eng{gateFails: 2, maxRounds: 1}
	e.setup(env)
	// After one fix round the gate still fails: the workflow waits for a
	// human; the retry runs another round, which passes.
	env.RegisterDelayedCallback(func() { env.SignalWorkflow(tasks.SignalRetry, nil) }, time.Hour)

	env.ExecuteWorkflow(PipelineWorkflow, input(codePlan()))
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	parked := false
	for _, st := range s.states {
		if st.Status == tasks.RunAwaitingRetry && st.Stage == "gate" && st.Error != nil && strings.Contains(*st.Error, "after 1 fix round") {
			parked = true
		}
	}
	require.True(t, parked, "the workflow should wait for a retry after the last fix round")
	require.Len(t, e.evaluations, 3)
	require.Len(t, e.delivered, 1)
}

func TestCodeWorkflowRejectedDiffRevisesFromImplement(t *testing.T) {
	env := newEnv()
	ids, dec := approvals(4)
	dec[ids[2]] = "rejected" // the final approval of the diff
	s := &script{approvals: ids, decisions: dec}
	s.setup(env)
	e := &eng{maxRounds: 3}
	e.setup(env)

	env.ExecuteWorkflow(PipelineWorkflow, input(codePlan()))
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	var impl []StageRequest
	for _, c := range s.stageCalls {
		if c.Stage == "implement" {
			impl = append(impl, c)
		}
	}
	require.Len(t, impl, 2)
	require.Equal(t, 1, impl[1].Revision)
	require.Equal(t, "please add audit logging", impl[1].Feedback)
	require.Equal(t, 2, e.snapshots)
	require.Len(t, e.delivered, 1)
}

func TestPlansWithoutEngineeringStepsNeverTouchWorkspaces(t *testing.T) {
	env := newEnv()
	ids, dec := approvals(2)
	s := &script{approvals: ids, decisions: dec}
	s.setup(env)
	e := &eng{}
	e.setup(env)
	env.ExecuteWorkflow(PipelineWorkflow, input(engineering()))
	require.NoError(t, env.GetWorkflowError())
	require.Equal(t, 0, e.stopped+e.prepared)
}
