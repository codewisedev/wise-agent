package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.temporal.io/sdk/activity"

	"wise/apps/api/internal/apperr"
	"wise/apps/api/internal/db/dbgen"
	"wise/apps/api/internal/executor"
	"wise/apps/api/internal/tasks"
	"wise/apps/api/internal/workspaces"
)

// EngRequest runs one engineering step (workspace, quality, gate, deliver).
type EngRequest struct {
	WorkflowRunID uuid.UUID
	TaskID        uuid.UUID
	CreatedBy     *uuid.UUID
	Stage         string
	Step          int32
	Kinds         []string // gate: the kinds evaluated (quality report, reviews)
	Round         int      // gate: fix round (0 = first evaluation)
}

func (r EngRequest) stage() StageRequest {
	return StageRequest{WorkflowRunID: r.WorkflowRunID, TaskID: r.TaskID, Stage: r.Stage, Step: r.Step, Attempt: 1}
}

func (a *Activities) ws() (*workspaces.Service, error) {
	if a.Workspaces == nil || !a.Workspaces.Available() {
		return nil, errors.New("the executor is not configured")
	}
	return a.Workspaces, nil
}

func beat(ctx context.Context) func() {
	return func() { activity.RecordHeartbeat(ctx) }
}

// keepBeating heartbeats every few seconds until the returned stop is called
// (for calls without their own progress callback).
func keepBeating(ctx context.Context) func() {
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				activity.RecordHeartbeat(ctx)
			}
		}
	}()
	return func() { close(done) }
}

func (a *Activities) running(ctx context.Context, req EngRequest) error {
	return a.Tasks.RecordStep(ctx, req.WorkflowRunID, tasks.StepUpdate{Stage: req.Stage, Attempt: max(req.Step, 1), Status: "running"})
}

func (a *Activities) succeeded(ctx context.Context, req EngRequest, artifactID *uuid.UUID) error {
	return a.Tasks.RecordStep(ctx, req.WorkflowRunID, tasks.StepUpdate{Stage: req.Stage, Attempt: max(req.Step, 1), Status: "succeeded", ArtifactID: artifactID})
}

// humanError turns a client error (bad settings, missing setup) into a stage
// failure that waits for a human retry; other errors are retried.
func (a *Activities) humanError(ctx context.Context, req EngRequest, err error) error {
	var ee *executor.Error
	if e, ok := apperr.As(err); ok && e.Status < 500 {
		return a.failStage(ctx, req.stage(), nil, e.Detail)
	}
	if errors.As(err, &ee) && ee.Status < 500 {
		return a.failStage(ctx, req.stage(), nil, ee.Msg)
	}
	return err
}

// liveWorkspace is the task's ready workspace.
func (a *Activities) liveWorkspace(ctx context.Context, req EngRequest) (dbgen.Workspace, error) {
	w, err := a.Workspaces.ForTask(ctx, req.TaskID)
	if err != nil {
		return dbgen.Workspace{}, err
	}
	if w == nil || (w.Status != "ready" && w.Status != "stopped") {
		return dbgen.Workspace{}, a.failStage(ctx, req.stage(), nil, "the task has no ready workspace; retry the workspace step")
	}
	return *w, nil
}

// PrepareWorkspace prepares the task's workspace and waits until it is ready.
func (a *Activities) PrepareWorkspace(ctx context.Context, req EngRequest) error {
	ws, err := a.ws()
	if err != nil {
		return a.failStage(ctx, req.stage(), nil, err.Error())
	}
	if err := ws.Reload(ctx); err != nil {
		return err
	}
	if err := a.running(ctx, req); err != nil {
		return err
	}
	runID := req.WorkflowRunID
	w, err := ws.Start(ctx, req.TaskID, &runID, req.CreatedBy)
	if err != nil {
		return a.humanError(ctx, req, err)
	}
	w, err = ws.Track(ctx, w, beat(ctx))
	if err != nil {
		return err
	}
	if w.Status != "ready" {
		msg := "the workspace could not be prepared"
		if w.Error != nil {
			msg += ": " + *w.Error
		}
		return a.failStage(ctx, req.stage(), nil, msg)
	}
	return a.succeeded(ctx, req, nil)
}

// QualityCheck is one check in a quality report.
type QualityCheck struct {
	Name       string `json:"name"`
	Command    string `json:"command"`
	Status     string `json:"status"` // passed | failed | timed_out | error
	ExitCode   *int   `json:"exitCode,omitempty"`
	DurationMs int64  `json:"durationMs"`
	Output     string `json:"output"`
}

