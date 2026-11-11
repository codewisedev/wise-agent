package improvement

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"wise/apps/api/internal/agents"
	"wise/apps/api/internal/apperr"
	"wise/apps/api/internal/audit"
	"wise/apps/api/internal/db"
	"wise/apps/api/internal/db/dbgen"
	"wise/apps/api/internal/insights"
	"wise/apps/api/internal/memory"
	"wise/apps/api/internal/settings"
	"wise/apps/api/internal/tasks"
)

// Service runs analyses and manages proposals.
type Service struct {
	db       *db.DB
	agents   *agents.Service
	memory   *memory.Service
	settings *settings.Service
	tasks    *tasks.Service
	insights *insights.Service
	log      *slog.Logger

	mu sync.RWMutex
	st Settings
}

// New returns a Service.
func New(d *db.DB, ag *agents.Service, mem *memory.Service, st *settings.Service, ts *tasks.Service, ins *insights.Service, log *slog.Logger) *Service {
	return &Service{db: d, agents: ag, memory: mem, settings: st, tasks: ts, insights: ins, log: log, st: DefaultSettings()}
}

// SetSettings applies the "improvement" setting.
func (s *Service) SetSettings(st Settings) {
	s.mu.Lock()
	s.st = st
	s.mu.Unlock()
}

// Settings returns the current setting.
func (s *Service) Settings() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.st
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// Evidence references what a proposal is based on (all checked against the database).
type Evidence struct {
	RunIDs      []string `json:"run_ids,omitempty"`
	ApprovalIDs []string `json:"approval_ids,omitempty"`
	FeedbackIDs []string `json:"feedback_ids,omitempty"`
	MemoryIDs   []string `json:"memory_ids,omitempty"`
}

func (e Evidence) count() int {
	return len(e.RunIDs) + len(e.ApprovalIDs) + len(e.FeedbackIDs) + len(e.MemoryIDs)
}

// Raw is a proposal as the analysis agent returns it.
type Raw struct {
	Kind               string          `json:"kind"`
	Target             string          `json:"target"`
	Title              string          `json:"title"`
	Rationale          string          `json:"rationale"`
	Evidence           Evidence        `json:"evidence"`
	PromptEdits        []PromptEdit    `json:"prompt_edits,omitempty"`
	MemoryOps          []MemoryOp      `json:"memory_ops,omitempty"`
	RoutingOps         []PatchOp       `json:"routing_ops,omitempty"`
	TemplateDefinition json.RawMessage `json:"template_definition,omitempty"`
}

// Refusal is a proposal the checks refused.
type Refusal struct {
	Kind   string `json:"kind"`
	Target string `json:"target"`
	Title  string `json:"title"`
	Reason string `json:"reason"`
}

// Built is a validated proposal ready to store.
type Built struct {
	Kind, Target, Title, Rationale string
	Evidence                       Evidence
	Change, Before, Base           json.RawMessage
	Metrics                        json.RawMessage
}

// PromptChange is a prompt proposal's change.
type PromptChange struct {
	Edits  []PromptEdit `json:"edits"`
	Prompt string       `json:"prompt"` // the full new prompt
}

// RoutingChange is a routing proposal's change.
type RoutingChange struct {
	Setting string          `json:"setting"`
	Ops     []PatchOp       `json:"ops"`
	Value   json.RawMessage `json:"value"` // the setting after the patch
}

// MemoryChange is a memory proposal's change.
type MemoryChange struct {
	Ops []MemoryOp `json:"ops"`
}

