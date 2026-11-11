// Package insights measures how Wise's agents, models and workflows perform,
// from data Wise already keeps (runs, events, approvals, gate rounds, model
// calls, feedback). Evaluation replays are excluded everywhere.
//
// Attribution: a rejected review counts against the agents of the steps that
// re-ran because of it (from the gate's reviseFrom step to the gate); a
// failed quality-gate round counts against the gate's implementing step.
package insights

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"wise/apps/api/internal/db"
	"wise/apps/api/internal/db/dbgen"
	"wise/apps/api/internal/tasks"
)

// Service computes insights.
type Service struct{ db *db.DB }

// New returns a Service.
func New(d *db.DB) *Service { return &Service{db: d} }

// Windows are the supported windows.
var Windows = map[string]time.Duration{"7d": 7 * 24 * time.Hour, "30d": 30 * 24 * time.Hour, "90d": 90 * 24 * time.Hour}

// AgentStat is one agent version's numbers.
type AgentStat struct {
	Agent          string    `json:"agent"`
	Version        string    `json:"version"`
	DefinitionID   uuid.UUID `json:"definitionId"`
	Runs           int64     `json:"runs"`
	Succeeded      int64     `json:"succeeded"`
	Failed         int64     `json:"failed"`
	Cancelled      int64     `json:"cancelled"`
	Retries        int64     `json:"retries"`
	InvalidOutputs int64     `json:"invalidOutputs"`
	MedianSeconds  float64   `json:"medianSeconds"`
	P90Seconds     float64   `json:"p90Seconds"`
	CostUSD        float64   `json:"costUsd"`
	Tokens         int64     `json:"tokens"`
	Reviewed       int64     `json:"reviewed"` // decided reviews this agent's work was part of
	Rejected       int64     `json:"rejected"`
	GateFixRounds  int64     `json:"gateFixRounds"`
	ThumbsUp       int64     `json:"thumbsUp"`
	ThumbsDown     int64     `json:"thumbsDown"`
	SuccessRate    float64   `json:"successRate"`
	RejectionRate  float64   `json:"rejectionRate"`
	CostPerRunUSD  float64   `json:"costPerRunUsd"`
	InvalidRate    float64   `json:"invalidRate"`
}

// ProviderStat is one model's numbers.
type ProviderStat struct {
	Provider        string  `json:"provider"`
	Model           string  `json:"model"`
	Calls           int64   `json:"calls"`
	Errors          int64   `json:"errors"`
	Rejected        int64   `json:"rejected"`
	MedianLatencyMs float64 `json:"medianLatencyMs"`
	CostUSD         float64 `json:"costUsd"`
	Tokens          int64   `json:"tokens"`
	ErrorRate       float64 `json:"errorRate"`
}

// TemplateStat is one workflow template's numbers.
type TemplateStat struct {
	Template      string  `json:"template"`
	Runs          int64   `json:"runs"`
	Completed     int64   `json:"completed"`
	Failed        int64   `json:"failed"`
	Cancelled     int64   `json:"cancelled"`
	Stalled       int64   `json:"stalled"`
	MedianSeconds float64 `json:"medianSeconds"`
	CostUSD       float64 `json:"costUsd"`
}

// Stall is where waiting workflows failed.
type Stall struct {
	Template string `json:"template"`
	Stage    string `json:"stage"`
	Waiting  int64  `json:"waiting"`
}

// Cluster is a repeated failure message or rejection comment.
type Cluster struct {
	Agent   string      `json:"agent"`
	Text    string      `json:"text"` // a representative (truncated) message
	Count   int         `json:"count"`
	RunIDs  []uuid.UUID `json:"runIds,omitempty"`
	Sources []uuid.UUID `json:"sources,omitempty"` // approval ids for comments
}

// Report is the insights over a window.
type Report struct {
	Window    string         `json:"window"`
	Since     time.Time      `json:"since"`
	Agents    []AgentStat    `json:"agents"`
	Providers []ProviderStat `json:"providers"`
	Templates []TemplateStat `json:"templates"`
	Stalls    []Stall        `json:"stalls"`
	Failures  []Cluster      `json:"failures"`
	Comments  []Cluster      `json:"comments"` // review rejection comments, per attributed agent
}

func ratio(a, b int64) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b)
}

