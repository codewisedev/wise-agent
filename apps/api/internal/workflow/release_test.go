package workflow

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"

	"wise/apps/api/internal/release"
	"wise/apps/api/internal/tasks"
)

func releasePlan() tasks.Plan {
	p := codePlan()
	p.Steps = append(p.Steps, tasks.PlanStep{ID: "release", Name: "Release", Type: tasks.StepRelease, Kind: "push", Enabled: true})
	return p
}

type rel struct {
	skip     string
	policy   string
	pushes   []PushReleaseRequest
	prs      []OpenPullRequestRequest
	reportAt int // reports built before the release step's prepare
	prepared int
}

func (r *rel) setup(env *testsuite.TestWorkflowEnvironment, s *script) {
	env.OnActivity(a.PrepareRelease, mock.Anything, mock.Anything).Return(func(context.Context, EngRequest) (ReleasePrep, error) {
		r.prepared++
		s.mu.Lock()
		r.reportAt = s.reports
		s.mu.Unlock()
		return ReleasePrep{Skip: r.skip}, nil
	})
	env.OnActivity(a.PushRelease, mock.Anything, mock.Anything).Return(func(_ context.Context, q PushReleaseRequest) (uuid.UUID, error) {
		r.pushes = append(r.pushes, q)
		return uuid.New(), nil
	})
	env.OnActivity(a.ReleasePolicy, mock.Anything, mock.Anything).Return(func(context.Context, uuid.UUID) (release.Policy, error) {
		return release.Policy{PullRequests: r.policy, Draft: true}, nil
	})
	env.OnActivity(a.OpenPullRequest, mock.Anything, mock.Anything).Return(func(_ context.Context, q OpenPullRequestRequest) error {
		r.prs = append(r.prs, q)
		return nil
	})
}

func TestReleaseAsksBeforePushAndPullRequest(t *testing.T) {
	env := newEnv()
	ids, dec := approvals(5) // requirements, architecture, gate, push, pull request
	s := &script{approvals: ids, decisions: dec}
	s.setup(env)
	e := &eng{maxRounds: 3}
	e.setup(env)
	r := &rel{policy: "ask"}
	r.setup(env, s)

	env.ExecuteWorkflow(PipelineWorkflow, input(releasePlan()))
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	require.Equal(t, 1, r.reportAt, "the final report exists before the release step (it is the PR description)")
	require.Equal(t, 1, s.reports, "built once")
	require.Equal(t, "release", s.gates[3].Gate)
	require.Equal(t, []string{"release_request"}, s.gates[3].Kinds)
	require.Len(t, r.pushes, 1)
	require.Equal(t, ids[3], r.pushes[0].ApprovalID, "the push carries only its approval")
	require.Equal(t, "release-pr", s.gates[4].Gate)
	require.Len(t, r.prs, 1)
	require.Equal(t, ids[4], *r.prs[0].ApprovalID)
	require.Equal(t, 1, e.stopped, "the sandbox stops after delivery, once")
}

func TestReleaseDeclinedPushCompletesWithoutPushing(t *testing.T) {
	env := newEnv()
	ids, dec := approvals(4)
	dec[ids[3]] = "rejected"
	s := &script{approvals: ids, decisions: dec}
	s.setup(env)
	(&eng{maxRounds: 3}).setup(env)
	r := &rel{policy: "auto"}
	r.setup(env, s)

	env.ExecuteWorkflow(PipelineWorkflow, input(releasePlan()))
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	require.Empty(t, r.pushes)
	require.Empty(t, r.prs)
	var skipped bool
	for _, st := range s.steps {
		if st.Stage == "release" && st.Status == "skipped" {
			skipped = true
		}
	}
	require.True(t, skipped)
}

func TestReleaseWithNothingDeliveredAsksNothing(t *testing.T) {
	env := newEnv()
	ids, dec := approvals(3)
	s := &script{approvals: ids, decisions: dec}
	s.setup(env)
	(&eng{maxRounds: 3}).setup(env)
	r := &rel{skip: "no changes were delivered", policy: "auto"}
	r.setup(env, s)

	env.ExecuteWorkflow(PipelineWorkflow, input(releasePlan()))
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	require.Len(t, s.gates, 3)
	require.Empty(t, r.pushes)
}

func TestAutoPolicyOpensPullRequestWithoutAsking(t *testing.T) {
	env := newEnv()
	ids, dec := approvals(4)
	s := &script{approvals: ids, decisions: dec}
	s.setup(env)
	(&eng{maxRounds: 3}).setup(env)
	r := &rel{policy: "auto"}
	r.setup(env, s)

	env.ExecuteWorkflow(PipelineWorkflow, input(releasePlan()))
	require.NoError(t, env.GetWorkflowError())
	require.Len(t, s.gates, 4)
	require.Len(t, r.prs, 1)
	require.Nil(t, r.prs[0].ApprovalID)
}