// TemplateChange is a template proposal's change.
type TemplateChange struct {
	Definition json.RawMessage `json:"definition"`
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func parseIDs(ids []string) []uuid.UUID {
	var out []uuid.UUID
	for _, s := range ids {
		if id, err := uuid.Parse(strings.TrimSpace(s)); err == nil && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

func strs(ids []uuid.UUID) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = id.String()
	}
	return out
}

// checkEvidence keeps only references that exist (work runs, approvals, feedback, memories).
func (s *Service) checkEvidence(ctx context.Context, e Evidence) (Evidence, error) {
	var out Evidence
	if ids := parseIDs(e.RunIDs); len(ids) > 0 {
		found, err := s.db.EvidenceRunsExist(ctx, ids)
		if err != nil {
			return out, err
		}
		out.RunIDs = strs(found)
	}
	if ids := parseIDs(e.ApprovalIDs); len(ids) > 0 {
		found, err := s.db.EvidenceApprovalsExist(ctx, ids)
		if err != nil {
			return out, err
		}
		out.ApprovalIDs = strs(found)
	}
	if ids := parseIDs(e.FeedbackIDs); len(ids) > 0 {
		found, err := s.db.EvidenceFeedbackExist(ctx, ids)
		if err != nil {
			return out, err
		}
		out.FeedbackIDs = strs(found)
	}
	if ids := parseIDs(e.MemoryIDs); len(ids) > 0 {
		found, err := s.db.EvidenceMemoriesExist(ctx, ids)
		if err != nil {
			return out, err
		}
		out.MemoryIDs = strs(found)
	}
	return out, nil
}

// Build validates a raw proposal against the hard rule and the current state.
func (s *Service) Build(ctx context.Context, r Raw) (Built, error) {
	b := Built{Kind: r.Kind, Target: strings.TrimSpace(r.Target), Title: clip(r.Title, 160), Rationale: clip(r.Rationale, 2000)}
	if b.Title == "" || b.Rationale == "" {
		return b, refuse("a proposal needs a title and a rationale")
	}
	ev, err := s.checkEvidence(ctx, r.Evidence)
	if err != nil {
		return b, err
	}
	if ev.count() == 0 {
		return b, refuse("no valid evidence: every proposal must cite runs, reviews, ratings or memories that exist")
	}
	b.Evidence = ev
	switch r.Kind {
	case KindPrompt:
		if err := CheckPromptTarget(b.Target); err != nil {
			return b, err
		}
		cur, err := s.db.GetCurrentAgentDefinitionByName(ctx, b.Target)
		if db.IsNotFound(err) {
			return b, refuse("agent %q does not exist", b.Target)
		}
		if err != nil {
			return b, err
		}
		prompt, err := ApplyPromptEdits(cur.Prompt, r.PromptEdits)
		if err != nil {
			return b, err
		}
		b.Change = mustJSON(PromptChange{Edits: r.PromptEdits, Prompt: prompt})
		b.Before = mustJSON(map[string]any{"prompt": cur.Prompt, "version": cur.Version})
		b.Base = mustJSON(map[string]any{"definitionId": cur.ID})
		if rep, err := s.insights.ReportDays(ctx, s.Settings().WindowDays); err == nil {
			var mine []insights.AgentStat
			for _, a := range rep.Agents {
				if a.Agent == b.Target {
					mine = append(mine, a)
				}
			}
			b.Metrics = mustJSON(mine)
		}
	case KindRouting:
		if err := CheckRoutingOps(b.Target, r.RoutingOps); err != nil {
			return b, err
		}
		row, err := s.db.GetSetting(ctx, b.Target)
		if err != nil {
			return b, err
		}
		next, err := ApplyPatch(row.Value, r.RoutingOps)
		if err != nil {
			return b, err
		}
		if next, err = s.settings.Check(ctx, b.Target, next); err != nil {
			return b, refuse("the patched %s setting is invalid: %v", b.Target, err)
		}
		b.Change = mustJSON(RoutingChange{Setting: b.Target, Ops: r.RoutingOps, Value: next})
		b.Before = row.Value
		b.Base = mustJSON(map[string]any{"setting": b.Target, "version": row.Version})
	case KindTemplate:
		id, err := uuid.Parse(b.Target)
		if err != nil {
			return b, refuse("target must be a template id")
		}
		t, err := s.db.GetWorkflowTemplate(ctx, id)
		if db.IsNotFound(err) {
			return b, refuse("template %s does not exist", b.Target)
		}
		if err != nil {
			return b, err
		}
		var before, after tasks.TemplateDef
		if err := json.Unmarshal(t.Definition, &before); err != nil {
			return b, err
		}
		if len(r.TemplateDefinition) == 0 || json.Unmarshal(r.TemplateDefinition, &after) != nil {
			return b, refuse("a template proposal needs the complete new definition")
		}
		if err := CheckTemplate(before, after); err != nil {
			return b, err
		}
		if _, issues := s.tasks.Preview(ctx, after); len(issues) > 0 {
			return b, refuse("the proposed template is invalid: %s", issues[0].Message)
		}
		def := mustJSON(after)
		if string(def) == string(mustJSON(before)) {
			return b, refuse("the proposed template is the same as the current one")
		}
		b.Change = mustJSON(TemplateChange{Definition: def})
		b.Before = mustJSON(map[string]any{"name": t.Name, "definition": json.RawMessage(t.Definition)})
		b.Base = mustJSON(map[string]any{"templateId": t.ID, "version": t.Version})
	case KindMemory:
		b.Target = "memory"
		if err := CheckMemoryOps(r.MemoryOps); err != nil {
			return b, err
		}
		versions := map[string]int32{}
		var before []map[string]any
		ops := make([]MemoryOp, 0, len(r.MemoryOps))
		for i, op := range r.MemoryOps {
			ids := parseIDs(op.IDs)
			if len(ids) != len(op.IDs) {
				return b, refuse("operation %d: invalid memory ids", i+1)
			}
			for _, id := range ids {
				m, err := s.db.GetMemory(ctx, id)
				if db.IsNotFound(err) || (err == nil && m.Status != "active") {
					return b, refuse("operation %d: memory %s is not an active memory", i+1, id)
				}
				if err != nil {
					return b, err
				}
				if _, seen := versions[id.String()]; !seen {
					versions[id.String()] = m.Version
					before = append(before, map[string]any{"id": m.ID, "version": m.Version, "kind": m.Kind, "scope": m.Scope,
						"category": m.Category, "title": m.Title, "body": m.Body, "projectId": m.ProjectID})
				}
			}
			if op.Op != "archive" {
				in, err := s.memoryInput(ctx, op)
				if err != nil {
					return b, refuse("operation %d: %v", i+1, err)
				}
				op.Scope, op.Kind, op.Category, op.Title, op.Body = in.Scope, in.Kind, in.Category, in.Title, in.Body
				op.ProjectID = ""
				if in.ProjectID != nil {
					op.ProjectID = in.ProjectID.String()
				}
			}
			ops = append(ops, op)
		}
		b.Change = mustJSON(MemoryChange{Ops: ops})
		b.Before = mustJSON(map[string]any{"memories": before})
		b.Base = mustJSON(map[string]any{"versions": versions})
	default:
		return b, refuse("%q is not a proposal kind (prompt, memory, routing, template)", r.Kind)
	}
	return b, nil
}

// memoryInput completes and validates a create or merge operation's memory.
func (s *Service) memoryInput(ctx context.Context, op MemoryOp) (memory.Input, error) {
	in := memory.Input{Scope: op.Scope, Kind: op.Kind, Category: op.Category, Title: strings.TrimSpace(op.Title), Body: strings.TrimSpace(op.Body)}
	if op.Op == "merge" && (in.Scope == "" || in.Kind == "") {
		// Default to the first merged memory's scope and kind.
		if m, err := s.db.GetMemory(ctx, parseIDs(op.IDs)[0]); err == nil {
			if in.Scope == "" {
				in.Scope, in.ProjectID = m.Scope, m.ProjectID
			}
			if in.Kind == "" {
				in.Kind = m.Kind
			}
			if in.Category == "" {
				in.Category = m.Category
			}
		}
	}
	if in.Scope == "" {
		in.Scope = "developer"
	}
	if in.Kind == "" {
		in.Kind = "rule"
	}
	if op.ProjectID != "" {
		id, err := uuid.Parse(op.ProjectID)
		if err != nil {
			return in, errors.New("invalid project id")
		}
		in.ProjectID = &id
	}
	return in, memory.ValidateInput(&in)
}

// Fingerprint identifies the same change proposed again.
func Fingerprint(kind, target string, change json.RawMessage) string {
	var v any
	_ = json.Unmarshal(change, &v)
	if m, ok := v.(map[string]any); ok {
		delete(m, "edits") // a prompt change is identified by its result
	}
	h := sha256.Sum256([]byte(kind + "\x00" + target + "\x00" + string(mustJSON(v))))
	return hex.EncodeToString(h[:16])
}

// store saves built proposals (skipping changes already proposed, applied, or rejected recently).
func (s *Service) store(ctx context.Context, q *dbgen.Queries, analysisID *uuid.UUID, b Built) (*dbgen.ImprovementProposal, *Refusal) {
	fp := Fingerprint(b.Kind, b.Target, b.Change)
	if st, err := q.ImprovementFingerprintStatus(ctx, fp); err == nil {
		reason := map[string]string{"applied": "the same change was already applied", "rejected": "you rejected the same change recently",
			"rolled_back": "the same change was rolled back recently", "stale": "the same change was proposed recently"}[st]
		if reason == "" {
			reason = "the same change is already proposed"
		}
		return nil, &Refusal{Kind: b.Kind, Target: b.Target, Title: b.Title, Reason: reason}
	}
	id, _ := uuid.NewV7()
	row, err := q.InsertImprovementProposal(ctx, dbgen.InsertImprovementProposalParams{ID: id, AnalysisID: analysisID, Kind: b.Kind,
		Target: b.Target, Title: b.Title, Rationale: b.Rationale, Evidence: mustJSON(b.Evidence), Change: b.Change, Before: b.Before,
		Base: b.Base, Fingerprint: fp, MetricsBefore: b.Metrics})
	if err != nil {
		return nil, &Refusal{Kind: b.Kind, Target: b.Target, Title: b.Title, Reason: "could not be stored: " + err.Error()}
	}
	return &row, nil
}

// ErrAnalysisRunning means an analysis is already in progress.
var ErrAnalysisRunning = errors.New("an analysis is already running")

// Analyse starts an analysis now (trigger "manual") or for a schedule period.
func (s *Service) Analyse(ctx context.Context, trigger, period string, userID *uuid.UUID) (dbgen.ImprovementAnalysis, error) {
	st := s.Settings()
	digest, worth, err := s.Digest(ctx, st)
	if err != nil {
		return dbgen.ImprovementAnalysis{}, err
	}
	id, _ := uuid.NewV7()
	p := dbgen.InsertImprovementAnalysisParams{ID: id, Trigger: trigger, WindowDays: int32(st.WindowDays), Status: "running",
		Digest: mustJSON(digest), CreatedBy: userID}
	if period != "" {
		p.Period = &period
	}
	if !worth {
		now := time.Now()
		p.Status, p.FinishedAt = "skipped", &now
	}
	var a dbgen.ImprovementAnalysis
	err = s.db.InTx(ctx, func(q *dbgen.Queries) error {
		var err error
		if a, err = q.InsertImprovementAnalysis(ctx, p); err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{Action: "improvement.analysis", EntityType: "improvement_analysis", EntityID: &id,
			Payload: map[string]any{"trigger": trigger, "period": period, "status": p.Status}})
	})
	if db.IsUniqueViolation(err) {
		if period != "" {
			return a, nil // this period was already analysed
		}
		return a, apperr.Conflict(ErrAnalysisRunning.Error()).WithCause(ErrAnalysisRunning)
	}
	if err != nil || !worth {
		return a, err
	}
	opts := agents.RunOptions{UserID: userID, IdempotencyKey: "improvement:" + id.String()}
	if st.Analysis.Account != "" {
		opts.Model = &agents.ModelOverride{Account: st.Analysis.Account, Model: st.Analysis.Model}
	}
	var input map[string]any
	_ = json.Unmarshal(mustJSON(digest), &input)
	run, err := s.agents.Enqueue(ctx, Agent, input, opts)
	if err != nil {
		msg := clip(err.Error(), 500)
		_ = s.db.FinishImprovementAnalysis(ctx, dbgen.FinishImprovementAnalysisParams{ID: id, Status: "failed", Refused: []byte("[]"), Error: &msg})
		return a, err
	}
	if err := s.db.SetImprovementAnalysisRun(ctx, dbgen.SetImprovementAnalysisRunParams{ID: id, AgentRunID: &run.ID}); err != nil {
		return a, err
	}
	a.AgentRunID = &run.ID
	return a, nil
}

