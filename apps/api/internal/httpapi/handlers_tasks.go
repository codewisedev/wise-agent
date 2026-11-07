package httpapi

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"

	"wise/apps/api/internal/apperr"
	"wise/apps/api/internal/auth"
	"wise/apps/api/internal/db/dbgen"
	"wise/apps/api/internal/httpapi/apigen"
	"wise/apps/api/internal/tasks"
	"wise/apps/api/internal/workflow"
)

// ---- converters ----

func toTask(t dbgen.Task, projectName *string) apigen.Task {
	return apigen.Task{
		Id: t.ID, ProjectId: t.ProjectID, ProjectName: projectName, Title: t.Title, Description: t.Description,
		Status: apigen.TaskStatus(t.Status), CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
	}
}

func toWorkflowRun(r dbgen.WorkflowRun) apigen.WorkflowRun {
	wfID := workflow.WorkflowID(r.ID)
	return apigen.WorkflowRun{
		Id: r.ID, TaskId: r.TaskID, WorkflowType: r.WorkflowType, Status: apigen.WorkflowRunStatus(r.Status),
		CurrentStage: r.CurrentStage, Error: r.Error, TemporalWorkflowId: &wfID,
		CreatedAt: r.CreatedAt, StartedAt: r.StartedAt, FinishedAt: r.FinishedAt, UpdatedAt: r.UpdatedAt,
	}
}

func toWorkflowStep(s dbgen.WorkflowStep) apigen.WorkflowStep {
	return apigen.WorkflowStep{
		Id: s.ID, Stage: s.Stage, Attempt: int(s.Attempt), Status: apigen.WorkflowStepStatus(s.Status),
		AgentRunId: s.AgentRunID, ArtifactId: s.ArtifactID, Error: s.Error, StartedAt: s.StartedAt, FinishedAt: s.FinishedAt,
	}
}

func toWorkflowEvent(e dbgen.WorkflowEvent) apigen.WorkflowEvent {
	return apigen.WorkflowEvent{WorkflowRunId: e.WorkflowRunID, Seq: int(e.Seq), Type: e.Type, Payload: jsonObject(e.Payload), CreatedAt: e.CreatedAt}
}

func toArtifact(a dbgen.Artifact) apigen.Artifact {
	return apigen.Artifact{
		Id: a.ID, TaskId: a.TaskID, WorkflowRunId: a.WorkflowRunID, Kind: apigen.ArtifactKind(a.Kind), Version: int(a.Version),
		Content: jsonObject(a.Content), ContentHash: a.ContentHash, AgentRunId: a.AgentRunID, CreatedAt: a.CreatedAt,
	}
}

func toApproval(a dbgen.Approval) apigen.Approval {
	var refs []tasks.ArtifactRef
	_ = json.Unmarshal(a.Artifacts, &refs)
	out := apigen.Approval{
		Id: a.ID, WorkflowRunId: a.WorkflowRunID, TaskId: a.TaskID, Gate: apigen.ApprovalGate(a.Gate), Round: int(a.Round),
		Status: apigen.ApprovalStatus(a.Status), CombinedHash: a.CombinedHash, Comment: a.Comment,
		DecidedBy: a.DecidedBy, DecidedAt: a.DecidedAt, CreatedAt: a.CreatedAt,
		Artifacts: make([]apigen.ArtifactRef, 0, len(refs)), Notes: []string{},
	}
	for _, r := range refs {
		id, _ := uuid.Parse(r.ArtifactID)
		out.Artifacts = append(out.Artifacts, apigen.ArtifactRef{ArtifactId: id, Kind: apigen.ArtifactKind(r.Kind), Version: int(r.Version), Hash: r.Hash})
	}
	_ = json.Unmarshal(a.Notes, &out.Notes)
	return out
}

func toTaskDetail(d tasks.Detail) apigen.TaskDetail {
	out := apigen.TaskDetail{
		Task:      toTask(d.Task, nil),
		Stages:    tasks.Stages,
		Steps:     mapSlice(d.Steps, toWorkflowStep),
		Artifacts: mapSlice(d.Artifacts, toArtifact),
		Approvals: mapSlice(d.Approvals, toApproval),
	}
	if d.Workflow != nil {
		w := toWorkflowRun(*d.Workflow)
		out.Workflow = &w
	}
	return out
}

