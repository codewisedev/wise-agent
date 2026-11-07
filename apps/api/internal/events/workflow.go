package events

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"

	"wise/apps/api/internal/db"
	"wise/apps/api/internal/db/dbgen"
)

// WorkflowEvent is the wire form of a workflow event (matches the WorkflowEvent contract).
type WorkflowEvent struct {
	WorkflowRunID uuid.UUID       `json:"workflowRunId"`
	Seq           int32           `json:"seq"`
	Type          string          `json:"type"`
	Payload       json.RawMessage `json:"payload"`
	CreatedAt     time.Time       `json:"createdAt"`
}

func FromWorkflowRow(e dbgen.WorkflowEvent) WorkflowEvent {
	return WorkflowEvent{WorkflowRunID: e.WorkflowRunID, Seq: e.Seq, Type: e.Type, Payload: e.Payload, CreatedAt: e.CreatedAt}
}

type wfSubscriber struct {
	runID *uuid.UUID // nil = every workflow
	ch    chan WorkflowEvent
}

type workflowSubs struct {
	mu   sync.RWMutex
	subs map[*wfSubscriber]struct{}
}

// PublishWorkflow delivers workflow events to subscribers; slow subscribers drop
// events and re-sync from the REST endpoint by seq.
func (h *Hub) PublishWorkflow(evs ...WorkflowEvent) {
	h.wf.mu.RLock()
	defer h.wf.mu.RUnlock()
	for _, ev := range evs {
		for s := range h.wf.subs {
			if s.runID != nil && *s.runID != ev.WorkflowRunID {
				continue
			}
			select {
			case s.ch <- ev:
			default:
			}
		}
	}
}

// SubscribeWorkflow returns workflow events (optionally for one workflow run).
func (h *Hub) SubscribeWorkflow(runID *uuid.UUID) (<-chan WorkflowEvent, func()) {
	s := &wfSubscriber{runID: runID, ch: make(chan WorkflowEvent, 256)}
	h.wf.mu.Lock()
	h.wf.subs[s] = struct{}{}
	h.wf.mu.Unlock()
	return s.ch, func() {
		h.wf.mu.Lock()
		if _, ok := h.wf.subs[s]; ok {
			delete(h.wf.subs, s)
			close(s.ch)
		}
		h.wf.mu.Unlock()
	}
}

// ListenWorkflowEvents bridges Postgres NOTIFY (sent in the same transaction as
// each workflow event, by the API or the orchestrator) into the hub.
func (h *Hub) ListenWorkflowEvents(ctx context.Context, d *db.DB, log *slog.Logger) {
	d.Listen(ctx, "wise_workflow_events", log, func(payload string) {
		id, err := strconv.ParseInt(payload, 10, 64)
		if err != nil {
			return // "" on (re)connect: clients catch up over REST
		}
		row, err := d.GetWorkflowEventByID(ctx, id)
		if err != nil {
			log.Warn("load notified workflow event", "id", id, "err", err)
			return
		}
		h.PublishWorkflow(FromWorkflowRow(row))
	})
}