// OnRunFinished turns a finished analysis run into proposals, and advances
// the evaluation a finished replay or judge run belongs to.
func (s *Service) OnRunFinished(ctx context.Context, runID uuid.UUID) {
	if err := s.process(ctx, runID); err != nil {
		s.log.Warn("improvement analysis: process run", "run", runID, "err", err)
	}
	if err := s.onEvalRun(ctx, runID); err != nil {
		s.log.Warn("evaluation: advance", "run", runID, "err", err)
	}
}

func (s *Service) process(ctx context.Context, runID uuid.UUID) error {
	a, err := s.db.GetImprovementAnalysisByRun(ctx, &runID)
	if db.IsNotFound(err) || (err == nil && a.Status != "running") {
		return nil
	}
	if err != nil {
		return err
	}
	run, err := s.db.GetAgentRun(ctx, runID)
	if err != nil {
		return err
	}
	status, errMsg := "succeeded", (*string)(nil)
	var raws []Raw
	switch {
	case run.Status != dbgen.AgentRunStatusSucceeded:
		status = "failed"
		msg := "the analysis run " + string(run.Status)
		if run.Error != nil {
			msg += ": " + clip(*run.Error, 400)
		}
		errMsg = &msg
	default:
		var out struct {
			Proposals []Raw `json:"proposals"`
		}
		if err := json.Unmarshal(run.Output, &out); err != nil {
			status = "failed"
			msg := "unreadable output: " + err.Error()
			errMsg = &msg
		}
		raws = out.Proposals
	}
	max := s.Settings().MaxProposals
	// Validation may read settings and templates: do it before the transaction.
	var built []Built
	refused := []Refusal{}
	for _, r := range raws {
		b, err := s.Build(ctx, r)
		var re *RuleError
		switch {
		case errors.As(err, &re):
			refused = append(refused, Refusal{Kind: r.Kind, Target: r.Target, Title: clip(r.Title, 160), Reason: re.Reason})
		case err != nil:
			refused = append(refused, Refusal{Kind: r.Kind, Target: r.Target, Title: clip(r.Title, 160), Reason: "could not be checked: " + clip(err.Error(), 200)})
		case len(built) >= max:
			refused = append(refused, Refusal{Kind: r.Kind, Target: r.Target, Title: b.Title, Reason: fmt.Sprintf("over the limit of %d proposals per analysis", max)})
		default:
			built = append(built, b)
		}
	}
	return s.db.InTx(ctx, func(q *dbgen.Queries) error {
		if _, err := q.LockImprovementAnalysis(ctx, a.ID); db.IsNotFound(err) {
			return nil // processed meanwhile
		} else if err != nil {
			return err
		}
		stored := 0
		for _, b := range built {
			row, ref := s.store(ctx, q, &a.ID, b)
			if ref != nil {
				refused = append(refused, *ref)
				continue
			}
			stored++
			if err := audit.Record(ctx, q, audit.Entry{Action: "improvement.proposed", EntityType: "improvement_proposal", EntityID: &row.ID,
				Payload: map[string]any{"kind": row.Kind, "target": row.Target}}); err != nil {
				return err
			}
		}
		return q.FinishImprovementAnalysis(ctx, dbgen.FinishImprovementAnalysisParams{ID: a.ID, Status: status, Proposals: int32(stored),
			Refused: mustJSON(refused), Error: errMsg})
	})
}