// requireSession allows only interactive sign-ins: decisions that steer a
// workflow (approve, reject, retry, cancel) must not be made by API tokens.
func requireSession(ctx context.Context, action string) (auth.Principal, error) {
	p, err := principal(ctx)
	if err != nil {
		return p, err
	}
	if p.Method != auth.MethodSession {
		return p, apperr.Forbidden(action + " requires a signed-in session; API tokens cannot do this")
	}
	return p, nil
}

// ---- tasks ----

func (s *Server) CreateTask(ctx context.Context, req apigen.CreateTaskRequestObject) (apigen.CreateTaskResponseObject, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	start := req.Body.Start == nil || *req.Body.Start
	userID := p.User.ID
	task, _, err := s.Tasks.CreateTask(ctx, req.ProjectId, req.Body.Title, deref(req.Body.Description), start, &userID)
	if err != nil {
		return nil, err
	}
	d, err := s.Tasks.GetTask(ctx, task.ID)
	if err != nil {
		return nil, err
	}
	return apigen.CreateTask201JSONResponse(toTaskDetail(d)), nil
}

func (s *Server) ListTasks(ctx context.Context, req apigen.ListTasksRequestObject) (apigen.ListTasksResponseObject, error) {
	var status *string
	if req.Params.Status != nil {
		v := string(*req.Params.Status)
		status = &v
	}
	rows, err := s.Tasks.ListTasks(ctx, req.Params.ProjectId, status)
	if err != nil {
		return nil, err
	}
	items := make([]apigen.Task, len(rows))
	for i, r := range rows {
		name := r.ProjectName
		items[i] = toTask(dbgen.Task{
			ID: r.ID, ProjectID: r.ProjectID, Title: r.Title, Description: r.Description, Status: r.Status,
			CreatedBy: r.CreatedBy, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
		}, &name)
	}
	return apigen.ListTasks200JSONResponse{Items: items}, nil
}

func (s *Server) GetTask(ctx context.Context, req apigen.GetTaskRequestObject) (apigen.GetTaskResponseObject, error) {
	d, err := s.Tasks.GetTask(ctx, req.TaskId)
	if err != nil {
		return nil, err
	}
	return apigen.GetTask200JSONResponse(toTaskDetail(d)), nil
}

func (s *Server) CancelTask(ctx context.Context, req apigen.CancelTaskRequestObject) (apigen.CancelTaskResponseObject, error) {
	if _, err := requireSession(ctx, "cancelling a task"); err != nil {
		return nil, err
	}
	t, err := s.Tasks.CancelTask(ctx, req.TaskId)
	if err != nil {
		return nil, err
	}
	return apigen.CancelTask202JSONResponse(toTask(t, nil)), nil
}

func (s *Server) ListTaskArtifacts(ctx context.Context, req apigen.ListTaskArtifactsRequestObject) (apigen.ListTaskArtifactsResponseObject, error) {
	var kind *string
	if req.Params.Kind != nil {
		v := string(*req.Params.Kind)
		kind = &v
	}
	arts, err := s.Tasks.ListArtifacts(ctx, req.TaskId, kind)
	if err != nil {
		return nil, err
	}
	return apigen.ListTaskArtifacts200JSONResponse{Items: mapSlice(arts, toArtifact)}, nil
}

func (s *Server) GetArtifact(ctx context.Context, req apigen.GetArtifactRequestObject) (apigen.GetArtifactResponseObject, error) {
	a, err := s.Tasks.GetArtifact(ctx, req.ArtifactId)
	if err != nil {
		return nil, err
	}
	return apigen.GetArtifact200JSONResponse(toArtifact(a)), nil
}

// ---- workflows ----

func (s *Server) StartTaskWorkflow(ctx context.Context, req apigen.StartTaskWorkflowRequestObject) (apigen.StartTaskWorkflowResponseObject, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	userID := p.User.ID
	run, err := s.Tasks.StartWorkflow(ctx, req.TaskId, &userID)
	if err != nil {
		return nil, err
	}
	return apigen.StartTaskWorkflow202JSONResponse(toWorkflowRun(run)), nil
}