// Since parses a window ("30d").
func Since(window string, now time.Time) (string, time.Time) {
	d, ok := Windows[window]
	if !ok {
		window, d = "30d", Windows["30d"]
	}
	return window, now.Add(-d)
}

// Report computes the insights over a window.
func (s *Service) Report(ctx context.Context, window string) (Report, error) {
	window, since := Since(window, time.Now())
	return s.ReportSince(ctx, window, since)
}

// ReportDays computes the insights over the last n days.
func (s *Service) ReportDays(ctx context.Context, days int) (Report, error) {
	return s.ReportSince(ctx, fmt.Sprintf("%dd", days), time.Now().AddDate(0, 0, -days))
}

// ReportSince computes the insights since a time.
func (s *Service) ReportSince(ctx context.Context, window string, since time.Time) (Report, error) {
	rep := Report{Window: window, Since: since, Agents: []AgentStat{}, Providers: []ProviderStat{}, Templates: []TemplateStat{},
		Stalls: []Stall{}, Failures: []Cluster{}, Comments: []Cluster{}}
	rows, err := s.db.InsightAgentStats(ctx, since)
	if err != nil {
		return rep, err
	}
	byDef := map[uuid.UUID]*AgentStat{}
	for _, r := range rows {
		a := AgentStat{Agent: r.AgentName, Version: r.DefinitionVersion, DefinitionID: r.AgentDefinitionID, Runs: r.Runs,
			Succeeded: r.Succeeded, Failed: r.Failed, Cancelled: r.Cancelled, Retries: r.Retries, InvalidOutputs: r.InvalidOutputs,
			MedianSeconds: r.MedianS, P90Seconds: r.P90S, CostUSD: r.CostUsd, Tokens: r.Tokens}
		rep.Agents = append(rep.Agents, a)
	}
	for i := range rep.Agents {
		byDef[rep.Agents[i].DefinitionID] = &rep.Agents[i]
	}
	if fb, err := s.db.InsightFeedbackStats(ctx, since); err == nil {
		for _, f := range fb {
			if f.AgentDefinitionID != nil {
				if a := byDef[*f.AgentDefinitionID]; a != nil {
					a.ThumbsUp, a.ThumbsDown = f.Up, f.Down
				}
			}
		}
	} else {
		return rep, err
	}
	comments, err := s.attributeReviews(ctx, since, byDef)
	if err != nil {
		return rep, err
	}
	rep.Comments = comments
	if err := s.attributeGateRounds(ctx, since, byDef); err != nil {
		return rep, err
	}
	for i := range rep.Agents {
		a := &rep.Agents[i]
		a.SuccessRate, a.RejectionRate, a.InvalidRate = ratio(a.Succeeded, a.Runs), ratio(a.Rejected, a.Reviewed), ratio(a.InvalidOutputs, a.Runs)
		if a.Runs > 0 {
			a.CostPerRunUSD = a.CostUSD / float64(a.Runs)
		}
	}
	ps, err := s.db.InsightProviderStats(ctx, since)
	if err != nil {
		return rep, err
	}
	for _, p := range ps {
		rep.Providers = append(rep.Providers, ProviderStat{Provider: p.Provider, Model: p.Model, Calls: p.Calls, Errors: p.Errors,
			Rejected: p.Rejected, MedianLatencyMs: p.MedianLatencyMs, CostUSD: p.CostUsd, Tokens: p.Tokens, ErrorRate: ratio(p.Errors, p.Calls)})
	}
	ts, err := s.db.InsightTemplateStats(ctx, since)
	if err != nil {
		return rep, err
	}
	for _, t := range ts {
		rep.Templates = append(rep.Templates, TemplateStat{Template: t.Template, Runs: t.Runs, Completed: t.Completed, Failed: t.Failed,
			Cancelled: t.Cancelled, Stalled: t.Stalled, MedianSeconds: t.MedianS, CostUSD: t.CostUsd})
	}
	st, err := s.db.InsightStalls(ctx)
	if err != nil {
		return rep, err
	}
	for _, x := range st {
		rep.Stalls = append(rep.Stalls, Stall{Template: x.Template, Stage: x.Stage, Waiting: x.Waiting})
	}
	fails, err := s.db.InsightFailures(ctx, since)
	if err != nil {
		return rep, err
	}
	rep.Failures = clusterFailures(fails)
	return rep, nil
}