// Sweep processes analyses whose hook was missed and starts the weekly analysis when due.
func (s *Service) Sweep(ctx context.Context, now time.Time) {
	if ids, err := s.db.ListUnprocessedImprovementAnalyses(ctx); err == nil {
		for _, a := range ids {
			if a.AgentRunID != nil {
				s.OnRunFinished(ctx, *a.AgentRunID)
			}
		}
	}
	_ = s.db.FailStaleImprovementAnalyses(ctx)
	s.sweepEvals(ctx)
	if period := s.Settings().Due(now); period != "" {
		if _, err := s.Analyse(ctx, "scheduled", period, nil); err != nil && !errors.Is(err, ErrAnalysisRunning) {
			s.log.Warn("weekly improvement analysis", "period", period, "err", err)
		}
	}
}

// RunScheduler sweeps every few minutes until ctx ends.
func (s *Service) RunScheduler(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			s.Sweep(ctx, now)
		}
	}
}

// Analyses lists recent analyses.
func (s *Service) Analyses(ctx context.Context) ([]dbgen.ImprovementAnalysis, error) {
	return s.db.ListImprovementAnalyses(ctx, 20)
}

// Filter narrows proposal listings.
type Filter struct {
	Status, Kind, Target *string
	Limit                int
}

// Proposals lists proposals, marking open ones whose target changed as stale.
func (s *Service) Proposals(ctx context.Context, f Filter) ([]dbgen.ImprovementProposal, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 100
	}
	rows, err := s.db.ListImprovementProposals(ctx, dbgen.ListImprovementProposalsParams{Status: f.Status, Kind: f.Kind, Target: f.Target, RowLimit: int32(f.Limit)})
	if err != nil {
		return nil, err
	}
	for i := range rows {
		rows[i] = s.refreshStale(ctx, rows[i])
	}
	return rows, nil
}

