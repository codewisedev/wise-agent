package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"

	"wise/apps/api/internal/apperr"
	"wise/apps/api/internal/audit"
	"wise/apps/api/internal/db"
	"wise/apps/api/internal/db/dbgen"
)

// issuesError turns plan issues into a 400 with one field error per issue.
func issuesError(issues []Issue) error {
	fields := make([]apperr.FieldError, len(issues))
	for i, is := range issues {
		f := "workflow"
		if is.StepID != "" {
			f = "steps." + is.StepID
		}
		fields[i] = apperr.FieldError{Field: f, Message: is.Message}
	}
	return apperr.Validation(fields...)
}

// SeedTemplates creates the built-in templates that do not exist yet.
func (s *Service) SeedTemplates(ctx context.Context, log *slog.Logger) error {
	for _, t := range SeedTemplates() {
		if _, err := s.db.GetWorkflowTemplateBySlug(ctx, &t.Slug); err == nil {
			continue
		} else if !db.IsNotFound(err) {
			return err
		}
		def, err := json.Marshal(t.Def)
		if err != nil {
			return err
		}
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		isDefault := false
		if t.Default {
			_, err := s.db.GetDefaultWorkflowTemplate(ctx)
			isDefault = db.IsNotFound(err)
		}
		slug := t.Slug
		if _, err := s.db.CreateWorkflowTemplate(ctx, dbgen.CreateWorkflowTemplateParams{
			ID: id, Slug: &slug, Name: t.Name, Description: t.Description, Definition: def, IsDefault: isDefault,
		}); err != nil && !db.IsUniqueViolation(err) {
			return err
		}
		log.Info("workflow template created", "slug", t.Slug)
	}
	return nil
}

func (s *Service) ListTemplates(ctx context.Context) ([]dbgen.WorkflowTemplate, error) {
	return s.db.ListWorkflowTemplates(ctx)
}

func (s *Service) GetTemplate(ctx context.Context, id uuid.UUID) (dbgen.WorkflowTemplate, error) {
	t, err := s.db.GetWorkflowTemplate(ctx, id)
	if db.IsNotFound(err) {
		return t, apperr.NotFound("workflow template not found")
	}
	return t, err
}

// TemplateInput creates or replaces a template.
type TemplateInput struct {
	Name        string
	Description string
	Definition  TemplateDef
	Default     bool
}

func (s *Service) validateTemplate(ctx context.Context, in TemplateInput) ([]byte, error) {
	if n := strings.TrimSpace(in.Name); n == "" || len(n) > 100 {
		return nil, apperr.Validation(apperr.FieldError{Field: "name", Message: "must be 1-100 characters"})
	}
	if _, issues := Resolve(ctx, in.Definition, Overrides{}, false, s.Agents, s.Accounts, s.maxRevisions()); len(issues) > 0 {
		return nil, issuesError(issues)
	}
	return json.Marshal(in.Definition)
}

func (s *Service) CreateTemplate(ctx context.Context, in TemplateInput, userID *uuid.UUID) (dbgen.WorkflowTemplate, error) {
	def, err := s.validateTemplate(ctx, in)
	if err != nil {
		return dbgen.WorkflowTemplate{}, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return dbgen.WorkflowTemplate{}, err
	}
	var t dbgen.WorkflowTemplate
	err = s.db.InTx(ctx, func(q *dbgen.Queries) error {
		if in.Default {
			if err := q.ClearDefaultWorkflowTemplate(ctx, id); err != nil {
				return err
			}
		}
		if t, err = q.CreateWorkflowTemplate(ctx, dbgen.CreateWorkflowTemplateParams{
			ID: id, Name: strings.TrimSpace(in.Name), Description: strings.TrimSpace(in.Description), Definition: def,
			IsDefault: in.Default, CreatedBy: userID,
		}); err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{Action: "workflow_template.create", EntityType: "workflow_template", EntityID: audit.Ptr(id),
			Payload: map[string]any{"name": t.Name}})
	})
	return t, err
}

