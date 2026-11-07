package auth

import (
	"context"
	"time"

	"github.com/google/uuid"

	"wise/apps/api/internal/audit"
	"wise/apps/api/internal/db/dbgen"
)

type Method string

const (
	MethodSession  Method = "session"
	MethodAPIToken Method = "api_token"
)

// Principal is the authenticated caller of a request.
type Principal struct {
	User      dbgen.User
	Method    Method
	SessionID []byte     // set for session auth
	CSRFToken string     // set for session auth
	ExpiresAt time.Time  // session expiry
	Renewed   bool       // expiry was extended; the cookie must be re-issued
	TokenID   *uuid.UUID // set for api token auth
}

// AuditActor returns the audit actor for this principal.
func (p Principal) AuditActor() audit.Actor {
	if p.Method == MethodAPIToken && p.TokenID != nil {
		return audit.Actor{Type: audit.ActorAPIToken, ID: p.TokenID}
	}
	id := p.User.ID
	return audit.Actor{Type: audit.ActorUser, ID: &id}
}

type principalKey struct{}

func WithPrincipal(ctx context.Context, p Principal) context.Context {
	ctx = audit.WithActor(ctx, p.AuditActor())
	return context.WithValue(ctx, principalKey{}, p)
}

func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}