func (s *Server) GetWorkflow(ctx context.Context, req apigen.GetWorkflowRequestObject) (apigen.GetWorkflowResponseObject, error) {
	run, steps, err := s.Tasks.GetWorkflow(ctx, req.WorkflowId)
	if err != nil {
		return nil, err
	}
	return apigen.GetWorkflow200JSONResponse{Workflow: toWorkflowRun(run), Steps: mapSlice(steps, toWorkflowStep)}, nil
}

func (s *Server) ListWorkflowEvents(ctx context.Context, req apigen.ListWorkflowEventsRequestObject) (apigen.ListWorkflowEventsResponseObject, error) {
	after := 0
	if req.Params.AfterSeq != nil {
		after = *req.Params.AfterSeq
	}
	evs, err := s.Tasks.ListWorkflowEvents(ctx, req.WorkflowId, int32(after))
	if err != nil {
		return nil, err
	}
	return apigen.ListWorkflowEvents200JSONResponse{Items: mapSlice(evs, toWorkflowEvent)}, nil
}

func (s *Server) RetryWorkflow(ctx context.Context, req apigen.RetryWorkflowRequestObject) (apigen.RetryWorkflowResponseObject, error) {
	if _, err := requireSession(ctx, "retrying a workflow"); err != nil {
		return nil, err
	}
	run, err := s.Tasks.RetryWorkflow(ctx, req.WorkflowId)
	if err != nil {
		return nil, err
	}
	return apigen.RetryWorkflow202JSONResponse(toWorkflowRun(run)), nil
}

// ---- approvals ----

func (s *Server) ListApprovals(ctx context.Context, req apigen.ListApprovalsRequestObject) (apigen.ListApprovalsResponseObject, error) {
	var status *string
	if req.Params.Status != nil {
		v := string(*req.Params.Status)
		status = &v
	}
	rows, err := s.Tasks.ListApprovals(ctx, status, nil)
	if err != nil {
		return nil, err
	}
	pending, err := s.DB.CountPendingApprovals(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]apigen.Approval, len(rows))
	for i, r := range rows {
		a := toApproval(r.Approval)
		title, pid, pname := r.TaskTitle, r.ProjectID, r.ProjectName
		a.TaskTitle, a.ProjectId, a.ProjectName = &title, &pid, &pname
		items[i] = a
	}
	return apigen.ListApprovals200JSONResponse{Items: items, PendingCount: int(pending)}, nil
}

func (s *Server) GetApproval(ctx context.Context, req apigen.GetApprovalRequestObject) (apigen.GetApprovalResponseObject, error) {
	ap, err := s.Tasks.GetApproval(ctx, req.ApprovalId)
	if err != nil {
		return nil, err
	}
	out := apigen.ApprovalDetail{Approval: toApproval(ap), Artifacts: []apigen.Artifact{}}
	for _, ref := range out.Approval.Artifacts {
		a, err := s.Tasks.GetArtifact(ctx, ref.ArtifactId)
		if err != nil {
			return nil, err
		}
		out.Artifacts = append(out.Artifacts, toArtifact(a))
	}
	return apigen.GetApproval200JSONResponse(out), nil
}

func (s *Server) ApproveApproval(ctx context.Context, req apigen.ApproveApprovalRequestObject) (apigen.ApproveApprovalResponseObject, error) {
	p, err := requireSession(ctx, "approving")
	if err != nil {
		return nil, err
	}
	userID := p.User.ID
	ap, err := s.Tasks.Decide(ctx, req.ApprovalId, true, req.Body.CombinedHash, deref(req.Body.Comment), &userID)
	if err != nil {
		return nil, err
	}
	return apigen.ApproveApproval200JSONResponse(toApproval(ap)), nil
}

func (s *Server) RejectApproval(ctx context.Context, req apigen.RejectApprovalRequestObject) (apigen.RejectApprovalResponseObject, error) {
	p, err := requireSession(ctx, "rejecting")
	if err != nil {
		return nil, err
	}
	userID := p.User.ID
	ap, err := s.Tasks.Decide(ctx, req.ApprovalId, false, req.Body.CombinedHash, deref(req.Body.Comment), &userID)
	if err != nil {
		return nil, err
	}
	return apigen.RejectApproval200JSONResponse(toApproval(ap)), nil
}