func (s *Service) UpdateTemplate(ctx context.Context, id uuid.UUID, in TemplateInput) (dbgen.WorkflowTemplate, error) {
	def, err := s.validateTemplate(ctx, in)
	if err != nil {
		return dbgen.WorkflowTemplate{}, err
	}
	var t dbgen.WorkflowTemplate
	err = s.db.InTx(ctx, func(q *dbgen.Queries) error {
		t, err = q.UpdateWorkflowTemplate(ctx, dbgen.UpdateWorkflowTemplateParams{
			ID: id, Name: strings.TrimSpace(in.Name), Description: strings.TrimSpace(in.Description), Definition: def,
		})
		if db.IsNotFound(err) {
			return apperr.NotFound("workflow template not found")
		}
		if err != nil {
			return err
		}
		if in.Default && !t.IsDefault {
			if err := q.ClearDefaultWorkflowTemplate(ctx, id); err != nil {
				return err
			}
			if err := q.SetDefaultWorkflowTemplate(ctx, id); err != nil {
				return err
			}
			t.IsDefault = true
		}
		return audit.Record(ctx, q, audit.Entry{Action: "workflow_template.update", EntityType: "workflow_template", EntityID: audit.Ptr(id),
			Payload: map[string]any{"name": t.Name, "version": t.Version}})
	})
	return t, err
}

func (s *Service) DeleteTemplate(ctx context.Context, id uuid.UUID) error {
	return s.db.InTx(ctx, func(q *dbgen.Queries) error {
		t, err := q.GetWorkflowTemplate(ctx, id)
		if db.IsNotFound(err) {
			return apperr.NotFound("workflow template not found")
		}
		if err != nil {
			return err
		}
		if t.IsDefault {
			return apperr.Conflict("make another template the default before deleting this one")
		}
		if _, err := q.DeleteWorkflowTemplate(ctx, id); err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{Action: "workflow_template.delete", EntityType: "workflow_template", EntityID: audit.Ptr(id),
			Payload: map[string]any{"name": t.Name}})
	})
}

// templateFor returns the task's template, or the default template.
func templateFor(ctx context.Context, q *dbgen.Queries, task dbgen.Task) (dbgen.WorkflowTemplate, error) {
	if task.TemplateID != nil {
		t, err := q.GetWorkflowTemplate(ctx, *task.TemplateID)
		if err == nil || !db.IsNotFound(err) {
			return t, err
		}
	}
	t, err := q.GetDefaultWorkflowTemplate(ctx)
	if db.IsNotFound(err) {
		return t, apperr.Conflict("no workflow template is marked as default")
	}
	return t, err
}

// TaskWorkflow is a task's template choice, its overrides and the resolved plan.
type TaskWorkflow struct {
	Template  dbgen.WorkflowTemplate
	Overrides Overrides
	Plan      Plan
	Issues    []Issue
}

func (s *Service) planFor(ctx context.Context, q *dbgen.Queries, task dbgen.Task, stepByStep bool) (TaskWorkflow, error) {
	tmpl, err := templateFor(ctx, q, task)
	if err != nil {
		return TaskWorkflow{}, err
	}
	def, err := ParseTemplate(tmpl.Definition)
	if err != nil {
		return TaskWorkflow{}, err
	}
	ov := ParseOverrides(task.WorkflowOverrides)
	plan, issues := Resolve(ctx, def, ov, stepByStep, s.Agents, s.Accounts, s.maxRevisions())
	plan.TemplateID, plan.TemplateName, plan.TemplateVersion = tmpl.ID.String(), tmpl.Name, tmpl.Version
	return TaskWorkflow{Template: tmpl, Overrides: ov, Plan: plan, Issues: issues}, nil
}

// TaskWorkflowPreview shows what running the task would do.
func (s *Service) TaskWorkflowPreview(ctx context.Context, taskID uuid.UUID) (TaskWorkflow, error) {
	task, err := s.db.GetTask(ctx, taskID)
	if db.IsNotFound(err) {
		return TaskWorkflow{}, apperr.NotFound("task not found")
	}
	if err != nil {
		return TaskWorkflow{}, err
	}
	return s.planFor(ctx, s.db.Queries, task, false)
}

