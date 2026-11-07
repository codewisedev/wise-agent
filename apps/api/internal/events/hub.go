// Package events persists agent events and fans them out to live subscribers.
package events

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	"wise/apps/api/internal/db"
	"wise/apps/api/internal/db/dbgen"
)

// Event is the wire form of an agent event (matches the AgentEvent contract).
type Event struct {
	RunID     uuid.UUID       `json:"runId"`
	Seq       int32           `json:"seq"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"createdAt"`
}

func FromRow(e dbgen.AgentEvent) Event {
	return Event{RunID: e.RunID, Seq: e.Seq, Type: e.Type, Payload: e.Payload, CreatedAt: e.CreatedAt}
}

type subscriber struct {
	runID *uuid.UUID // nil = all runs
	ch    chan Event
}

// Hub stores events and broadcasts them after commit.
type Hub struct {
	db   *db.DB
	mu   sync.RWMutex
	subs map[*subscriber]struct{}
	wf   workflowSubs
}

func NewHub(d *db.DB) *Hub {
	return &Hub{db: d, subs: map[*subscriber]struct{}{}, wf: workflowSubs{subs: map[*wfSubscriber]struct{}{}}}
}

// Record inserts an event inside an existing transaction. The run row must be
// locked by the caller (LockAgentRun) so sequence numbers stay gap-free.
// Call Publish with the result after the transaction commits.
func Record(ctx context.Context, q *dbgen.Queries, runID uuid.UUID, typ string, payload any) (Event, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return Event{}, fmt.Errorf("marshal event payload: %w", err)
	}
	if payload == nil || string(b) == "null" {
		b = []byte("{}")
	}
	row, err := q.InsertAgentEvent(ctx, dbgen.InsertAgentEventParams{RunID: runID, Type: typ, Payload: b})
	if err != nil {
		return Event{}, fmt.Errorf("insert event: %w", err)
	}
	return FromRow(row), nil
}

// Emit records one event in its own transaction and publishes it.
func (h *Hub) Emit(ctx context.Context, runID uuid.UUID, typ string, payload any) error {
	var ev Event
	err := h.db.InTx(ctx, func(q *dbgen.Queries) error {
		if _, err := q.LockAgentRun(ctx, runID); err != nil {
			return err
		}
		var err error
		ev, err = Record(ctx, q, runID, typ, payload)
		return err
	})
	if err != nil {
		return err
	}
	h.Publish(ev)
	return nil
}

// Publish delivers events to subscribers. Slow subscribers drop events rather
// than blocking writers; clients re-sync from the REST endpoint by seq.
func (h *Hub) Publish(evs ...Event) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, ev := range evs {
		for s := range h.subs {
			if s.runID != nil && *s.runID != ev.RunID {
				continue
			}
			select {
			case s.ch <- ev:
			default:
			}
		}
	}
}

// Subscribe returns a channel of events (optionally for one run) and a cancel func.
func (h *Hub) Subscribe(runID *uuid.UUID) (<-chan Event, func()) {
	s := &subscriber{runID: runID, ch: make(chan Event, 256)}
	h.mu.Lock()
	h.subs[s] = struct{}{}
	h.mu.Unlock()
	return s.ch, func() {
		h.mu.Lock()
		if _, ok := h.subs[s]; ok {
			delete(h.subs, s)
			close(s.ch)
		}
		h.mu.Unlock()
	}
}
