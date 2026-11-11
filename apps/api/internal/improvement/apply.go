package improvement

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"wise/apps/api/internal/agents"
	"wise/apps/api/internal/apperr"
	"wise/apps/api/internal/audit"
	"wise/apps/api/internal/db"
	"wise/apps/api/internal/db/dbgen"
	"wise/apps/api/internal/insights"
	"wise/apps/api/internal/tasks"
)

// Applied records what applying produced and what it replaced (for rollback).
type Applied struct {
	// prompt
	DefinitionID         *uuid.UUID `json:"definitionId,omitempty"`
	PreviousDefinitionID *uuid.UUID `json:"previousDefinitionId,omitempty"`
	// routing
	Setting         string          `json:"setting,omitempty"`
	Version         int32           `json:"version,omitempty"` // setting or template version written
	PreviousVersion int32           `json:"previousVersion,omitempty"`
	PreviousValue   json.RawMessage `json:"previousValue,omitempty"`
	// template
	TemplateID *uuid.UUID `json:"templateId,omitempty"`
	// memory
	Created  []uuid.UUID `json:"created,omitempty"`
	Archived []uuid.UUID `json:"archived,omitempty"`
}

// recheck validates a stored change against the hard rule again, at apply time.
func recheck(p dbgen.ImprovementProposal) error {
	switch p.Kind {
	case KindPrompt:
		return CheckPromptTarget(p.Target)
	case KindRouting:
		var ch RoutingChange
		if err := json.Unmarshal(p.Change, &ch); err != nil {
			return err
		}
		if ch.Setting != p.Target {
			return refuse("the change targets another setting")
		}
		return CheckRoutingOps(ch.Setting, ch.Ops)
	case KindMemory:
		var ch MemoryChange
		if err := json.Unmarshal(p.Change, &ch); err != nil {
			return err
		}
		return CheckMemoryOps(ch.Ops)
	case KindTemplate:
		return nil // compared with the current definition in applyTemplate
	}
	return refuse("unknown proposal kind %q", p.Kind)
}

func (s *Service) metricsNow(ctx context.Context, p dbgen.ImprovementProposal) json.RawMessage {
	if p.Kind != KindPrompt {
		return nil
	}
	rep, err := s.insights.ReportDays(ctx, s.Settings().WindowDays)
	if err != nil {
		return nil
	}
	var mine []insights.AgentStat
	for _, a := range rep.Agents {
		if a.Agent == p.Target {
			mine = append(mine, a)
		}
	}
	return mustJSON(mine)
}

// Apply writes an approved proposal through the target's own versioned
// mechanism (agent version, setting history, template history, memory versions).
func (s *Service) Apply(ctx context.Context, id uuid.UUID, userID *uuid.UUID) (dbgen.ImprovementProposal, error) {
	p, err := s.Proposal(ctx, id) // refreshes staleness
	if err != nil {
		return p, err
	}
	switch {
	case p.Status == "stale":
		return p, apperr.Conflict("the target changed since this was proposed; reject it (a new analysis will reconsider it)")
	case p.Status == "evaluating":
		return p, apperr.Conflict("the proposal is being evaluated; wait for the result or stop the evaluation")
	case !open(p.Status):
		return p, apperr.Conflict("the proposal is " + p.Status)
	}
	if err := recheck(p); err != nil {
		var re *RuleError
		if errors.As(err, &re) {
			return p, apperr.Validation(apperr.FieldError{Field: "change", Message: re.Reason})
		}
		return p, err
	}
	metrics := s.metricsNow(ctx, p)
	var ap Applied
	switch p.Kind {
	case KindPrompt:
		ap, err = s.applyPrompt(ctx, p, userID)
	case KindRouting:
		ap, err = s.applyRouting(ctx, p, userID)
	case KindTemplate:
		ap, err = s.applyTemplate(ctx, p, userID)
	case KindMemory:
		ap, err = s.applyMemory(ctx, p, userID)
	}
	if err != nil {
		var re *RuleError
		if errors.As(err, &re) {
			return p, apperr.Validation(apperr.FieldError{Field: "change", Message: re.Reason})
		}
		return p, err
	}
	var row dbgen.ImprovementProposal
	err = s.db.InTx(ctx, func(q *dbgen.Queries) error {
		var err error
		if row, err = q.SetImprovementProposalApplied(ctx, dbgen.SetImprovementProposalAppliedParams{ID: id, Applied: mustJSON(ap), Metrics: metrics, DecidedBy: userID}); err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{Action: "improvement.applied", EntityType: "improvement_proposal", EntityID: &id,
			Payload: map[string]any{"kind": p.Kind, "target": p.Target, "applied": ap}})
	})
	return row, err
}

