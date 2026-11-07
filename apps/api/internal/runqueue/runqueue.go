// Package runqueue dispatches agent runs to runtimes through a Postgres queue
// (SELECT … FOR UPDATE SKIP LOCKED) with leases, heartbeats and requeueing.
// It also implements the runtime-facing operations, authenticated per run.
package runqueue

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"wise/apps/api/internal/agents"
	"wise/apps/api/internal/apperr"
	"wise/apps/api/internal/db"
	"wise/apps/api/internal/db/dbgen"
	"wise/apps/api/internal/events"
	"wise/apps/api/internal/modelgateway"
	"wise/apps/api/internal/schemas"
)

const runTokenPrefix = "wrt_"

// MaxEventPayloadBytes bounds a single runtime event.
const MaxEventPayloadBytes = 256 << 10

type Service struct {
	db    *db.DB
	hub   *events.Hub
	gw    *modelgateway.Gateway
	tools map[string]agents.Tool
	lease time.Duration
	log   *slog.Logger
}

func NewService(d *db.DB, hub *events.Hub, gw *modelgateway.Gateway, tools map[string]agents.Tool, log *slog.Logger) *Service {
	return &Service{db: d, hub: hub, gw: gw, tools: tools, lease: 60 * time.Second, log: log}
}

func (s *Service) LeaseSeconds() int { return int(s.lease.Seconds()) }

// Claim is everything a runtime needs to execute a run.
type Claim struct {
	Run        dbgen.AgentRun
	Token      string
	Definition json.RawMessage
	Prompt     string
	Tools      []agents.Tool
	Project    *ProjectContext
}

type ProjectContext struct {
	Project      dbgen.Project
	Repositories []dbgen.Repository
}

// Claim takes the oldest queued run, or returns nil when the queue is empty.
func (s *Service) Claim(ctx context.Context, runtimeID string) (*Claim, error) {
	token, hash, err := newRunToken()
	if err != nil {
		return nil, err
	}
	var c *Claim
	var ev events.Event
	err = s.db.InTx(ctx, func(q *dbgen.Queries) error {
		run, err := q.ClaimAgentRun(ctx, dbgen.ClaimAgentRunParams{
			RuntimeID: &runtimeID, RunTokenHash: hash, LeaseExpiresAt: ptr(time.Now().Add(s.lease)),
		})
		if db.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		defRow, err := q.GetAgentDefinition(ctx, run.AgentDefinitionID)
		if err != nil {
			return err
		}
		def, err := agents.ParseDefinition(defRow.Definition)
		if err != nil {
			return err
		}
		c = &Claim{Run: run, Token: token, Definition: defRow.Definition, Prompt: defRow.Prompt}
		for _, name := range def.Tools {
			if t, ok := s.tools[name]; ok {
				c.Tools = append(c.Tools, t)
			}
		}
		if run.ProjectID != nil {
			p, err := q.GetProject(ctx, *run.ProjectID)
			if err != nil {
				return err
			}
			repos, err := q.ListRepositories(ctx, *run.ProjectID)
			if err != nil {
				return err
			}
			c.Project = &ProjectContext{Project: p.Project, Repositories: repos}
		}
		ev, err = events.Record(ctx, q, run.ID, "run.started", map[string]any{
			"runtimeId": runtimeID, "attempt": run.Attempt,
		})
		return err
	})
	if err != nil || c == nil {
		return nil, err
	}
	s.hub.Publish(ev)
	return c, nil
}

// Authenticate resolves a per-run token to its running run.
func (s *Service) Authenticate(ctx context.Context, token string) (dbgen.AgentRun, error) {
	if !strings.HasPrefix(token, runTokenPrefix) {
		return dbgen.AgentRun{}, apperr.Unauthorized("invalid run token")
	}
	sum := sha256.Sum256([]byte(token))
	run, err := s.db.GetRunningRunByTokenHash(ctx, sum[:])
	if db.IsNotFound(err) {
		return dbgen.AgentRun{}, apperr.Unauthorized("run token invalid or run no longer running")
	}
	return run, err
}

// Heartbeat extends the lease and reports whether cancellation was requested.
func (s *Service) Heartbeat(ctx context.Context, run dbgen.AgentRun) (bool, error) {
	return s.db.ExtendRunLease(ctx, dbgen.ExtendRunLeaseParams{ID: run.ID, LeaseExpiresAt: ptr(time.Now().Add(s.lease))})
}

// RuntimeEvent is an event reported by the runtime.
type RuntimeEvent struct {
	Type    string
	Payload map[string]any
}

var runtimeEventTypes = map[string]bool{
	"tool.call": true, "tool.result": true, "agent.message": true, "output.invalid": true, "artifact.created": true,
}

