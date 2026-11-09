package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"wise/apps/api/internal/agents"
	"wise/apps/api/internal/apperr"
	"wise/apps/api/internal/db"
	"wise/apps/api/internal/db/dbgen"
)

// ExtractorAgent proposes memory candidates from feedback and reports.
const ExtractorAgent = "memory-agent"

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// existingFor lists active memories near the material so the agent does not
// propose what is already known.
func (s *Service) existingFor(ctx context.Context, projectID *uuid.UUID, text string) []map[string]string {
	rel, _, _ := s.relevant(ctx, projectID, text, 12)
	rules, _ := s.db.ActiveRules(ctx, projectID)
	out := []map[string]string{}
	for _, m := range append(rules, rel...) {
		if len(out) >= 20 {
			break
		}
		out = append(out, map[string]string{"kind": m.Kind, "scope": m.Scope, "title": m.Title, "body": clip(m.Body, 300)})
	}
	return out
}

// extract queues a memory-agent run and records what it extracts from.
func (s *Service) extract(ctx context.Context, idem, source string, refs map[string]any, projectID *uuid.UUID, input map[string]any, userID *uuid.UUID) (*dbgen.AgentRun, error) {
	input["existing_memories"] = s.existingFor(ctx, projectID, fmt.Sprint(input["feedback"], " ", input["material"]))
	run, err := s.agents.Enqueue(ctx, ExtractorAgent, input, agents.RunOptions{ProjectID: projectID, UserID: userID, IdempotencyKey: idem})
	if err != nil {
		return nil, err
	}
	rawRefs, _ := json.Marshal(refs)
	if err := s.db.InsertMemoryExtraction(ctx, dbgen.InsertMemoryExtractionParams{RunID: run.ID, Source: source, SourceRefs: rawRefs, ProjectID: projectID}); err != nil && !db.IsUniqueViolation(err) {
		return nil, err
	}
	return &run, nil
}

// taskContext loads the task (and its project) an extraction is about.
func (s *Service) taskContext(ctx context.Context, taskID uuid.UUID) (dbgen.Task, *uuid.UUID, error) {
	t, err := s.db.GetTask(ctx, taskID)
	if db.IsNotFound(err) {
		return t, nil, apperr.NotFound("task not found")
	}
	pid := t.ProjectID
	return t, &pid, err
}

func artifactText(content []byte, n int) string {
	var v any
	if json.Unmarshal(content, &v) != nil {
		return clip(string(content), n)
	}
	b, _ := json.MarshalIndent(v, "", " ")
	return clip(string(b), n)
}

// OnApprovalDecided extracts from a review decision that carries a comment:
// the user's corrections are the most valuable source of memories.
func (s *Service) OnApprovalDecided(ctx context.Context, ap dbgen.Approval) {
	if ap.Comment == nil || len(strings.TrimSpace(*ap.Comment)) < 10 || !s.Settings().ExtractOnReviewComments {
		return
	}
	task, pid, err := s.taskContext(ctx, ap.TaskID)
	if err != nil {
		s.log.Warn("memory extraction: load task", "err", err)
		return
	}
	var material strings.Builder
	var refs []struct {
		Kind string `json:"kind"`
	}
	_ = json.Unmarshal(ap.Artifacts, &refs)
	for _, r := range refs {
		if a, err := s.db.GetLatestArtifact(ctx, dbgen.GetLatestArtifactParams{TaskID: ap.TaskID, Kind: r.Kind}); err == nil {
			material.WriteString("### " + r.Kind + "\n" + artifactText(a.Content, 6000) + "\n")
		}
	}
	input := map[string]any{
		"source": "review_feedback", "decision": ap.Status, "feedback": *ap.Comment,
		"task":     map[string]any{"title": task.Title, "description": clip(task.Description, 3000)},
		"material": clip(material.String(), 12000),
	}
	if _, err := s.extract(ctx, "memory:approval:"+ap.ID.String(), "review_feedback",
		map[string]any{"taskId": ap.TaskID, "approvalId": ap.ID}, pid, input, nil); err != nil {
		s.log.Warn("memory extraction from review", "approval", ap.ID, "err", err)
	}
}

// OnWorkflowCompleted extracts decisions from a finished workflow's final report.
func (s *Service) OnWorkflowCompleted(ctx context.Context, taskID, workflowRunID uuid.UUID) {
	if !s.Settings().ExtractOnCompletedWorkflows {
		return
	}
	task, pid, err := s.taskContext(ctx, taskID)
	if err != nil {
		return
	}
	report, err := s.db.GetLatestArtifact(ctx, dbgen.GetLatestArtifactParams{TaskID: taskID, Kind: "final_report"})
	if err != nil {
		return // no report (e.g. a template without one): nothing to learn from
	}
	input := map[string]any{
		"source": "report", "task": map[string]any{"title": task.Title, "description": clip(task.Description, 3000)},
		"material": artifactText(report.Content, 14000),
	}
	if _, err := s.extract(ctx, "memory:workflow:"+workflowRunID.String(), "report",
		map[string]any{"taskId": taskID, "workflowId": workflowRunID, "artifactId": report.ID}, pid, input, nil); err != nil {
		s.log.Warn("memory extraction from report", "workflow", workflowRunID, "err", err)
	}
}