// QualityReport is the quality step's artifact.
type QualityReport struct {
	Passed bool           `json:"passed"`
	Checks []QualityCheck `json:"checks"`
	Note   string         `json:"note,omitempty"`
}

// RunQuality runs the repository's checks in the sandbox and stores the
// report. Failing checks do not fail the step (the gate decides).
func (a *Activities) RunQuality(ctx context.Context, req EngRequest) error {
	ws, err := a.ws()
	if err != nil {
		return a.failStage(ctx, req.stage(), nil, err.Error())
	}
	if err := ws.Reload(ctx); err != nil {
		return err
	}
	if err := a.running(ctx, req); err != nil {
		return err
	}
	w, err := a.liveWorkspace(ctx, req)
	if err != nil {
		return err
	}
	rs, _, err := ws.RepoSettings(ctx, w.RepositoryID)
	if err != nil {
		return err
	}
	rep := QualityReport{Passed: true, Checks: []QualityCheck{}}
	cmds := rs.Commands.List()
	if len(cmds) == 0 {
		rep.Note = "the repository has no quality commands configured; nothing was checked"
	}
	budget := max(ws.Settings().MaxOutputKB, 16) << 10
	for _, c := range cmds {
		res, err := ws.CommandBeat(ctx, w, c[1], 0, budget, beat(ctx))
		if err != nil {
			return a.humanError(ctx, req, err)
		}
		chk := QualityCheck{Name: c[0], Command: c[1], ExitCode: res.ExitCode, DurationMs: res.DurationMs, Output: res.Output}
		switch {
		case res.TimedOut:
			chk.Status = "timed_out"
		case res.Status != executor.JobSucceeded:
			chk.Status = "error"
		case res.ExitCode != nil && *res.ExitCode == 0:
			chk.Status = "passed"
		default:
			chk.Status = "failed"
		}
		if chk.Status != "passed" {
			rep.Passed = false
		}
		rep.Checks = append(rep.Checks, chk)
	}
	raw, _ := json.Marshal(rep)
	art, err := a.Tasks.SaveArtifact(ctx, req.WorkflowRunID, req.TaskID, tasks.KindQualityReport, raw, nil)
	if err != nil {
		return err
	}
	return a.succeeded(ctx, req, &art.ID)
}

// GateResult is the gate's verdict for one round.
type GateResult struct {
	Passed    bool
	Summary   string
	Feedback  string // for the implementing agent
	MaxRounds int
}

type finding struct {
	Severity   string `json:"severity"`
	File       string `json:"file"`
	Line       int    `json:"line"`
	Title      string `json:"title"`
	Detail     string `json:"detail"`
	Suggestion string `json:"suggestion"`
}

type review struct {
	Verdict  string    `json:"verdict"`
	Summary  string    `json:"summary"`
	Findings []finding `json:"findings"`
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}

