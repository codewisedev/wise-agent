package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/google/uuid"
	"go.temporal.io/sdk/workflow"

	"wise/apps/api/internal/githost"
	"wise/apps/api/internal/gitworker"
	"wise/apps/api/internal/release"
	"wise/apps/api/internal/tasks"
)

// ReleasePrep is PrepareRelease's result.
type ReleasePrep struct {
	Skip string // nothing to push (the reason)
}

// PrepareRelease records what the push approval will cover: the delivered
// commit, the wise/ branch, the repository's remote and git account.
func (a *Activities) PrepareRelease(ctx context.Context, req EngRequest) (ReleasePrep, error) {
	if err := a.running(ctx, req); err != nil {
		return ReleasePrep{}, err
	}
	if a.Release == nil {
		return ReleasePrep{}, a.failStage(ctx, req.stage(), nil, "releasing is not configured on this installation")
	}
	p, err := a.Release.Prepare(ctx, req.TaskID)
	if err != nil {
		return ReleasePrep{}, a.failStage(ctx, req.stage(), nil, err.Error())
	}
	if p.Skip != "" {
		msg := p.Skip
		return ReleasePrep{Skip: msg}, a.Tasks.RecordStep(ctx, req.WorkflowRunID, tasks.StepUpdate{Stage: req.Stage, Attempt: max(req.Step, 1), Status: "skipped", Error: &msg})
	}
	raw, _ := json.Marshal(p.Request)
	if _, err := a.Tasks.SaveArtifact(ctx, req.WorkflowRunID, req.TaskID, release.KindRequest, raw, nil); err != nil {
		return ReleasePrep{}, err
	}
	return ReleasePrep{}, nil
}

// PushReleaseRequest pushes what a push approval approved.
type PushReleaseRequest struct {
	EngRequest
	ApprovalID uuid.UUID
}

func (a *Activities) gitClient(ctx context.Context, req EngRequest) (*gitworker.Client, error) {
	if a.Git == nil || a.Release == nil {
		return nil, a.failStage(ctx, req.stage(), nil, "the git worker is not configured on this installation")
	}
	return a.Git, nil
}

// PushRelease asks the git worker to push (it receives only the approval id)
// and records the push artifact.
func (a *Activities) PushRelease(ctx context.Context, req PushReleaseRequest) (uuid.UUID, error) {
	git, err := a.gitClient(ctx, req.EngRequest)
	if err != nil {
		return uuid.Nil, err
	}
	if err := a.running(ctx, req.EngRequest); err != nil {
		return uuid.Nil, err
	}
	if _, err := git.Push(ctx, req.ApprovalID); err != nil {
		var ge *gitworker.Error
		already := errors.As(err, &ge) && strings.Contains(ge.Msg, "already pushed")
		if !already {
			if errors.As(err, &ge) {
				return uuid.Nil, a.failStage(ctx, req.stage(), nil, ge.Msg)
			}
			return uuid.Nil, err // worker unreachable: retried
		}
	}
	p, err := a.Release.PushByApproval(ctx, req.ApprovalID)
	if err != nil {
		return uuid.Nil, err
	}
	if p.Status != "pushed" {
		msg := "the push did not complete"
		if p.Error != nil {
			msg = *p.Error
		}
		return uuid.Nil, a.failStage(ctx, req.stage(), nil, msg)
	}
	out := map[string]any{"pushId": p.ID, "remote": p.Remote, "branch": p.Branch, "sha": p.Sha, "baseBranch": p.BaseBranch, "existed": p.Existed}
	if rm, err := githost.ParseRemote(p.Remote); err == nil {
		kind := githost.GitHub
		if strings.Contains(rm.Host, "gitlab") {
			kind = githost.GitLab
		}
		out["web"] = rm.Web()
		out["compareUrl"] = githost.CompareURL(kind, rm, p.BaseBranch, p.Branch)
	}
	raw, _ := json.Marshal(out)
	art, err := a.Tasks.SaveArtifact(ctx, req.WorkflowRunID, req.TaskID, release.KindPush, raw, nil)
	if err != nil {
		return uuid.Nil, err
	}
	return p.ID, a.succeeded(ctx, req.EngRequest, &art.ID)
}

// ReleasePolicy returns the task's project pull request policy.
func (a *Activities) ReleasePolicy(ctx context.Context, taskID uuid.UUID) (release.Policy, error) {
	if a.Release == nil {
		return release.Policy{PullRequests: "off"}, nil
	}
	return a.Release.PolicyForTask(ctx, taskID)
}

// OpenPullRequestRequest opens the pull request of a push.
type OpenPullRequestRequest struct {
	EngRequest
	PushID     uuid.UUID
	ApprovalID *uuid.UUID // when the project asks before opening
}

