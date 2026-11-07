package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"wise/apps/api/internal/apperr"
	"wise/apps/api/internal/audit"
	"wise/apps/api/internal/db"
	"wise/apps/api/internal/db/dbgen"
	"wise/apps/api/internal/events"
	"wise/apps/api/internal/projects"
	"wise/apps/api/internal/schemas"
)

type Service struct {
	db  *db.DB
	hub *events.Hub
}

func NewService(d *db.DB, hub *events.Hub) *Service { return &Service{db: d, hub: hub} }

// Summary is a current definition with run statistics.
type Summary struct {
	Row       dbgen.AgentDefinition
	Def       Definition
	RunCount  int64
	LastRunAt *time.Time
}

func (s *Service) List(ctx context.Context) ([]Summary, error) {
	rows, err := s.db.ListCurrentAgentDefinitions(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Summary, 0, len(rows))
	for _, r := range rows {
		d, err := ParseDefinition(r.AgentDefinition.Definition)
		if err != nil {
			return nil, err
		}
		sm := Summary{Row: r.AgentDefinition, Def: d, RunCount: r.RunCount}
		if t, ok := r.LastRunAt.(time.Time); ok {
			sm.LastRunAt = &t
		}
		out = append(out, sm)
	}
	return out, nil
}

func (s *Service) Get(ctx context.Context, name string) (Summary, error) {
	all, err := s.List(ctx)
	if err != nil {
		return Summary{}, err
	}
	for _, a := range all {
		if a.Def.Name == name {
			return a, nil
		}
	}
	return Summary{}, apperr.NotFound(fmt.Sprintf("agent %q not found", name))
}

// CreateRun validates input against the agent's input schema and queues a run.
func (s *Service) CreateRun(ctx context.Context, name string, input map[string]any, projectID *uuid.UUID, userID *uuid.UUID) (dbgen.AgentRun, error) {
	row, err := s.db.GetCurrentAgentDefinitionByName(ctx, name)
	if db.IsNotFound(err) {
		return dbgen.AgentRun{}, apperr.NotFound(fmt.Sprintf("agent %q not found", name))
	}
	if err != nil {
		return dbgen.AgentRun{}, err
	}
	def, err := ParseDefinition(row.Definition)
	if err != nil {
		return dbgen.AgentRun{}, err
	}
	if !row.Enabled {
		phase := "a later phase"
		if def.Phase != nil {
			phase = fmt.Sprintf("phase %d", *def.Phase)
		}
		return dbgen.AgentRun{}, apperr.Conflict(fmt.Sprintf("%s is declared but not executable until %s", name, phase))
	}
	if input == nil {
		input = map[string]any{}
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return dbgen.AgentRun{}, apperr.BadRequest("input must be a JSON object")
	}
	if err := schemas.Validate(def.InputSchema, raw, "input"); err != nil {
		return dbgen.AgentRun{}, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return dbgen.AgentRun{}, err
	}
	var run dbgen.AgentRun
	var ev events.Event
	err = s.db.InTx(ctx, func(q *dbgen.Queries) error {
		if projectID != nil {
			if err := projects.LockActive(ctx, q, *projectID); err != nil {
				return err
			}
		}
		run, err = q.CreateAgentRun(ctx, dbgen.CreateAgentRunParams{
			ID: id, AgentDefinitionID: row.ID, AgentName: name, ProjectID: projectID,
			Input: raw, MaxAttempts: int32(max(def.ErrorHandling.MaxAttempts, 1)), CreatedBy: userID,
		})
		if err != nil {
			return err
		}
		if ev, err = events.Record(ctx, q, id, "run.queued", map[string]any{
			"agent": name, "version": def.Version, "definitionId": row.ID,
		}); err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{
			Action: "agent_run.create", EntityType: "agent_run", EntityID: audit.Ptr(id),
			Payload: map[string]any{"agent": name, "projectId": projectID},
		})
	})
	if err != nil {
		return dbgen.AgentRun{}, err
	}
	s.hub.Publish(ev)
	return run, nil
}

func (s *Service) GetRun(ctx context.Context, id uuid.UUID) (dbgen.AgentRun, error) {
	run, err := s.db.GetAgentRun(ctx, id)
	if db.IsNotFound(err) {
		return dbgen.AgentRun{}, apperr.NotFound("run not found")
	}
	return run, err
}

func (s *Service) ListRuns(ctx context.Context, agentName *string, projectID *uuid.UUID) ([]dbgen.AgentRun, error) {
	if agentName != nil {
		if _, err := s.db.GetCurrentAgentDefinitionByName(ctx, *agentName); db.IsNotFound(err) {
			return nil, apperr.NotFound(fmt.Sprintf("agent %q not found", *agentName))
		}
	}
	return s.db.ListAgentRuns(ctx, dbgen.ListAgentRunsParams{AgentName: agentName, ProjectID: projectID, RowLimit: 100})
}

// CancelRun cancels a queued run immediately, or asks the runtime to stop a running one.
func (s *Service) CancelRun(ctx context.Context, id uuid.UUID) (dbgen.AgentRun, error) {
	var run dbgen.AgentRun
	var evs []events.Event
	err := s.db.InTx(ctx, func(q *dbgen.Queries) error {
		cur, err := q.LockAgentRun(ctx, id)
		if db.IsNotFound(err) {
			return apperr.NotFound("run not found")
		}
		if err != nil {
			return err
		}
		switch cur.Status {
		case dbgen.AgentRunStatusQueued:
			msg := "cancelled before start"
			run, err = q.FinishRun(ctx, dbgen.FinishRunParams{ID: id, Status: dbgen.AgentRunStatusCancelled, Error: &msg})
			if err != nil {
				return err
			}
			ev, err := events.Record(ctx, q, id, "run.cancelled", map[string]any{"reason": msg})
			if err != nil {
				return err
			}
			evs = append(evs, ev)
		case dbgen.AgentRunStatusRunning:
			if run, err = q.RequestRunCancel(ctx, id); err != nil {
				return err
			}
		default:
			return apperr.Conflict(fmt.Sprintf("run already %s", cur.Status))
		}
		return audit.Record(ctx, q, audit.Entry{Action: "agent_run.cancel", EntityType: "agent_run", EntityID: audit.Ptr(id)})
	})
	if err != nil {
		return dbgen.AgentRun{}, err
	}
	s.hub.Publish(evs...)
	return run, nil
}

func (s *Service) ListEvents(ctx context.Context, id uuid.UUID, afterSeq int32) ([]dbgen.AgentEvent, error) {
	if _, err := s.GetRun(ctx, id); err != nil {
		return nil, err
	}
	return s.db.ListAgentEvents(ctx, dbgen.ListAgentEventsParams{RunID: id, Seq: afterSeq})
}

func (s *Service) ListModelCalls(ctx context.Context, id uuid.UUID) ([]dbgen.ModelCall, error) {
	if _, err := s.GetRun(ctx, id); err != nil {
		return nil, err
	}
	return s.db.ListModelCallsForRun(ctx, &id)
}
