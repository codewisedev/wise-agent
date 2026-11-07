// Package audit records who changed what. Every mutation writes an entry in
// the same transaction as the change itself.
package audit

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"wise/apps/api/internal/db/dbgen"
)

type ActorType string

const (
	ActorUser     ActorType = "user"
	ActorAPIToken ActorType = "api_token"
	ActorSystem   ActorType = "system"
)

// Actor identifies who performed an action.
type Actor struct {
	Type ActorType
	ID   *uuid.UUID
}

var System = Actor{Type: ActorSystem}

type ctxKey struct{}

// WithActor stores the acting principal in ctx.
func WithActor(ctx context.Context, a Actor) context.Context {
	return context.WithValue(ctx, ctxKey{}, a)
}

// ActorFrom returns the actor in ctx, defaulting to System.
func ActorFrom(ctx context.Context) Actor {
	if a, ok := ctx.Value(ctxKey{}).(Actor); ok {
		return a
	}
	return System
}

// Entry describes a single audited action.
type Entry struct {
	Action     string
	EntityType string
	EntityID   *uuid.UUID
	Payload    any
}

// Record writes e attributed to the actor in ctx using q (typically a transaction).
func Record(ctx context.Context, q *dbgen.Queries, e Entry) error {
	payload := []byte("{}")
	if e.Payload != nil {
		b, err := json.Marshal(e.Payload)
		if err != nil {
			return fmt.Errorf("marshal audit payload: %w", err)
		}
		payload = b
	}
	actor := ActorFrom(ctx)
	id, err := uuid.NewV7()
	if err != nil {
		return err
	}
	return q.InsertAuditLog(ctx, dbgen.InsertAuditLogParams{
		ID:         id,
		ActorType:  string(actor.Type),
		ActorID:    actor.ID,
		Action:     e.Action,
		EntityType: e.EntityType,
		EntityID:   e.EntityID,
		Payload:    payload,
	})
}

// Ptr is a helper for taking the address of an ID.
func Ptr(id uuid.UUID) *uuid.UUID { return &id }
