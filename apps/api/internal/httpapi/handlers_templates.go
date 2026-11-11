package httpapi

import (
	"time"

	"context"
	"encoding/json"
	"github.com/google/uuid"

	"wise/apps/api/internal/db/dbgen"
	"wise/apps/api/internal/httpapi/apigen"
	"wise/apps/api/internal/tasks"
)

// reshape converts between domain and API types that share a JSON shape.
func reshape[T any](v any) T {
	var out T
	b, err := json.Marshal(v)
	if err == nil {
		_ = json.Unmarshal(b, &out)
	}
	return out
}

func toTemplate(t dbgen.WorkflowTemplate) apigen.WorkflowTemplate {
	var def apigen.TemplateDefinition
	_ = json.Unmarshal(t.Definition, &def)
	if def.Steps == nil {
		def.Steps = []apigen.TemplateStep{}
	}
	return apigen.WorkflowTemplate{
		Id: t.ID, Slug: t.Slug, Name: t.Name, Description: t.Description, Definition: def,
		Version: int(t.Version), IsDefault: t.IsDefault, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
	}
}

func toPlan(p tasks.Plan) apigen.WorkflowPlan {
	out := reshape[apigen.WorkflowPlan](p)
	if out.Steps == nil {
		out.Steps = []apigen.PlanStep{}
	}
	for i := range out.Steps {
		if out.Steps[i].ReviewKinds == nil {
			out.Steps[i].ReviewKinds = []string{}
		}
	}
	return out
}

func toIssues(is []tasks.Issue) []apigen.PlanIssue {
	out := make([]apigen.PlanIssue, len(is))
	for i, x := range is {
		out[i] = apigen.PlanIssue{Message: x.Message}
		if x.StepID != "" {
			id := x.StepID
			out[i].StepId = &id
		}
	}
	return out
}

func toTaskWorkflow(tw tasks.TaskWorkflow) apigen.TaskWorkflow {
	steps := map[string]apigen.StepOverride{}
	for id, o := range tw.Overrides.Steps {
		steps[id] = reshape[apigen.StepOverride](o)
	}
	return apigen.TaskWorkflow{Template: toTemplate(tw.Template), Steps: steps, Plan: toPlan(tw.Plan), Issues: toIssues(tw.Issues)}
}

func templateInput(b *apigen.WorkflowTemplateInput) tasks.TemplateInput {
	return tasks.TemplateInput{
		Name: b.Name, Description: deref(b.Description), Default: deref(b.IsDefault),
		Definition: reshape[tasks.TemplateDef](b.Definition),
	}
}

func (s *Server) ListWorkflowTemplates(ctx context.Context, _ apigen.ListWorkflowTemplatesRequestObject) (apigen.ListWorkflowTemplatesResponseObject, error) {
	rows, err := s.Tasks.ListTemplates(ctx)
	if err != nil {
		return nil, err
	}
	return apigen.ListWorkflowTemplates200JSONResponse{Items: mapSlice(rows, toTemplate)}, nil
}

func (s *Server) CreateWorkflowTemplate(ctx context.Context, req apigen.CreateWorkflowTemplateRequestObject) (apigen.CreateWorkflowTemplateResponseObject, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	userID := p.User.ID
	t, err := s.Tasks.CreateTemplate(ctx, templateInput(req.Body), &userID)
	if err != nil {
		return nil, err
	}
	return apigen.CreateWorkflowTemplate201JSONResponse(toTemplate(t)), nil
}

func (s *Server) PreviewWorkflowTemplate(ctx context.Context, req apigen.PreviewWorkflowTemplateRequestObject) (apigen.PreviewWorkflowTemplateResponseObject, error) {
	plan, issues := s.Tasks.Preview(ctx, reshape[tasks.TemplateDef](req.Body))
	return apigen.PreviewWorkflowTemplate200JSONResponse{Plan: toPlan(plan), Issues: toIssues(issues)}, nil
}

func (s *Server) GetWorkflowTemplate(ctx context.Context, req apigen.GetWorkflowTemplateRequestObject) (apigen.GetWorkflowTemplateResponseObject, error) {
	t, err := s.Tasks.GetTemplate(ctx, req.TemplateId)
	if err != nil {
		return nil, err
	}
	return apigen.GetWorkflowTemplate200JSONResponse(toTemplate(t)), nil
}