// OpenPullRequest asks the git worker to open the pull request and records it.
func (a *Activities) OpenPullRequest(ctx context.Context, req OpenPullRequestRequest) error {
	git, err := a.gitClient(ctx, req.EngRequest)
	if err != nil {
		return err
	}
	if err := a.running(ctx, req.EngRequest); err != nil {
		return err
	}
	if _, err := git.OpenPR(ctx, req.PushID, req.ApprovalID); err != nil {
		var ge *gitworker.Error
		if !(errors.As(err, &ge) && strings.Contains(ge.Msg, "already open")) {
			if errors.As(err, &ge) {
				return a.failStage(ctx, req.stage(), nil, "pull request: "+ge.Msg)
			}
			return err
		}
	}
	pr, err := a.Release.PullRequestByPush(ctx, req.PushID)
	if err != nil {
		return err
	}
	if pr.Status != "opened" {
		msg := "the pull request was not opened"
		if pr.Error != nil {
			msg = *pr.Error
		}
		return a.failStage(ctx, req.stage(), nil, "pull request: "+msg)
	}
	raw, _ := json.Marshal(map[string]any{"pullRequestId": pr.ID, "pushId": pr.PushID, "number": pr.Number, "url": pr.Url,
		"state": pr.State, "draft": pr.Draft, "title": pr.Title})
	art, err := a.Tasks.SaveArtifact(ctx, req.WorkflowRunID, req.TaskID, release.KindPullRequest, raw, nil)
	if err != nil {
		return err
	}
	return a.succeeded(ctx, req.EngRequest, &art.ID)
}

// decide asks for an approval of the given artifact kinds and waits; it
// returns false when you decline (asks again when superseded).
func (w *pipeline) decide(ctx workflow.Context, gate string, kinds []string) (uuid.UUID, bool, error) {
	sctx := workflow.WithActivityOptions(ctx, shortOpts)
	for {
		var ap ApprovalInfo
		if err := workflow.ExecuteActivity(sctx, a.CreateApproval, ApprovalRequest{
			WorkflowRunID: w.in.WorkflowRunID, TaskID: w.in.TaskID, Gate: gate, Kinds: kinds,
		}).Get(ctx, &ap); err != nil {
			return uuid.Nil, false, err
		}
		if err := w.setState(ctx, tasks.RunAwaitingApproval, gate, nil); err != nil {
			return uuid.Nil, false, err
		}
		d, err := w.awaitDecision(ctx, ap.ID)
		if err != nil {
			return uuid.Nil, false, err
		}
		switch d.Status {
		case "approved":
			return ap.ID, true, nil
		case "rejected":
			return ap.ID, false, nil
		}
	}
}

// runRelease: push approval → push → pull request (per project policy). Declining
// the push (or the pull request) ends the step without it: the task still completes.
func (w *pipeline) runRelease(ctx workflow.Context, st tasks.PlanStep) error {
	var prep ReleasePrep
	if err := w.retrying(ctx, st, func(actx workflow.Context, req EngRequest) error {
		return workflow.ExecuteActivity(actx, a.PrepareRelease, req).Get(ctx, &prep)
	}); err != nil {
		return err
	}
	if prep.Skip != "" {
		return nil
	}
	approvalID, ok, err := w.decide(ctx, st.ID, []string{release.KindRequest})
	if err != nil {
		return err
	}
	if !ok {
		return w.step(ctx, tasks.StepUpdate{Stage: st.ID, Attempt: max(w.runs[st.ID], 1), Status: "skipped"})
	}
	var pushID uuid.UUID
	if err := w.retrying(ctx, st, func(actx workflow.Context, req EngRequest) error {
		return workflow.ExecuteActivity(actx, a.PushRelease, PushReleaseRequest{EngRequest: req, ApprovalID: approvalID}).Get(ctx, &pushID)
	}); err != nil {
		return err
	}
	var pol release.Policy
	if err := workflow.ExecuteActivity(workflow.WithActivityOptions(ctx, shortOpts), a.ReleasePolicy, w.in.TaskID).Get(ctx, &pol); err != nil {
		return err
	}
	var prApproval *uuid.UUID
	switch pol.PullRequests {
	case "auto":
	case "ask":
		id, ok, err := w.decide(ctx, st.ID+"-pr", []string{release.KindPush})
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		prApproval = &id
	default:
		return nil
	}
	return w.retrying(ctx, st, func(actx workflow.Context, req EngRequest) error {
		return workflow.ExecuteActivity(actx, a.OpenPullRequest, OpenPullRequestRequest{EngRequest: req, PushID: pushID, ApprovalID: prApproval}).Get(ctx, nil)
	})
}