// Proposal returns one proposal.
func (s *Service) Proposal(ctx context.Context, id uuid.UUID) (dbgen.ImprovementProposal, error) {
	p, err := s.db.GetImprovementProposal(ctx, id)
	if db.IsNotFound(err) {
		return p, apperr.NotFound("proposal not found")
	}
	if err != nil {
		return p, err
	}
	return s.refreshStale(ctx, p), nil
}

func open(status string) bool { return status == "proposed" || status == "evaluated" }

func (s *Service) refreshStale(ctx context.Context, p dbgen.ImprovementProposal) dbgen.ImprovementProposal {
	if !open(p.Status) {
		return p
	}
	if stale, err := s.Stale(ctx, p); err == nil && stale {
		if row, err := s.db.SetImprovementProposalStatus(ctx, dbgen.SetImprovementProposalStatusParams{ID: p.ID, Status: "stale"}); err == nil {
			return row
		}
	}
	return p
}

// Stale reports whether the proposal's target changed since it was written.
func (s *Service) Stale(ctx context.Context, p dbgen.ImprovementProposal) (bool, error) {
	switch p.Kind {
	case KindPrompt:
		var base struct {
			DefinitionID uuid.UUID `json:"definitionId"`
		}
		_ = json.Unmarshal(p.Base, &base)
		cur, err := s.db.GetCurrentAgentDefinitionByName(ctx, p.Target)
		if db.IsNotFound(err) {
			return true, nil
		}
		return err == nil && cur.ID != base.DefinitionID, err
	case KindRouting:
		var base struct {
			Version int32 `json:"version"`
		}
		_ = json.Unmarshal(p.Base, &base)
		row, err := s.db.GetSetting(ctx, p.Target)
		return err == nil && row.Version != base.Version, err
	case KindTemplate:
		var base struct {
			TemplateID uuid.UUID `json:"templateId"`
			Version    int32     `json:"version"`
		}
		_ = json.Unmarshal(p.Base, &base)
		t, err := s.db.GetWorkflowTemplate(ctx, base.TemplateID)
		if db.IsNotFound(err) {
			return true, nil
		}
		return err == nil && t.Version != base.Version, err
	case KindMemory:
		var base struct {
			Versions map[string]int32 `json:"versions"`
		}
		_ = json.Unmarshal(p.Base, &base)
		for sid, v := range base.Versions {
			id, _ := uuid.Parse(sid)
			m, err := s.db.GetMemory(ctx, id)
			if db.IsNotFound(err) {
				return true, nil
			}
			if err != nil {
				return false, err
			}
			if m.Version != v || m.Status != "active" {
				return true, nil
			}
		}
	}
	return false, nil
}