// stepAgents maps workflow → stage → the agent run's definition.
type stepRun struct {
	agent string
	def   uuid.UUID
}

func (s *Service) stepRuns(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]map[string]stepRun, error) {
	out := map[uuid.UUID]map[string]stepRun{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.db.InsightRunsForWorkflows(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if out[r.WorkflowRunID] == nil {
			out[r.WorkflowRunID] = map[string]stepRun{}
		}
		out[r.WorkflowRunID][r.Stage] = stepRun{agent: r.AgentName, def: r.AgentDefinitionID}
	}
	return out, nil
}

// revisedSteps are the steps a rejection at gate re-runs (reviseFrom..gate).
func revisedSteps(plan tasks.Plan, gate string) []tasks.PlanStep {
	g, gi, ok := plan.Step(gate)
	if !ok {
		return nil
	}
	from := gi
	if g.ReviseFrom != "" {
		if _, j, ok := plan.Step(g.ReviseFrom); ok && j <= gi {
			from = j
		}
	}
	var out []tasks.PlanStep
	for _, st := range plan.Steps[from : gi+1] {
		if st.Enabled && st.IsAgent() && st.Agent != "" {
			out = append(out, st)
		}
	}
	return out
}

func (s *Service) attributeReviews(ctx context.Context, since time.Time, byDef map[uuid.UUID]*AgentStat) ([]Cluster, error) {
	aps, err := s.db.InsightDecidedApprovals(ctx, &since)
	if err != nil {
		return nil, err
	}
	ids := make([]uuid.UUID, 0, len(aps))
	for _, a := range aps {
		ids = append(ids, a.WorkflowRunID)
	}
	runs, err := s.stepRuns(ctx, ids)
	if err != nil {
		return nil, err
	}
	comments := map[string]*Cluster{}
	var order []string
	for _, ap := range aps {
		plan, ok := tasks.ParsePlan(ap.Config)
		if !ok {
			continue
		}
		for _, st := range revisedSteps(plan, ap.Gate) {
			sr, ok := runs[ap.WorkflowRunID][st.ID]
			if !ok {
				continue
			}
			a := byDef[sr.def]
			if a == nil {
				continue
			}
			a.Reviewed++
			if ap.Status != "rejected" {
				continue
			}
			a.Rejected++
			if ap.Comment == nil || strings.TrimSpace(*ap.Comment) == "" {
				continue
			}
			key := sr.agent + "\x00" + normalize(*ap.Comment)
			c := comments[key]
			if c == nil {
				c = &Cluster{Agent: sr.agent, Text: clip(strings.TrimSpace(*ap.Comment), 400)}
				comments[key] = c
				order = append(order, key)
			}
			c.Count++
			if len(c.Sources) < 5 {
				c.Sources = append(c.Sources, ap.ID)
			}
		}
	}
	out := make([]Cluster, 0, len(order))
	for _, k := range order {
		out = append(out, *comments[k])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Count > out[j].Count })
	return out, nil
}

func (s *Service) attributeGateRounds(ctx context.Context, since time.Time, byDef map[uuid.UUID]*AgentStat) error {
	rows, err := s.db.InsightGateRounds(ctx, since)
	if err != nil {
		return err
	}
	var ids []uuid.UUID
	for _, r := range rows {
		if r.WorkflowRunID != nil {
			ids = append(ids, *r.WorkflowRunID)
		}
	}
	runs, err := s.stepRuns(ctx, ids)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if r.WorkflowRunID == nil {
			continue
		}
		plan, ok := tasks.ParsePlan(r.Config)
		if !ok {
			continue
		}
		for _, st := range plan.Steps {
			if st.Type != tasks.StepGate || st.FixFrom == "" {
				continue
			}
			if sr, ok := runs[*r.WorkflowRunID][st.FixFrom]; ok {
				if a := byDef[sr.def]; a != nil {
					a.GateFixRounds += r.FailedRounds
				}
			}
		}
	}
	return nil
}

var (
	uuidRe = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	hexRe  = regexp.MustCompile(`\b[0-9a-f]{12,}\b`)
	numRe  = regexp.MustCompile(`\d+`)
	wsRe   = regexp.MustCompile(`\s+`)
)

