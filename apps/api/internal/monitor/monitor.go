// Package monitor reports what Wise is doing right now: queued and running
// agent runs (native and coding CLIs), stuck runs, and workflows waiting for you.
package monitor

import (
	"context"
	"time"

	"github.com/google/uuid"

	"wise/apps/api/internal/db"
)

// StuckAfter is how long a running run may go without an event before it is flagged.
const StuckAfter = 10 * time.Minute

// Run is one active agent run.
type Run struct {
	ID          uuid.UUID  `json:"id"`
	Agent       string     `json:"agent"`
	Status      string     `json:"status"`
	Purpose     string     `json:"purpose"`
	Runtime     string     `json:"runtime"` // native, claude-code, codex
	Account     string     `json:"account,omitempty"`
	Attempt     int32      `json:"attempt"`
	MaxAttempts int32      `json:"maxAttempts"`
	Cancelling  bool       `json:"cancelling"`
	ProjectID   *uuid.UUID `json:"projectId,omitempty"`
	ProjectName string     `json:"projectName,omitempty"`
	TaskID      *uuid.UUID `json:"taskId,omitempty"`
	TaskTitle   string     `json:"taskTitle,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
	StartedAt   *time.Time `json:"startedAt,omitempty"`
	LeaseUntil  *time.Time `json:"leaseUntil,omitempty"`
	LastEvent   string     `json:"lastEvent,omitempty"`
	LastEventAt *time.Time `json:"lastEventAt,omitempty"`
	Tokens      int64      `json:"tokens"`
	CostUSD     float64    `json:"costUsd"`
	Stuck       string     `json:"stuck,omitempty"` // why it looks stuck
}

// Waiting is a workflow waiting for you.
type Waiting struct {
	ID          uuid.UUID `json:"id"`
	TaskID      uuid.UUID `json:"taskId"`
	TaskTitle   string    `json:"taskTitle"`
	ProjectName string    `json:"projectName"`
	Status      string    `json:"status"`
	Stage       string    `json:"stage"`
	Error       *string   `json:"error,omitempty"`
	Since       time.Time `json:"since"`
}

// Snapshot is the monitor's view.
type Snapshot struct {
	At      time.Time `json:"at"`
	Queued  []Run     `json:"queued"`
	Running []Run     `json:"running"`
	Evals   []Run     `json:"evals"` // evaluation replays, shown apart
	Stuck   int       `json:"stuck"`
	Waiting []Waiting `json:"waiting"`
}

// Service builds snapshots.
type Service struct{ db *db.DB }

// New returns a Service.
func New(d *db.DB) *Service { return &Service{db: d} }

// Snapshot reads the current state.
func (s *Service) Snapshot(ctx context.Context) (Snapshot, error) {
	now := time.Now()
	out := Snapshot{At: now, Queued: []Run{}, Running: []Run{}, Evals: []Run{}, Waiting: []Waiting{}}
	rows, err := s.db.MonitorActiveRuns(ctx)
	if err != nil {
		return out, err
	}
	for _, r := range rows {
		run := Run{ID: r.ID, Agent: r.AgentName, Status: r.Status, Purpose: r.Purpose, Runtime: r.Runtime, Account: r.Account,
			Attempt: r.Attempt, MaxAttempts: r.MaxAttempts, Cancelling: r.CancelRequested, ProjectID: r.ProjectID, TaskID: r.TaskID,
			CreatedAt: r.CreatedAt, StartedAt: r.StartedAt, LeaseUntil: r.LeaseExpiresAt, Tokens: r.Tokens, CostUSD: r.TotalCostUsd}
		if run.Runtime == "" {
			run.Runtime = "native"
		}
		if r.ProjectName != nil {
			run.ProjectName = *r.ProjectName
		}
		if r.TaskTitle != nil {
			run.TaskTitle = *r.TaskTitle
		}
		if r.HasEvent {
			at := r.LastEventAt
			run.LastEvent, run.LastEventAt = r.LastEvent, &at
		}
		if run.Status == "running" {
			last := run.StartedAt
			if run.LastEventAt != nil {
				last = run.LastEventAt
			}
			switch {
			case run.LeaseUntil != nil && run.LeaseUntil.Before(now):
				run.Stuck = "its runtime stopped renewing the lease"
			case last != nil && now.Sub(*last) > StuckAfter:
				run.Stuck = "no activity for " + now.Sub(*last).Round(time.Minute).String()
			}
			if run.Stuck != "" {
				out.Stuck++
			}
		}
		switch {
		case run.Purpose == "eval":
			out.Evals = append(out.Evals, run)
		case run.Status == "queued":
			out.Queued = append(out.Queued, run)
		default:
			out.Running = append(out.Running, run)
		}
	}
	ws, err := s.db.MonitorWaitingWorkflows(ctx)
	if err != nil {
		return out, err
	}
	for _, w := range ws {
		out.Waiting = append(out.Waiting, Waiting{ID: w.ID, TaskID: w.TaskID, TaskTitle: w.TaskTitle, ProjectName: w.ProjectName,
			Status: w.Status, Stage: w.Stage, Error: w.Error, Since: w.UpdatedAt})
	}
	return out, nil
}