// SetTaskWorkflow chooses a task's template (nil = default) and step overrides.
func (s *Service) SetTaskWorkflow(ctx context.Context, taskID uuid.UUID, templateID *uuid.UUID, ov Overrides) (TaskWorkflow, error) {
	raw, err := json.Marshal(ov)
	if err != nil {
		return TaskWorkflow{}, err
	}
	var out TaskWorkflow
	err = s.db.InTx(ctx, func(q *dbgen.Queries) error {
		if _, err := q.LockTask(ctx, taskID); db.IsNotFound(err) {
			return apperr.NotFound("task not found")
		} else if err != nil {
			return err
		}
		if templateID != nil {
			if _, err := q.GetWorkflowTemplate(ctx, *templateID); db.IsNotFound(err) {
				return apperr.Validation(apperr.FieldError{Field: "templateId", Message: "no such template"})
			} else if err != nil {
				return err
			}
		}
		task, err := q.SetTaskWorkflow(ctx, dbgen.SetTaskWorkflowParams{ID: taskID, TemplateID: templateID, WorkflowOverrides: raw})
		if err != nil {
			return err
		}
		if out, err = s.planFor(ctx, q, task, false); err != nil {
			return err
		}
		if len(out.Issues) > 0 {
			return issuesError(out.Issues)
		}
		return audit.Record(ctx, q, audit.Entry{Action: "task.workflow", EntityType: "task", EntityID: audit.Ptr(taskID),
			Payload: map[string]any{"templateId": templateID, "overrides": ov}})
	})
	return out, err
}

// StepChange edits a not-yet-finished step of a running workflow.
type StepChange struct {
	Account *string
	Model   *string
}

// ChangeStep switches the account/model of a step that has not run yet or is
// waiting (for a retry or a review re-run). The step reads it when it next runs.
func (s *Service) ChangeStep(ctx context.Context, runID uuid.UUID, stepID string, ch StepChange) (Plan, error) {
	var plan Plan
	err := s.db.InTx(ctx, func(q *dbgen.Queries) error {
		run, err := q.LockWorkflowRun(ctx, runID)
		if db.IsNotFound(err) {
			return apperr.NotFound("workflow not found")
		}
		if err != nil {
			return err
		}
		if !IsActiveRun(run.Status) {
			return apperr.Conflict(fmt.Sprintf("workflow is %s", run.Status))
		}
		p, ok := ParsePlan(run.Config)
		if !ok {
			return apperr.Conflict("this workflow has no configurable steps")
		}
		st, i, ok := p.Step(stepID)
		if !ok {
			return apperr.NotFound("step not found")
		}
		if run.CurrentStage != nil && *run.CurrentStage == stepID && run.Status == RunRunning {
			return apperr.Conflict("the step is running; change it when it waits for a retry or a review")
		}
		if ch.Account != nil {
			st.Account = strings.TrimSpace(*ch.Account)
			if st.Account != "" && s.Accounts != nil {
				if err := s.Accounts(ctx, st.Account); err != nil {
					return err
				}
			}
		}
		if ch.Model != nil {
			st.Model = strings.TrimSpace(*ch.Model)
		}
		if st.Model != "" && st.Account == "" {
			return apperr.Validation(apperr.FieldError{Field: "model", Message: "choose an account too"})
		}
		p.Steps[i] = st
		raw, err := json.Marshal(p)
		if err != nil {
			return err
		}
		if err := q.SetWorkflowRunConfig(ctx, dbgen.SetWorkflowRunConfigParams{ID: runID, Config: raw}); err != nil {
			return err
		}
		if err := recordEvent(ctx, q, runID, "step.changed", map[string]any{"stage": stepID, "account": st.Account, "model": st.Model}); err != nil {
			return err
		}
		plan = p
		return audit.Record(ctx, q, audit.Entry{Action: "workflow.step_change", EntityType: "workflow_run", EntityID: audit.Ptr(runID),
			Payload: map[string]any{"step": stepID, "account": st.Account, "model": st.Model}})
	})
	return plan, err
}

// CurrentStep reads a step's live settings (account/model may have changed).
func (s *Service) CurrentStep(ctx context.Context, runID uuid.UUID, stepID string) (PlanStep, error) {
	run, err := s.db.GetWorkflowRun(ctx, runID)
	if err != nil {
		return PlanStep{}, err
	}
	p, ok := ParsePlan(run.Config)
	if !ok {
		return PlanStep{}, fmt.Errorf("workflow run %s has no plan", runID)
	}
	st, _, ok := p.Step(stepID)
	if !ok {
		return PlanStep{}, fmt.Errorf("step %q not in plan", stepID)
	}
	return st, nil
}

// Preview resolves an unsaved template definition.
func (s *Service) Preview(ctx context.Context, def TemplateDef) (Plan, []Issue) {
	return Resolve(ctx, def, Overrides{}, false, s.Agents, s.Accounts, s.maxRevisions())
}