// normalize makes similar messages compare equal (ids, numbers, case, spaces).
func normalize(s string) string {
	s = strings.ToLower(s)
	s = uuidRe.ReplaceAllString(s, "<id>")
	s = hexRe.ReplaceAllString(s, "<id>")
	s = numRe.ReplaceAllString(s, "#")
	s = wsRe.ReplaceAllString(strings.TrimSpace(s), " ")
	if len(s) > 160 {
		s = s[:160]
	}
	return s
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func clusterFailures(rows []dbgen.InsightFailuresRow) []Cluster {
	by := map[string]*Cluster{}
	var order []string
	for _, r := range rows {
		key := r.AgentName + "\x00" + normalize(r.Error)
		c := by[key]
		if c == nil {
			c = &Cluster{Agent: r.AgentName, Text: clip(r.Error, 400)}
			by[key] = c
			order = append(order, key)
		}
		c.Count++
		if len(c.RunIDs) < 5 {
			c.RunIDs = append(c.RunIDs, r.ID)
		}
	}
	out := make([]Cluster, 0, len(order))
	for _, k := range order {
		out = append(out, *by[k])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Count > out[j].Count })
	if len(out) > 50 {
		out = out[:50]
	}
	return out
}

// Day is one day of an agent's trend.
type Day struct {
	Day       string  `json:"day"`
	Runs      int64   `json:"runs"`
	Succeeded int64   `json:"succeeded"`
	CostUSD   float64 `json:"costUsd"`
}

// AgentDetail is one agent's trend and evidence.
type AgentDetail struct {
	Agent    string        `json:"agent"`
	Window   string        `json:"window"`
	Versions []AgentStat   `json:"versions"`
	Days     []Day         `json:"days"`
	Failures []Cluster     `json:"failures"`
	Comments []Cluster     `json:"comments"`
	Feedback []FeedbackRef `json:"feedback"` // your thumbs-down notes
}

// FeedbackRef is a thumbs-down with its note.
type FeedbackRef struct {
	ID         uuid.UUID  `json:"id"`
	TargetType string     `json:"targetType"`
	TargetID   uuid.UUID  `json:"targetId"`
	RunID      *uuid.UUID `json:"runId,omitempty"`
	Note       string     `json:"note"`
	At         time.Time  `json:"at"`
}

// Agent returns one agent's detail.
func (s *Service) Agent(ctx context.Context, agent, window string) (AgentDetail, error) {
	rep, err := s.Report(ctx, window)
	if err != nil {
		return AgentDetail{}, err
	}
	d := AgentDetail{Agent: agent, Window: rep.Window, Versions: []AgentStat{}, Days: []Day{}, Failures: []Cluster{}, Comments: []Cluster{}, Feedback: []FeedbackRef{}}
	for _, a := range rep.Agents {
		if a.Agent == agent {
			d.Versions = append(d.Versions, a)
		}
	}
	for _, c := range rep.Failures {
		if c.Agent == agent {
			d.Failures = append(d.Failures, c)
		}
	}
	for _, c := range rep.Comments {
		if c.Agent == agent {
			d.Comments = append(d.Comments, c)
		}
	}
	days, err := s.db.InsightDailyRuns(ctx, dbgen.InsightDailyRunsParams{Since: rep.Since, AgentName: agent})
	if err != nil {
		return d, err
	}
	for _, x := range days {
		d.Days = append(d.Days, Day{Day: x.Day.Time.Format("2006-01-02"), Runs: x.Runs, Succeeded: x.Succeeded, CostUSD: x.CostUsd})
	}
	fb, err := s.db.InsightNegativeFeedback(ctx, dbgen.InsightNegativeFeedbackParams{AgentName: &agent, Since: rep.Since})
	if err != nil {
		return d, err
	}
	for _, f := range fb {
		d.Feedback = append(d.Feedback, FeedbackRef{ID: f.ID, TargetType: f.TargetType, TargetID: f.TargetID, RunID: f.RunID, Note: f.Note, At: f.UpdatedAt})
	}
	return d, nil
}

// String renders a stat for logs and digests.
func (a AgentStat) String() string {
	return fmt.Sprintf("%s v%s: %d runs, %.0f%% ok, %.0f%% rejected, %d fix rounds, $%.3f/run", a.Agent, a.Version, a.Runs,
		a.SuccessRate*100, a.RejectionRate*100, a.GateFixRounds, a.CostPerRunUSD)
}