func (s *Server) UpdateWorkflowTemplate(ctx context.Context, req apigen.UpdateWorkflowTemplateRequestObject) (apigen.UpdateWorkflowTemplateResponseObject, error) {
	t, err := s.Tasks.UpdateTemplate(ctx, req.TemplateId, templateInput(req.Body), sessionUserID(ctx))
	if err != nil {
		return nil, err
	}
	return apigen.UpdateWorkflowTemplate200JSONResponse(toTemplate(t)), nil
}

func (s *Server) DeleteWorkflowTemplate(ctx context.Context, req apigen.DeleteWorkflowTemplateRequestObject) (apigen.DeleteWorkflowTemplateResponseObject, error) {
	if err := s.Tasks.DeleteTemplate(ctx, req.TemplateId); err != nil {
		return nil, err
	}
	return apigen.DeleteWorkflowTemplate204Response{}, nil
}

func (s *Server) GetTaskWorkflow(ctx context.Context, req apigen.GetTaskWorkflowRequestObject) (apigen.GetTaskWorkflowResponseObject, error) {
	tw, err := s.Tasks.TaskWorkflowPreview(ctx, req.TaskId)
	if err != nil {
		return nil, err
	}
	return apigen.GetTaskWorkflow200JSONResponse(toTaskWorkflow(tw)), nil
}

func (s *Server) SetTaskWorkflow(ctx context.Context, req apigen.SetTaskWorkflowRequestObject) (apigen.SetTaskWorkflowResponseObject, error) {
	ov := tasks.Overrides{}
	if req.Body.Steps != nil {
		ov.Steps = map[string]tasks.StepOverride{}
		for id, o := range *req.Body.Steps {
			ov.Steps[id] = reshape[tasks.StepOverride](o)
		}
	}
	tw, err := s.Tasks.SetTaskWorkflow(ctx, req.TaskId, req.Body.TemplateId, ov)
	if err != nil {
		return nil, err
	}
	return apigen.SetTaskWorkflow200JSONResponse(toTaskWorkflow(tw)), nil
}

func (s *Server) RunTask(ctx context.Context, req apigen.RunTaskRequestObject) (apigen.RunTaskResponseObject, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	stepByStep := req.Body != nil && deref(req.Body.StepByStep)
	userID := p.User.ID
	run, err := s.Tasks.StartWorkflow(ctx, req.TaskId, &userID, stepByStep)
	if err != nil {
		return nil, err
	}
	return apigen.RunTask202JSONResponse(toWorkflowRun(run)), nil
}

func (s *Server) ChangeWorkflowStep(ctx context.Context, req apigen.ChangeWorkflowStepRequestObject) (apigen.ChangeWorkflowStepResponseObject, error) {
	if _, err := requireSession(ctx, "changing a workflow step"); err != nil {
		return nil, err
	}
	plan, err := s.Tasks.ChangeStep(ctx, req.WorkflowId, req.StepId, tasks.StepChange{Account: req.Body.Account, Model: req.Body.Model})
	if err != nil {
		return nil, err
	}
	return apigen.ChangeWorkflowStep200JSONResponse(toPlan(plan)), nil
}

func (s *Server) ListWorkflowTemplateVersions(ctx context.Context, req apigen.ListWorkflowTemplateVersionsRequestObject) (apigen.ListWorkflowTemplateVersionsResponseObject, error) {
	rows, err := s.Tasks.TemplateVersions(ctx, req.TemplateId)
	if err != nil {
		return nil, err
	}
	type item struct {
		Version     int32           `json:"version"`
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Definition  json.RawMessage `json:"definition"`
		ChangedBy   *uuid.UUID      `json:"changedBy"`
		CreatedAt   time.Time       `json:"createdAt"`
	}
	items := make([]item, len(rows))
	for i, r := range rows {
		items[i] = item{Version: r.Version, Name: r.Name, Description: r.Description, Definition: r.Definition, ChangedBy: r.ChangedBy, CreatedAt: r.CreatedAt}
	}
	return reshape[apigen.ListWorkflowTemplateVersions200JSONResponse](map[string]any{"items": items}), nil
}

func (s *Server) RestoreWorkflowTemplateVersion(ctx context.Context, req apigen.RestoreWorkflowTemplateVersionRequestObject) (apigen.RestoreWorkflowTemplateVersionResponseObject, error) {
	t, err := s.Tasks.RestoreTemplate(ctx, req.TemplateId, int32(req.Version), sessionUserID(ctx))
	if err != nil {
		return nil, err
	}
	return apigen.RestoreWorkflowTemplateVersion200JSONResponse(toTemplate(t)), nil
}