// EvaluateGate checks the latest quality report and reviews and records the
// round's verdict.
func (a *Activities) EvaluateGate(ctx context.Context, req EngRequest) (GateResult, error) {
	ws, err := a.ws()
	if err != nil {
		return GateResult{}, a.failStage(ctx, req.stage(), nil, err.Error())
	}
	if err := ws.Reload(ctx); err != nil {
		return GateResult{}, err
	}
	if err := a.running(ctx, req); err != nil {
		return GateResult{}, err
	}
	arts, err := a.Tasks.LatestArtifacts(ctx, req.TaskID, req.Kinds...)
	if err != nil {
		return GateResult{}, err
	}
	res := GateResult{Passed: true, MaxRounds: ws.Settings().MaxFixRounds}
	var fb, sum strings.Builder
	for _, k := range req.Kinds {
		art, ok := arts[k]
		if !ok {
			continue
		}
		if k == tasks.KindQualityReport {
			var q QualityReport
			if json.Unmarshal(art.Content, &q) != nil {
				continue
			}
			failed := 0
			for _, c := range q.Checks {
				if c.Status == "passed" {
					continue
				}
				failed++
				code := "?"
				if c.ExitCode != nil {
					code = fmt.Sprint(*c.ExitCode)
				}
				fmt.Fprintf(&fb, "### Check %q failed (%s, exit %s)\n$ %s\n```\n%s\n```\n\n", c.Name, c.Status, code, c.Command, tail(c.Output, 4000))
			}
			if failed > 0 {
				res.Passed = false
				fmt.Fprintf(&sum, "%d of %d checks failed. ", failed, len(q.Checks))
			} else {
				fmt.Fprintf(&sum, "%d checks passed. ", len(q.Checks))
			}
			continue
		}
		var r review
		if json.Unmarshal(art.Content, &r) != nil || r.Verdict == "" {
			continue // not a review
		}
		blocking := 0
		for _, f := range r.Findings {
			if f.Severity != "critical" && f.Severity != "major" {
				continue
			}
			blocking++
			loc := f.File
			if f.Line > 0 {
				loc = fmt.Sprintf("%s:%d", f.File, f.Line)
			}
			fmt.Fprintf(&fb, "### %s (%s, %s) — %s\n%s\n", f.Title, f.Severity, k, loc, f.Detail)
			if f.Suggestion != "" {
				fmt.Fprintf(&fb, "Suggested fix: %s\n", f.Suggestion)
			}
			fb.WriteString("\n")
		}
		if r.Verdict != "pass" || blocking > 0 {
			res.Passed = false
			fmt.Fprintf(&sum, "%s requests changes (%d blocking). ", strings.ReplaceAll(k, "_", " "), blocking)
		} else {
			fmt.Fprintf(&sum, "%s passed. ", strings.ReplaceAll(k, "_", " "))
		}
	}
	res.Summary = strings.TrimSpace(sum.String())
	res.Feedback = strings.TrimSpace(fb.String())
	if res.Summary == "" {
		res.Summary = "nothing to check"
	}
	rec, _ := json.Marshal(map[string]any{"round": req.Round, "passed": res.Passed, "summary": res.Summary, "feedback": res.Feedback})
	art, err := a.Tasks.SaveArtifact(ctx, req.WorkflowRunID, req.TaskID, tasks.KindGate, rec, nil)
	if err != nil {
		return GateResult{}, err
	}
	// Close this evaluation's step row: failed when the round failed (a fix
	// round follows), succeeded when it passed.
	u := tasks.StepUpdate{Stage: req.Stage, Attempt: max(req.Step, 1), Status: "succeeded", ArtifactID: &art.ID}
	if !res.Passed {
		u.Status, u.Error = "failed", &res.Summary
	}
	if err := a.Tasks.RecordStep(ctx, req.WorkflowRunID, u); err != nil {
		return GateResult{}, err
	}
	return res, nil
}

// QAFeedback formats the latest diagnosis for the implementing agent.
func (a *Activities) QAFeedback(ctx context.Context, taskID uuid.UUID) (string, error) {
	arts, err := a.Tasks.LatestArtifacts(ctx, taskID, tasks.KindQA)
	if err != nil {
		return "", err
	}
	art, ok := arts[tasks.KindQA]
	if !ok {
		return "", nil
	}
	var q struct {
		Summary string `json:"summary"`
		Fixes   []struct {
			Problem, Cause, Instruction, File string
		} `json:"fixes"`
		MissingTests []string `json:"missing_tests"`
	}
	if json.Unmarshal(art.Content, &q) != nil {
		return "", nil
	}
	var b strings.Builder
	b.WriteString("## Diagnosis\n" + q.Summary + "\n")
	for i, f := range q.Fixes {
		fmt.Fprintf(&b, "%d. %s\n   Cause: %s\n   Fix: %s\n", i+1, f.Problem, f.Cause, f.Instruction)
	}
	if len(q.MissingTests) > 0 {
		b.WriteString("Missing tests:\n- " + strings.Join(q.MissingTests, "\n- ") + "\n")
	}
	return b.String(), nil
}

// SnapshotResult is the stored changeset and whether it carries credentials.
type SnapshotResult struct {
	ArtifactID  uuid.UUID
	SecretFound bool
	Secrets     []string // credential-like files ("" when only a token was found in the diff)
	Protected   []string // changed files under a protected path (execution setting)
}

