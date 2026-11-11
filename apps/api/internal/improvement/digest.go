package improvement

import (
	"context"
	"encoding/json"
	"slices"
	"time"
)

// Digest is what an analysis sees. It holds aggregates and text you wrote
// (review comments, rating notes), truncated error messages, current prompts,
// memories and settings — never agent outputs, tool output or imported task
// text, which could carry instructions into a lasting change.
type Digest struct {
	WindowDays     int              `json:"window_days"`
	Agents         []map[string]any `json:"agents"`
	Prompts        []map[string]any `json:"prompts"`
	Failures       []map[string]any `json:"failures"`
	ReviewComments []map[string]any `json:"review_comments"`
	FeedbackNotes  []map[string]any `json:"feedback_notes"`
	Memories       []map[string]any `json:"memories"`
	Routing        map[string]any   `json:"routing"`
	Providers      []map[string]any `json:"providers"`
	Templates      []map[string]any `json:"templates"`
	PastProposals  []map[string]any `json:"past_proposals"`
	Outcomes       []map[string]any `json:"applied_outcomes"`
	Rules          map[string]any   `json:"rules"`
}

const maxPromptAgents = 6

// Digest gathers the analysis input; worth is false when there is too little to analyse.
func (s *Service) Digest(ctx context.Context, st Settings) (Digest, bool, error) {
	since := time.Now().AddDate(0, 0, -st.WindowDays)
	rep, err := s.insights.ReportSince(ctx, "", since)
	if err != nil {
		return Digest{}, false, err
	}
	d := Digest{WindowDays: st.WindowDays, Agents: []map[string]any{}, Prompts: []map[string]any{}, Failures: []map[string]any{},
		ReviewComments: []map[string]any{}, FeedbackNotes: []map[string]any{}, Memories: []map[string]any{}, Providers: []map[string]any{},
		Templates: []map[string]any{}, PastProposals: []map[string]any{}}

	var promptAgents []string
	troubled := map[string]bool{}
	for _, a := range rep.Agents {
		if a.Agent == Agent || a.Agent == Judge {
			continue
		}
		d.Agents = append(d.Agents, map[string]any{"agent": a.Agent, "version": a.Version, "runs": a.Runs,
			"success_rate": round2(a.SuccessRate), "failed": a.Failed, "retries": a.Retries, "invalid_outputs": a.InvalidOutputs,
			"reviewed": a.Reviewed, "rejected": a.Rejected, "gate_fix_rounds": a.GateFixRounds, "median_seconds": round2(a.MedianSeconds),
			"cost_per_run_usd": round2(a.CostPerRunUSD), "thumbs_up": a.ThumbsUp, "thumbs_down": a.ThumbsDown,
			"enough_runs": a.Runs >= int64(st.MinRuns)})
		if a.Runs >= int64(st.MinRuns) && (a.Rejected > 0 || a.Failed > 0 || a.InvalidOutputs > 0 || a.ThumbsDown > 0 || a.GateFixRounds > 0) {
			troubled[a.Agent] = true
		}
	}
	for _, c := range rep.Failures {
		if len(d.Failures) >= 15 {
			break
		}
		d.Failures = append(d.Failures, map[string]any{"agent": c.Agent, "count": c.Count, "message": clip(c.Text, 200), "run_ids": c.RunIDs})
	}
	for _, c := range rep.Comments {
		if len(d.ReviewComments) >= 25 {
			break
		}
		d.ReviewComments = append(d.ReviewComments, map[string]any{"agent": c.Agent, "count": c.Count, "comment": clip(c.Text, 400), "approval_ids": c.Sources})
		troubled[c.Agent] = true
	}
	notes, err := s.db.RecentNegativeFeedback(ctx, since)
	if err != nil {
		return d, false, err
	}
	for _, n := range notes {
		d.FeedbackNotes = append(d.FeedbackNotes, map[string]any{"agent": n.AgentName, "note": clip(n.Note, 400), "feedback_id": n.ID, "run_id": n.RunID})
		if n.AgentName != nil {
			troubled[*n.AgentName] = true
		}
	}

	// Prompts of the agents with problems (the ones a prompt proposal may target).
	all, err := s.db.ListCurrentAgentDefinitions(ctx)
	if err != nil {
		return d, false, err
	}
	for _, row := range all {
		a := row.AgentDefinition
		if a.Archived || CheckPromptTarget(a.Name) != nil {
			continue
		}
		promptAgents = append(promptAgents, a.Name)
		if troubled[a.Name] && len(d.Prompts) < maxPromptAgents {
			d.Prompts = append(d.Prompts, map[string]any{"agent": a.Name, "version": a.Version, "prompt": clip(a.Prompt, 12000)})
		}
	}

	mems, err := s.db.MemoryRetrievalStats(ctx)
	if err != nil {
		return d, false, err
	}
	for _, m := range mems {
		if len(d.Memories) >= 80 {
			break
		}
		e := map[string]any{"id": m.ID, "kind": m.Kind, "scope": m.Scope, "category": m.Category, "title": clip(m.Title, 200),
			"body": clip(m.Body, 300), "retrievals": m.Retrievals, "created": m.CreatedAt.Format("2006-01-02")}
		if m.Retrievals > 0 {
			e["last_retrieved"] = m.LastRetrieved.Format("2006-01-02")
		}
		d.Memories = append(d.Memories, e)
	}

	d.Routing = map[string]any{}
	if row, err := s.db.GetSetting(ctx, "models"); err == nil {
		var models struct {
			Providers map[string]any `json:"providers"`
			Aliases   map[string]any `json:"aliases"`
		}
		_ = json.Unmarshal(row.Value, &models)
		names := make([]string, 0, len(models.Providers))
		for n := range models.Providers {
			names = append(names, n)
		}
		slices.Sort(names)
		d.Routing["providers"] = names // names only: provider configuration is never shown or changed
		d.Routing["aliases"] = models.Aliases
	}
	if row, err := s.db.GetSetting(ctx, "model_policy"); err == nil {
		var policy struct {
			Budgets map[string]any `json:"daily_budget_usd"`
		}
		_ = json.Unmarshal(row.Value, &policy)
		d.Routing["daily_budget_usd"] = policy.Budgets
	}
	for _, p := range rep.Providers {
		d.Providers = append(d.Providers, map[string]any{"provider": p.Provider, "model": p.Model, "calls": p.Calls,
			"error_rate": round2(p.ErrorRate), "median_latency_ms": int(p.MedianLatencyMs), "cost_usd": round2(p.CostUSD)})
	}
	templates, err := s.db.ListWorkflowTemplates(ctx)
	if err != nil {
		return d, false, err
	}
	for _, t := range templates {
		d.Templates = append(d.Templates, map[string]any{"id": t.ID, "name": t.Name, "definition": json.RawMessage(t.Definition)})
	}
	past, err := s.db.PastImprovementProposals(ctx)
	if err != nil {
		return d, false, err
	}
	for _, p := range past {
		e := map[string]any{"kind": p.Kind, "target": p.Target, "title": p.Title, "outcome": p.Status, "when": p.UpdatedAt.Format("2006-01-02")}
		if p.DecisionNote != nil && *p.DecisionNote != "" {
			e["your_note"] = clip(*p.DecisionNote, 300)
		}
		d.PastProposals = append(d.PastProposals, e)
	}
	d.Outcomes = s.outcomes(ctx)
	if d.Outcomes == nil {
		d.Outcomes = []map[string]any{}
	}
	d.Rules = map[string]any{
		"max_proposals": st.MaxProposals,
		"kinds":         []string{KindPrompt, KindMemory, KindRouting, KindTemplate},
		"prompt_agents": promptAgents,
		"never": []string{"agent tools or permissions", "approval or review requirements", "security policies or reviewers",
			"execution boundaries, egress or sandboxes", "secrets, accounts or credentials", "git authorization", "coding runtime policies",
			"which agents may use cloud models"},
	}
	worth := len(troubled) > 0 || len(d.ReviewComments) > 0 || len(d.FeedbackNotes) > 0
	for _, a := range rep.Agents {
		if a.Runs >= int64(st.MinRuns) {
			worth = true
		}
	}
	return d, worth, nil
}

func round2(x float64) float64 { return float64(int64(x*100+0.5)) / 100 }
