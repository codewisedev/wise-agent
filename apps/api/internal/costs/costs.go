// Package costs reports model spend over time: by project, agent, model or
// runtime, against daily budgets, with subscription usage (API-equivalent
// cost reported by CLI subscriptions) kept apart from what you pay per use.
package costs

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	"github.com/google/uuid"

	"wise/apps/api/internal/db"
	"wise/apps/api/internal/db/dbgen"
)

// Windows are the supported windows.
var Windows = map[string]int{"7d": 7, "30d": 30, "90d": 90}

// GroupBys are the supported dimensions.
var GroupBys = []string{"project", "agent", "model", "runtime", "billing"}

// MaxKeys is how many groups are shown before "other".
const MaxKeys = 8

// Day is one day's spend per group.
type Day struct {
	Day   string             `json:"day"`
	Total float64            `json:"total"`
	By    map[string]float64 `json:"by"`
}

// Budget is an agent's spend today against its daily budget.
type Budget struct {
	Agent  string   `json:"agent"`
	Spent  float64  `json:"spent"`
	Budget *float64 `json:"budget,omitempty"` // nil: no budget applies
}

// Task is a task's spend.
type Task struct {
	ID      uuid.UUID `json:"id"`
	Title   string    `json:"title"`
	CostUSD float64   `json:"costUsd"`
	Runs    int64     `json:"runs"`
}

// Report is the cost report.
type Report struct {
	Window   string             `json:"window"`
	GroupBy  string             `json:"groupBy"`
	Keys     []string           `json:"keys"`
	Days     []Day              `json:"days"`
	Total    float64            `json:"total"`
	Billing  map[string]float64 `json:"billing"` // subscription, api, local
	EvalUSD  float64            `json:"evalUsd"`
	Tokens   int64              `json:"tokens"`
	Calls    int64              `json:"calls"`
	Budgets  []Budget           `json:"budgets"`
	TopTasks []Task             `json:"topTasks"`
}

// Service computes reports.
type Service struct{ db *db.DB }

// New returns a Service.
func New(d *db.DB) *Service { return &Service{db: d} }

func key(r dbgen.CostBreakdownRow, by string) string {
	switch by {
	case "project":
		return r.Project
	case "model":
		return r.Model
	case "runtime":
		return r.Runtime
	case "billing":
		return r.Billing
	default:
		return r.Agent
	}
}

// Report computes spend over a window grouped by a dimension.
func (s *Service) Report(ctx context.Context, window, groupBy string) (Report, error) {
	days, ok := Windows[window]
	if !ok {
		window, days = "30d", 30
	}
	valid := false
	for _, g := range GroupBys {
		valid = valid || g == groupBy
	}
	if !valid {
		groupBy = "agent"
	}
	now := time.Now().UTC()
	since := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -(days - 1))
	rep := Report{Window: window, GroupBy: groupBy, Keys: []string{}, Days: []Day{}, Billing: map[string]float64{"subscription": 0, "api": 0, "local": 0},
		Budgets: []Budget{}, TopTasks: []Task{}}
	rows, err := s.db.CostBreakdown(ctx, since)
	if err != nil {
		return rep, err
	}
	totals := map[string]float64{}
	for _, r := range rows {
		rep.Tokens += r.Tokens
		rep.Calls += r.Calls
		if r.Purpose == "eval" {
			rep.EvalUSD += r.CostUsd
			continue // evaluation replays are shown apart, not in the work totals
		}
		rep.Total += r.CostUsd
		rep.Billing[r.Billing] += r.CostUsd
		totals[key(r, groupBy)] += r.CostUsd
	}
	keys := make([]string, 0, len(totals))
	for k := range totals {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		return totals[keys[i]] > totals[keys[j]] || (totals[keys[i]] == totals[keys[j]] && keys[i] < keys[j])
	})
	shown := map[string]bool{}
	for i, k := range keys {
		if i < MaxKeys {
			shown[k] = true
			rep.Keys = append(rep.Keys, k)
		}
	}
	if len(keys) > MaxKeys {
		rep.Keys = append(rep.Keys, "other")
	}
	byDay := map[string]*Day{}
	for d := 0; d < days; d++ {
		k := since.AddDate(0, 0, d).Format("2006-01-02")
		byDay[k] = &Day{Day: k, By: map[string]float64{}}
		rep.Days = append(rep.Days, Day{})
	}
	for _, r := range rows {
		if r.Purpose == "eval" {
			continue
		}
		d := byDay[r.Day.Time.Format("2006-01-02")]
		if d == nil {
			continue
		}
		k := key(r, groupBy)
		if !shown[k] {
			k = "other"
		}
		d.By[k] += r.CostUsd
		d.Total += r.CostUsd
	}
	for i := 0; i < days; i++ {
		rep.Days[i] = *byDay[since.AddDate(0, 0, i).Format("2006-01-02")]
	}

	// Today's spend against daily budgets (the model_policy setting).
	var policy struct {
		Budgets map[string]float64 `json:"daily_budget_usd"`
	}
	if row, err := s.db.GetSetting(ctx, "model_policy"); err == nil {
		_ = json.Unmarshal(row.Value, &policy)
	}
	spent, err := s.db.AgentSpendToday(ctx)
	if err != nil {
		return rep, err
	}
	for _, sp := range spent {
		b := Budget{Agent: sp.AgentName, Spent: sp.CostUsd}
		if v, ok := policy.Budgets[sp.AgentName]; ok {
			b.Budget = &v
		} else if v, ok := policy.Budgets["default"]; ok {
			b.Budget = &v
		}
		rep.Budgets = append(rep.Budgets, b)
	}
	tasks, err := s.db.CostTopTasks(ctx, since)
	if err != nil {
		return rep, err
	}
	for _, t := range tasks {
		rep.TopTasks = append(rep.TopTasks, Task{ID: t.ID, Title: t.Title, CostUSD: t.CostUsd, Runs: t.Runs})
	}
	return rep, nil
}