// Snapshot commits the workspace's changes and stores the verified changeset.
func (a *Activities) Snapshot(ctx context.Context, req EngRequest) (SnapshotResult, error) {
	ws, err := a.ws()
	if err != nil {
		return SnapshotResult{}, a.failStage(ctx, req.stage(), nil, err.Error())
	}
	if err := ws.Reload(ctx); err != nil {
		return SnapshotResult{}, err
	}
	w, err := a.liveWorkspace(ctx, req)
	if err != nil {
		return SnapshotResult{}, err
	}
	info, err := a.Tasks.StartInfo(ctx, req.WorkflowRunID)
	if err != nil {
		return SnapshotResult{}, err
	}
	msg := info.Task.Title
	if arts, err := a.Tasks.LatestArtifacts(ctx, req.TaskID, tasks.KindImplementation); err == nil {
		var impl struct {
			CommitMessage string `json:"commit_message"`
		}
		if art, ok := arts[tasks.KindImplementation]; ok && json.Unmarshal(art.Content, &impl) == nil && strings.TrimSpace(impl.CommitMessage) != "" {
			msg = commitMessage(impl.CommitMessage)
		}
	}
	msg += fmt.Sprintf("\n\nWise-Task: %s (%s)", info.Task.Title, req.TaskID.String()[:8])
	stop := keepBeating(ctx)
	cs, err := ws.Snapshot(ctx, w, msg)
	stop()
	if err != nil {
		return SnapshotResult{}, a.humanError(ctx, req, err)
	}
	raw, _ := json.Marshal(cs)
	art, err := a.Tasks.SaveArtifact(ctx, req.WorkflowRunID, req.TaskID, tasks.KindChangeset, raw, nil)
	if err != nil {
		return SnapshotResult{}, err
	}
	res := SnapshotResult{ArtifactID: art.ID, SecretFound: cs.SecretFound}
	var paths []string
	for _, f := range cs.Files {
		if f.Secret {
			res.Secrets = append(res.Secrets, f.Path)
		}
		paths = append(paths, f.Path)
		if f.OldPath != "" {
			paths = append(paths, f.OldPath)
		}
	}
	res.Protected = workspaces.Protected(ws.Settings().ProtectedPaths, paths)
	if res.SecretFound || len(res.Protected) > 0 {
		msg := "the change contains credentials or touches protected paths; a fix round undoes that"
		return res, a.Tasks.RecordStep(ctx, req.WorkflowRunID, tasks.StepUpdate{Stage: req.Stage, Attempt: max(req.Step, 1), Status: "failed", ArtifactID: &art.ID, Error: &msg})
	}
	return res, a.succeeded(ctx, req, &art.ID)
}

// DeliverRequest delivers the changeset approved at Gate.
type DeliverRequest struct {
	EngRequest
	Gate string
}

// Deliver writes the approved commit into the user's repository as the
// task's branch. It refuses anything but the exact approved changeset.
func (a *Activities) Deliver(ctx context.Context, req DeliverRequest) error {
	ws, err := a.ws()
	if err != nil {
		return a.failStage(ctx, req.stage(), nil, err.Error())
	}
	if err := a.running(ctx, req.EngRequest); err != nil {
		return err
	}
	art, approvalID, err := a.Tasks.ApprovedArtifact(ctx, req.WorkflowRunID, req.TaskID, req.Gate, tasks.KindChangeset)
	if err != nil {
		return a.failStage(ctx, req.stage(), nil, err.Error())
	}
	var cs executor.Changeset
	if err := json.Unmarshal(art.Content, &cs); err != nil {
		return err
	}
	out := map[string]any{"changesetId": art.ID, "approvalId": approvalID}
	if cs.HeadSHA == "" {
		out["note"] = "no changes to deliver"
	} else {
		w, err := a.liveWorkspace(ctx, req.EngRequest)
		if err != nil {
			return err
		}
		w, err = ws.Deliver(ctx, w, cs.HeadSHA, &approvalID)
		if err != nil {
			return a.humanError(ctx, req.EngRequest, err)
		}
		out["branch"], out["sha"] = *w.DeliveredBranch, *w.DeliveredSha
	}
	raw, _ := json.Marshal(out)
	d, err := a.Tasks.SaveArtifact(ctx, req.WorkflowRunID, req.TaskID, tasks.KindDelivery, raw, nil)
	if err != nil {
		return err
	}
	return a.succeeded(ctx, req.EngRequest, &d.ID)
}

// StopWorkspace stops the task's sandbox when the workflow ends (files stay
// until the retention period).
func (a *Activities) StopWorkspace(ctx context.Context, taskID uuid.UUID) error {
	if a.Workspaces == nil || !a.Workspaces.Available() {
		return nil
	}
	// Best effort: a sandbox left running is removed with its workspace later.
	w, err := a.Workspaces.ForTask(ctx, taskID)
	if err != nil || w == nil || w.Status != "ready" {
		return nil
	}
	if err := a.Workspaces.StopSandbox(ctx, *w); err != nil {
		activity.GetLogger(ctx).Warn("stop workspace sandbox", "workspace", w.ID, "err", err)
	}
	return nil
}

// commitMessage cleans an agent's commit message: some agents escape the
// newlines inside their JSON report ("\\n" arrives as backslash-n).
func commitMessage(m string) string {
	m = strings.TrimSpace(m)
	if !strings.Contains(m, "\n") && strings.Contains(m, `\n`) {
		m = strings.ReplaceAll(m, `\n`, "\n")
	}
	return m
}