func (s *Service) applyPrompt(ctx context.Context, p dbgen.ImprovementProposal, userID *uuid.UUID) (Applied, error) {
	var ch PromptChange
	if err := json.Unmarshal(p.Change, &ch); err != nil {
		return Applied{}, err
	}
	cur, err := s.db.GetCurrentAgentDefinitionByName(ctx, p.Target)
	if err != nil {
		return Applied{}, err
	}
	// Only the prompt changes: the definition (tools, permissions, limits, schemas) is the current one.
	row, err := s.agents.Save(ctx, p.Target, agents.BumpVersion(cur.Definition), ch.Prompt, userID)
	if err != nil {
		return Applied{}, err
	}
	return Applied{DefinitionID: &row.ID, PreviousDefinitionID: &cur.ID}, nil
}

func (s *Service) applyRouting(ctx context.Context, p dbgen.ImprovementProposal, userID *uuid.UUID) (Applied, error) {
	var ch RoutingChange
	if err := json.Unmarshal(p.Change, &ch); err != nil {
		return Applied{}, err
	}
	var base struct {
		Version int32 `json:"version"`
	}
	_ = json.Unmarshal(p.Base, &base)
	cur, err := s.db.GetSetting(ctx, ch.Setting)
	if err != nil {
		return Applied{}, err
	}
	row, err := s.settings.Update(ctx, ch.Setting, ch.Value, base.Version, userID)
	if err != nil {
		return Applied{}, err
	}
	return Applied{Setting: ch.Setting, Version: row.Version, PreviousVersion: cur.Version, PreviousValue: cur.Value}, nil
}

func (s *Service) applyTemplate(ctx context.Context, p dbgen.ImprovementProposal, userID *uuid.UUID) (Applied, error) {
	var ch TemplateChange
	if err := json.Unmarshal(p.Change, &ch); err != nil {
		return Applied{}, err
	}
	id, err := uuid.Parse(p.Target)
	if err != nil {
		return Applied{}, err
	}
	t, err := s.db.GetWorkflowTemplate(ctx, id)
	if err != nil {
		return Applied{}, err
	}
	var before, after tasks.TemplateDef
	if err := json.Unmarshal(t.Definition, &before); err != nil {
		return Applied{}, err
	}
	if err := json.Unmarshal(ch.Definition, &after); err != nil {
		return Applied{}, err
	}
	if err := CheckTemplate(before, after); err != nil {
		return Applied{}, err
	}
	row, err := s.tasks.UpdateTemplate(ctx, id, tasks.TemplateInput{Name: t.Name, Description: t.Description, Definition: after, Default: t.IsDefault}, userID)
	if err != nil {
		return Applied{}, err
	}
	return Applied{TemplateID: &id, Version: row.Version, PreviousVersion: t.Version}, nil
}

func (s *Service) applyMemory(ctx context.Context, p dbgen.ImprovementProposal, userID *uuid.UUID) (Applied, error) {
	var ch MemoryChange
	if err := json.Unmarshal(p.Change, &ch); err != nil {
		return Applied{}, err
	}
	var ap Applied
	// Approving the proposal is the approval: memories are created active.
	for _, op := range ch.Ops {
		if op.Op != "archive" {
			in, err := s.memoryInput(ctx, op)
			if err != nil {
				return ap, refuse("%v", err)
			}
			m, err := s.memory.Create(ctx, in, userID)
			if err != nil {
				return ap, err
			}
			ap.Created = append(ap.Created, m.ID)
		}
		for _, mid := range parseIDs(op.IDs) {
			if _, err := s.memory.Archive(ctx, mid, userID); err != nil {
				return ap, fmt.Errorf("archive memory %s: %w", mid, err)
			}
			ap.Archived = append(ap.Archived, mid)
		}
	}
	return ap, nil
}

// ErrChangedSince means the target changed after the proposal was applied.
var ErrChangedSince = errors.New("the target changed since this was applied")