// ExtractFromTask runs an extraction over a task on demand (all review
// comments and the latest artifacts).
func (s *Service) ExtractFromTask(ctx context.Context, taskID uuid.UUID, userID *uuid.UUID) (dbgen.AgentRun, error) {
	task, pid, err := s.taskContext(ctx, taskID)
	if err != nil {
		return dbgen.AgentRun{}, err
	}
	var feedback, material strings.Builder
	if aps, err := s.db.ListApprovals(ctx, dbgen.ListApprovalsParams{TaskID: &taskID, RowLimit: 50}); err == nil {
		for _, r := range aps {
			if ap := r.Approval; ap.Comment != nil && *ap.Comment != "" {
				feedback.WriteString(fmt.Sprintf("- %s (%s): %s\n", ap.Gate, ap.Status, *ap.Comment))
			}
		}
	}
	if arts, err := s.db.ListArtifacts(ctx, dbgen.ListArtifactsParams{TaskID: taskID}); err == nil {
		seen := map[string]bool{}
		for _, a := range arts { // newest version of each kind first
			if seen[a.Kind] {
				continue
			}
			seen[a.Kind] = true
			material.WriteString("### " + a.Kind + "\n" + artifactText(a.Content, 5000) + "\n")
		}
	}
	if feedback.Len() == 0 && material.Len() == 0 {
		return dbgen.AgentRun{}, apperr.Conflict("this task has no review comments or results to learn from yet")
	}
	input := map[string]any{
		"source": "task", "task": map[string]any{"title": task.Title, "description": clip(task.Description, 3000)},
		"feedback": clip(feedback.String(), 6000), "material": clip(material.String(), 14000),
	}
	run, err := s.extract(ctx, "", "task", map[string]any{"taskId": taskID}, pid, input, userID)
	if err != nil {
		return dbgen.AgentRun{}, err
	}
	return *run, nil
}

// OnRunFinished turns a finished memory-agent run's output into candidates.
func (s *Service) OnRunFinished(ctx context.Context, runID uuid.UUID) {
	if err := s.process(ctx, runID); err != nil {
		s.log.Warn("memory extraction: process run", "run", runID, "err", err)
	}
}

func (s *Service) process(ctx context.Context, runID uuid.UUID) error {
	ext, err := s.db.GetMemoryExtraction(ctx, runID)
	if db.IsNotFound(err) || (err == nil && ext.ProcessedAt != nil) {
		return nil // not an extraction run, or already processed
	}
	if err != nil {
		return err
	}
	run, err := s.db.GetAgentRun(ctx, runID)
	if err != nil {
		return err
	}
	var cands []Candidate
	if run.Status == dbgen.AgentRunStatusSucceeded && len(run.Output) > 0 {
		var out struct {
			Memories []Candidate `json:"memories"`
		}
		if err := json.Unmarshal(run.Output, &out); err != nil {
			// A malformed output will not get better: record it as yielding nothing.
			s.log.Warn("memory extraction: unreadable output", "run", runID, "err", err)
		} else {
			cands = out.Memories
		}
	}
	if ext.Source == "profile_inference" {
		cands = downgradeInferred(cands) // inference proposes preferences, never rules
	}
	refs := map[string]any{}
	_ = json.Unmarshal(ext.SourceRefs, &refs)
	refs["runId"] = runID
	// Duplicate checks may call the embedding model: do them before the transaction.
	prep := s.prepareCandidates(ctx, cands, ext.ProjectID)
	// Candidates and "processed" commit together, so nothing is lost or stored twice.
	return s.db.InTx(ctx, func(q *dbgen.Queries) error {
		if _, err := q.LockMemoryExtraction(ctx, runID); db.IsNotFound(err) {
			return nil // processed meanwhile (hook and sweep raced)
		} else if err != nil {
			return err
		}
		if err := insertCandidates(ctx, q, prep, ext.Source, refs); err != nil {
			return err
		}
		return q.FinishMemoryExtraction(ctx, dbgen.FinishMemoryExtractionParams{RunID: runID, Candidates: int32(len(prep))})
	})
}

// Sweep processes extraction runs that finished while the hook was missed, and
// starts extractions for workflows that completed (in the orchestrator).
func (s *Service) Sweep(ctx context.Context) error {
	if s.Settings().ExtractOnCompletedWorkflows {
		done, err := s.db.ListCompletedWorkflowsToLearnFrom(ctx)
		if err != nil {
			return err
		}
		for _, w := range done {
			s.OnWorkflowCompleted(ctx, w.TaskID, w.ID)
		}
	}
	ids, err := s.db.ListFinishedUnprocessedExtractions(ctx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := s.process(ctx, id); err != nil {
			s.log.Warn("memory extraction sweep: process run", "run", id, "err", err) // keep going with the others
		}
	}
	return nil
}