// AppendEvents stores runtime events; tool.result events are also recorded as tool_calls.
func (s *Service) AppendEvents(ctx context.Context, run dbgen.AgentRun, in []RuntimeEvent) error {
	var evs []events.Event
	err := s.db.InTx(ctx, func(q *dbgen.Queries) error {
		if _, err := q.LockAgentRun(ctx, run.ID); err != nil {
			return err
		}
		for i, e := range in {
			if !runtimeEventTypes[e.Type] {
				return apperr.Validation(apperr.FieldError{Field: fmt.Sprintf("events[%d].type", i), Message: "not allowed from the runtime"})
			}
			if b, _ := json.Marshal(e.Payload); len(b) > MaxEventPayloadBytes {
				return apperr.Validation(apperr.FieldError{Field: fmt.Sprintf("events[%d].payload", i), Message: "too large (max 256 KiB)"})
			}
			ev, err := events.Record(ctx, q, run.ID, e.Type, e.Payload)
			if err != nil {
				return err
			}
			evs = append(evs, ev)
			if e.Type == "tool.result" {
				if err := recordToolCall(ctx, q, run.ID, e.Payload); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.hub.Publish(evs...)
	return nil
}

func recordToolCall(ctx context.Context, q *dbgen.Queries, runID uuid.UUID, p map[string]any) error {
	id, err := uuid.NewV7()
	if err != nil {
		return err
	}
	tool, _ := p["tool"].(string)
	status, _ := p["status"].(string)
	if status != "error" {
		status = "ok"
	}
	input, _ := json.Marshal(p["input"])
	var output []byte
	if v, ok := p["output"]; ok {
		output, _ = json.Marshal(v)
	}
	var errMsg *string
	if e, ok := p["error"].(string); ok && e != "" {
		errMsg = &e
	}
	dur, _ := p["durationMs"].(float64)
	if string(input) == "null" {
		input = []byte("{}")
	}
	return q.InsertToolCall(ctx, dbgen.InsertToolCallParams{
		ID: id, RunID: runID, Tool: tool, Input: input, Output: output, Status: status, Error: errMsg, DurationMs: int32(dur),
	})
}

// ModelTurn routes the transcript through the gateway using the run's definition.
func (s *Service) ModelTurn(ctx context.Context, run dbgen.AgentRun, msgs []modelgateway.Message) (modelgateway.TurnResponse, error) {
	defRow, err := s.db.GetAgentDefinition(ctx, run.AgentDefinitionID)
	if err != nil {
		return modelgateway.TurnResponse{}, err
	}
	def, err := agents.ParseDefinition(defRow.Definition)
	if err != nil {
		return modelgateway.TurnResponse{}, err
	}
	tools := make([]modelgateway.ToolSpec, 0, len(def.Tools))
	for _, name := range def.Tools {
		if t, ok := s.tools[name]; ok {
			tools = append(tools, modelgateway.ToolSpec{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema})
		}
	}
	resp, err := s.gw.Complete(ctx, modelgateway.TurnRequest{
		RunID: &run.ID, AgentName: def.Name,
		Alias: def.ModelConfig.Alias, Fallbacks: def.ModelConfig.Fallbacks,
		System: defRow.Prompt, Messages: msgs, Tools: tools, OutputSchema: def.OutputSchema,
		Temperature: def.ModelConfig.Temperature, MaxOutputTokens: def.ModelConfig.MaxOutputTokens,
		MaxRunTokens: def.ExecutionLimits.MaxTokens, MaxRunCostUSD: def.ExecutionLimits.MaxCostUSD,
	})
	switch {
	case errors.Is(err, modelgateway.ErrBudgetExceeded):
		return resp, apperr.TooManyRequests(err.Error())
	case err != nil && ctx.Err() == nil:
		return resp, &apperr.Error{Status: http.StatusBadGateway, Title: http.StatusText(http.StatusBadGateway), Detail: err.Error()}
	}
	return resp, err
}

// Complete validates the final output against the agent's output schema and finishes the run.
func (s *Service) Complete(ctx context.Context, run dbgen.AgentRun, output map[string]any) error {
	defRow, err := s.db.GetAgentDefinition(ctx, run.AgentDefinitionID)
	if err != nil {
		return err
	}
	def, err := agents.ParseDefinition(defRow.Definition)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(output)
	if err != nil {
		return err
	}
	if err := schemas.Validate(def.OutputSchema, raw, "output"); err != nil {
		if ae, ok := apperr.As(err); ok {
			ae.Status, ae.Title, ae.Detail = http.StatusUnprocessableEntity, http.StatusText(http.StatusUnprocessableEntity), "output does not match the agent's output_schema"
		}
		return err
	}
	return s.finish(ctx, run.ID, dbgen.AgentRunStatusSucceeded, raw, nil, "run.completed")
}

// Fail ends the run, requeues it (retryable and attempts left), or marks it cancelled.
func (s *Service) Fail(ctx context.Context, run dbgen.AgentRun, msg string, retryable, cancelled bool) error {
	switch {
	case cancelled || run.CancelRequested:
		return s.finish(ctx, run.ID, dbgen.AgentRunStatusCancelled, nil, &msg, "run.cancelled")
	case retryable && run.Attempt < run.MaxAttempts:
		return s.requeue(ctx, run.ID, msg)
	default:
		return s.finish(ctx, run.ID, dbgen.AgentRunStatusFailed, nil, &msg, "run.failed")
	}
}

func (s *Service) finish(ctx context.Context, id uuid.UUID, status dbgen.AgentRunStatus, output []byte, errMsg *string, evType string) error {
	var ev events.Event
	err := s.db.InTx(ctx, func(q *dbgen.Queries) error {
		cur, err := q.LockAgentRun(ctx, id)
		if err != nil {
			return err
		}
		if cur.Status != dbgen.AgentRunStatusRunning {
			return apperr.Conflict(fmt.Sprintf("run is %s", cur.Status))
		}
		run, err := q.FinishRun(ctx, dbgen.FinishRunParams{ID: id, Status: status, Output: output, Error: errMsg})
		if err != nil {
			return err
		}
		payload := map[string]any{
			"inputTokens": run.TotalInputTokens, "outputTokens": run.TotalOutputTokens, "costUsd": run.TotalCostUsd,
		}
		if errMsg != nil {
			payload["error"] = *errMsg
		}
		ev, err = events.Record(ctx, q, id, evType, payload)
		return err
	})
	if err != nil {
		return err
	}
	s.hub.Publish(ev)
	return nil
}

func (s *Service) requeue(ctx context.Context, id uuid.UUID, reason string) error {
	var ev events.Event
	err := s.db.InTx(ctx, func(q *dbgen.Queries) error {
		if _, err := q.LockAgentRun(ctx, id); err != nil {
			return err
		}
		if _, err := q.RequeueRun(ctx, id); err != nil {
			return err
		}
		var err error
		ev, err = events.Record(ctx, q, id, "run.requeued", map[string]any{"reason": reason})
		return err
	})
	if err != nil {
		return err
	}
	s.hub.Publish(ev)
	return nil
}

// Reap requeues (or fails) runs whose runtime stopped heartbeating.
func (s *Service) Reap(ctx context.Context) (int, error) {
	var evs []events.Event
	err := s.db.InTx(ctx, func(q *dbgen.Queries) error {
		expired, err := q.ListExpiredRunLeases(ctx)
		if err != nil {
			return err
		}
		for _, r := range expired {
			if r.CancelRequested {
				// The user asked to stop it; the runtime died before acknowledging.
				msg := "cancelled (runtime lease expired after cancellation was requested)"
				if _, err := q.FinishRun(ctx, dbgen.FinishRunParams{ID: r.ID, Status: dbgen.AgentRunStatusCancelled, Error: &msg}); err != nil {
					return err
				}
				ev, err := events.Record(ctx, q, r.ID, "run.cancelled", map[string]any{"reason": msg})
				if err != nil {
					return err
				}
				evs = append(evs, ev)
				continue
			}
			if r.Attempt < r.MaxAttempts {
				if _, err := q.RequeueRun(ctx, r.ID); err != nil {
					return err
				}
				ev, err := events.Record(ctx, q, r.ID, "run.requeued", map[string]any{"reason": "runtime lease expired"})
				if err != nil {
					return err
				}
				evs = append(evs, ev)
				continue
			}
			msg := "runtime lease expired and no attempts remain"
			if _, err := q.FinishRun(ctx, dbgen.FinishRunParams{ID: r.ID, Status: dbgen.AgentRunStatusFailed, Error: &msg}); err != nil {
				return err
			}
			ev, err := events.Record(ctx, q, r.ID, "run.failed", map[string]any{"error": msg})
			if err != nil {
				return err
			}
			evs = append(evs, ev)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	s.hub.Publish(evs...)
	return len(evs), nil
}

// RunReaper calls Reap periodically until ctx ends.
func (s *Service) RunReaper(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if n, err := s.Reap(ctx); err != nil {
				s.log.Warn("reap expired runs", "err", err)
			} else if n > 0 {
				s.log.Info("reaped runs with expired leases", "count", n)
			}
		}
	}
}

func newRunToken() (string, []byte, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", nil, err
	}
	tok := runTokenPrefix + base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(tok))
	return tok, sum[:], nil
}

func ptr[T any](v T) *T { return &v }