// Rollback restores what an applied proposal replaced. If the target changed
// since (another edit), confirm must be true.
func (s *Service) Rollback(ctx context.Context, id uuid.UUID, confirm bool, note string, userID *uuid.UUID) (dbgen.ImprovementProposal, error) {
	p, err := s.db.GetImprovementProposal(ctx, id)
	if db.IsNotFound(err) {
		return p, apperr.NotFound("proposal not found")
	}
	if err != nil {
		return p, err
	}
	if p.Status != "applied" {
		return p, apperr.Conflict("only applied proposals can be rolled back (this one is " + p.Status + ")")
	}
	var ap Applied
	if err := json.Unmarshal(p.Applied, &ap); err != nil {
		return p, err
	}
	changed := func() error {
		return apperr.Conflict("the target changed since this was applied; rolling back would also undo those changes. Confirm to roll back anyway").WithCause(ErrChangedSince)
	}
	switch p.Kind {
	case KindPrompt:
		cur, err := s.db.GetCurrentAgentDefinitionByName(ctx, p.Target)
		if err != nil {
			return p, err
		}
		if ap.DefinitionID != nil && cur.ID != *ap.DefinitionID && !confirm {
			return p, changed()
		}
		if _, err := s.agents.Restore(ctx, p.Target, *ap.PreviousDefinitionID); err != nil {
			return p, err
		}
	case KindRouting:
		cur, err := s.db.GetSetting(ctx, ap.Setting)
		if err != nil {
			return p, err
		}
		if cur.Version != ap.Version && !confirm {
			return p, changed()
		}
		if _, err := s.settings.Update(ctx, ap.Setting, ap.PreviousValue, cur.Version, userID); err != nil {
			return p, err
		}
	case KindTemplate:
		t, err := s.db.GetWorkflowTemplate(ctx, *ap.TemplateID)
		if err != nil {
			return p, err
		}
		if t.Version != ap.Version && !confirm {
			return p, changed()
		}
		if _, err := s.tasks.RestoreTemplate(ctx, *ap.TemplateID, ap.PreviousVersion, userID); err != nil {
			return p, err
		}
	case KindMemory:
		for _, mid := range ap.Created {
			if m, err := s.db.GetMemory(ctx, mid); err == nil && m.Status == "active" {
				if _, err := s.memory.Archive(ctx, mid, userID); err != nil {
					return p, err
				}
			}
		}
		for _, mid := range ap.Archived {
			if m, err := s.db.GetMemory(ctx, mid); err == nil && m.Status == "archived" {
				if _, err := s.memory.Reactivate(ctx, mid, userID); err != nil {
					return p, err
				}
			}
		}
	}
	var row dbgen.ImprovementProposal
	err = s.db.InTx(ctx, func(q *dbgen.Queries) error {
		var n *string
		if note != "" {
			c := clip(note, 2000)
			n = &c
		}
		var err error
		if row, err = q.SetImprovementProposalRolledBack(ctx, dbgen.SetImprovementProposalRolledBackParams{ID: id, Note: n}); err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{Action: "improvement.rolled_back", EntityType: "improvement_proposal", EntityID: &id,
			Payload: map[string]any{"kind": p.Kind, "target": p.Target, "confirmed": confirm}})
	})
	return row, err
}

// Monitor compares an applied proposal's target before and since it was applied.
type Monitor struct {
	AppliedAt *time.Time             `json:"appliedAt,omitempty"`
	Before    json.RawMessage        `json:"before,omitempty"`
	Since     []insights.AgentStat   `json:"since"`
	Template  *insights.TemplateStat `json:"template,omitempty"`
}

// Monitor reports the target's numbers since the proposal was applied.
func (s *Service) Monitor(ctx context.Context, id uuid.UUID) (Monitor, error) {
	p, err := s.Proposal(ctx, id)
	if err != nil {
		return Monitor{}, err
	}
	m := Monitor{AppliedAt: p.AppliedAt, Before: p.MetricsBefore, Since: []insights.AgentStat{}}
	if p.AppliedAt == nil {
		return m, nil
	}
	rep, err := s.insights.ReportSince(ctx, "since", *p.AppliedAt)
	if err != nil {
		return m, err
	}
	switch p.Kind {
	case KindPrompt:
		for _, a := range rep.Agents {
			if a.Agent == p.Target {
				m.Since = append(m.Since, a)
			}
		}
	case KindTemplate:
		var before struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(p.Before, &before)
		for i := range rep.Templates {
			if rep.Templates[i].Template == before.Name {
				m.Template = &rep.Templates[i]
			}
		}
	}
	return m, nil
}

// outcomes summarizes applied proposals' effect for the next analysis.
func (s *Service) outcomes(ctx context.Context) []map[string]any {
	rows, err := s.db.AppliedImprovementProposals(ctx)
	if err != nil {
		return nil
	}
	var out []map[string]any
	for _, p := range rows {
		if p.Kind != KindPrompt || p.AppliedAt == nil {
			continue
		}
		mon, err := s.Monitor(ctx, p.ID)
		if err != nil {
			continue
		}
		e := map[string]any{"kind": p.Kind, "target": p.Target, "title": p.Title, "applied": p.AppliedAt.Format("2006-01-02")}
		var before []insights.AgentStat
		_ = json.Unmarshal(p.MetricsBefore, &before)
		if len(before) > 0 {
			b := before[len(before)-1]
			e["before"] = map[string]any{"runs": b.Runs, "success_rate": round2(b.SuccessRate), "rejection_rate": round2(b.RejectionRate)}
		}
		if len(mon.Since) > 0 {
			a := mon.Since[len(mon.Since)-1]
			e["since"] = map[string]any{"runs": a.Runs, "success_rate": round2(a.SuccessRate), "rejection_rate": round2(a.RejectionRate)}
		}
		out = append(out, e)
	}
	return out
}