// Reject declines a proposal (kept, with your note, so later analyses do not repeat it).
func (s *Service) Reject(ctx context.Context, id uuid.UUID, note string, userID *uuid.UUID) (dbgen.ImprovementProposal, error) {
	var row dbgen.ImprovementProposal
	err := s.db.InTx(ctx, func(q *dbgen.Queries) error {
		p, err := q.LockImprovementProposal(ctx, id)
		if db.IsNotFound(err) {
			return apperr.NotFound("proposal not found")
		}
		if err != nil {
			return err
		}
		if !open(p.Status) && p.Status != "stale" {
			return apperr.Conflict(fmt.Sprintf("the proposal is %s", p.Status))
		}
		n := clip(note, 2000)
		if row, err = q.SetImprovementProposalStatus(ctx, dbgen.SetImprovementProposalStatusParams{ID: id, Status: "rejected", Note: &n, DecidedBy: userID}); err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{Action: "improvement.rejected", EntityType: "improvement_proposal", EntityID: &id,
			Payload: map[string]any{"kind": p.Kind, "target": p.Target}})
	})
	return row, err
}

// OpenCount is the number of proposals waiting for you.
func (s *Service) OpenCount(ctx context.Context) (int64, error) {
	return s.db.CountOpenImprovementProposals(ctx)
}
