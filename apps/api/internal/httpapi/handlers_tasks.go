package httpapi

import (
	"context"
	"encoding/json"
	openapi_types "github.com/oapi-codegen/runtime/types"

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
	out := apigen.Task{
		Id: t.ID, ProjectId: t.ProjectID, ProjectName: projectName, Title: t.Title, Description: t.Description,
		Status: apigen.TaskStatus(t.Status), CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
	}
	taskFields(&out, t)
	return out
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
		Plan:      toPlan(d.Plan),
		Issues:    toIssues(d.Issues),
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
	opts := tasks.CreateOptions{
		Start: req.Body.Start == nil || *req.Body.Start, StepByStep: deref(req.Body.StepByStep), TemplateID: req.Body.TemplateId,
	}
	if req.Body.Column != nil {
		col := *req.Body.Column
		opts.Column = &col
	}
	opts.Sprint = req.Body.SprintId
	if req.Body.Priority != nil {
		opts.Priority = string(*req.Body.Priority)
	}
	if req.Body.Labels != nil {
		opts.Labels = *req.Body.Labels
	}
	userID := p.User.ID
	task, _, err := s.Tasks.CreateTask(ctx, req.ProjectId, req.Body.Title, deref(req.Body.Description), opts, &userID)
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
	rows, err := s.Tasks.ListTasks(ctx, req.Params.ProjectId, status, req.Params.Archived != nil && *req.Params.Archived)
	if err != nil {
		return nil, err
	}
	items := make([]apigen.Task, len(rows))
	for i, r := range rows {
		name := r.ProjectName
		items[i] = toTask(dbgen.Task{
			ID: r.ID, ProjectID: r.ProjectID, Title: r.Title, Description: r.Description, Status: r.Status,
			CreatedBy: r.CreatedBy, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, TemplateID: r.TemplateID,
			ColumnID: r.ColumnID, Position: r.Position, SprintID: r.SprintID, ArchivedAt: r.ArchivedAt, Priority: r.Priority, Labels: r.Labels, Source: r.Source,
			ExternalID: r.ExternalID, ExternalUrl: r.ExternalUrl, ExternalStatus: r.ExternalStatus,
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

// ---- board ----

func taskFields(out *apigen.Task, t dbgen.Task) {
	col := t.ColumnID
	pri := apigen.TaskPriority(t.Priority)
	src := apigen.TaskSource(t.Source)
	labels := t.Labels
	if labels == nil {
		labels = []string{}
	}
	pos := t.Position
	out.Column, out.Priority, out.Source, out.Labels, out.Position = &col, &pri, &src, &labels, &pos
	out.ExternalId, out.ExternalUrl, out.ExternalStatus, out.TemplateId = t.ExternalID, t.ExternalUrl, t.ExternalStatus, t.TemplateID
	out.SprintId, out.ArchivedAt, out.TargetRepositoryId = t.SprintID, t.ArchivedAt, t.TargetRepositoryID
}

func (s *Server) GetBoard(ctx context.Context, req apigen.GetBoardRequestObject) (apigen.GetBoardResponseObject, error) {
	rows, err := s.Tasks.Board(ctx, req.ProjectId)
	if err != nil {
		return nil, err
	}
	cols, err := s.Tasks.Columns(ctx, req.ProjectId)
	if err != nil {
		return nil, err
	}
	byCol := map[uuid.UUID][]apigen.BoardCard{}
	for _, r := range rows {
		card := apigen.BoardCard{PendingApproval: r.PendingApproval, Task: toTask(dbgen.Task{
			ID: r.ID, ProjectID: r.ProjectID, Title: r.Title, Description: r.Description, Status: r.Status,
			CreatedBy: r.CreatedBy, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, TemplateID: r.TemplateID,
			ColumnID: r.ColumnID, Position: r.Position, SprintID: r.SprintID, ArchivedAt: r.ArchivedAt, Priority: r.Priority, Labels: r.Labels, Source: r.Source,
			IntegrationID: r.IntegrationID, ExternalID: r.ExternalID, ExternalUrl: r.ExternalUrl, ExternalStatus: r.ExternalStatus,
		}, nil)}
		if r.WorkflowID != uuid.Nil {
			id, st := r.WorkflowID, apigen.WorkflowRunStatus(r.WorkflowStatus)
			card.WorkflowId, card.WorkflowStatus, card.WorkflowStage = &id, &st, r.WorkflowStage
			if r.TemplateName != "" {
				name := r.TemplateName
				card.TemplateName = &name
			}
		}
		byCol[r.ColumnID] = append(byCol[r.ColumnID], card)
	}
	out := apigen.GetBoard200JSONResponse{Columns: []struct {
		Cards     []apigen.BoardCard `json:"cards"`
		Color     *string            `json:"color,omitempty"`
		Id        openapi_types.UUID `json:"id"`
		Name      string             `json:"name"`
		Position  float64            `json:"position"`
		ProjectId openapi_types.UUID `json:"projectId"`
		Role      *apigen.ColumnRole `json:"role,omitempty"`
		WipLimit  *int               `json:"wipLimit,omitempty"`
	}{}}
	for _, c := range cols {
		bc := toBoardColumn(c)
		cards := byCol[c.ID]
		if cards == nil {
			cards = []apigen.BoardCard{}
		}
		out.Columns = append(out.Columns, struct {
			Cards     []apigen.BoardCard `json:"cards"`
			Color     *string            `json:"color,omitempty"`
			Id        openapi_types.UUID `json:"id"`
			Name      string             `json:"name"`
			Position  float64            `json:"position"`
			ProjectId openapi_types.UUID `json:"projectId"`
			Role      *apigen.ColumnRole `json:"role,omitempty"`
			WipLimit  *int               `json:"wipLimit,omitempty"`
		}{Cards: cards, Color: bc.Color, Id: bc.Id, Name: bc.Name, Position: bc.Position, ProjectId: bc.ProjectId, Role: bc.Role, WipLimit: bc.WipLimit})
	}
	return out, nil
}

func (s *Server) UpdateTask(ctx context.Context, req apigen.UpdateTaskRequestObject) (apigen.UpdateTaskResponseObject, error) {
	b := req.Body
	p := tasks.CardPatch{Title: b.Title, Description: b.Description, Position: b.Position}
	if b.Priority != nil {
		v := string(*b.Priority)
		p.Priority = &v
	}
	if b.Column != nil {
		v := *b.Column
		p.Column = &v
	}
	if b.Sprint != nil {
		p.SetSprint = true
		if *b.Sprint != "none" && *b.Sprint != "" {
			id, err := uuid.Parse(*b.Sprint)
			if err != nil {
				return nil, apperr.Validation(apperr.FieldError{Field: "sprint", Message: "a sprint id or \"none\""})
			}
			p.Sprint = &id
		}
	}
	if b.Labels != nil {
		p.Labels = *b.Labels
		if p.Labels == nil {
			p.Labels = []string{}
		}
	}
	if b.Repository != nil {
		if err := s.setTaskRepository(ctx, req.TaskId, *b.Repository); err != nil {
			return nil, err
		}
	}
	t, err := s.Tasks.UpdateCard(ctx, req.TaskId, p)
	if err != nil {
		return nil, err
	}
	return apigen.UpdateTask200JSONResponse(toTask(t, nil)), nil
}

// ---- board columns ----

func toBoardColumn(c dbgen.BoardColumn) apigen.BoardColumn {
	out := apigen.BoardColumn{Id: c.ID, ProjectId: c.ProjectID, Name: c.Name, Position: c.Position, Color: c.Color}
	if c.Role != nil {
		r := apigen.ColumnRole(*c.Role)
		out.Role = &r
	}
	if c.WipLimit != nil {
		w := int(*c.WipLimit)
		out.WipLimit = &w
	}
	return out
}

func columnInput(b *apigen.ColumnInput) tasks.ColumnInput {
	in := tasks.ColumnInput{Name: b.Name, Color: b.Color, Position: b.Position}
	if b.Role != nil {
		r := string(*b.Role)
		in.Role = &r
	}
	if b.WipLimit != nil {
		w := int32(*b.WipLimit)
		in.WIPLimit = &w
	}
	return in
}

func (s *Server) ListColumns(ctx context.Context, req apigen.ListColumnsRequestObject) (apigen.ListColumnsResponseObject, error) {
	cols, err := s.Tasks.Columns(ctx, req.ProjectId)
	if err != nil {
		return nil, err
	}
	return apigen.ListColumns200JSONResponse{Items: mapSlice(cols, toBoardColumn)}, nil
}

func (s *Server) CreateColumn(ctx context.Context, req apigen.CreateColumnRequestObject) (apigen.CreateColumnResponseObject, error) {
	c, err := s.Tasks.CreateColumn(ctx, req.ProjectId, columnInput(req.Body))
	if err != nil {
		return nil, err
	}
	return apigen.CreateColumn201JSONResponse(toBoardColumn(c)), nil
}

func (s *Server) UpdateColumn(ctx context.Context, req apigen.UpdateColumnRequestObject) (apigen.UpdateColumnResponseObject, error) {
	c, err := s.Tasks.UpdateColumn(ctx, req.ColumnId, columnInput(req.Body))
	if err != nil {
		return nil, err
	}
	return apigen.UpdateColumn200JSONResponse(toBoardColumn(c)), nil
}

func (s *Server) DeleteColumn(ctx context.Context, req apigen.DeleteColumnRequestObject) (apigen.DeleteColumnResponseObject, error) {
	if err := s.Tasks.DeleteColumn(ctx, req.ColumnId, req.Params.MoveTo); err != nil {
		return nil, err
	}
	return apigen.DeleteColumn204Response{}, nil
}
