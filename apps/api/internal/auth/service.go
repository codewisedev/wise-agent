// Package auth implements local users, cookie sessions and personal API tokens.
package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"wise/apps/api/internal/apperr"
	"wise/apps/api/internal/audit"
	"wise/apps/api/internal/db"
	"wise/apps/api/internal/db/dbgen"
)

type Service struct {
	db         *db.DB
	sessionTTL time.Duration
	limiter    *LoginLimiter
	now        func() time.Time
}

func NewService(d *db.DB, sessionTTL time.Duration) *Service {
	return &Service{
		db:         d,
		sessionTTL: sessionTTL,
		limiter:    NewLoginLimiter(10, 15*time.Minute),
		now:        time.Now,
	}
}

// ClientInfo describes the client initiating a login.
type ClientInfo struct {
	IP        string
	UserAgent string
}

// NewSession is returned on successful login.
type NewSession struct {
	Token     string
	Principal Principal
}

var errInvalidCredentials = apperr.Unauthorized("invalid username or password")

// Login verifies credentials and opens a session.
func (s *Service) Login(ctx context.Context, username, password string, client ClientInfo) (NewSession, error) {
	if !s.limiter.Allowed(client.IP) {
		return NewSession{}, apperr.TooManyRequests("too many failed login attempts; try again later")
	}
	user, err := s.db.GetUserByUsername(ctx, strings.TrimSpace(username))
	hash := dummyHash
	if err == nil {
		hash = user.PasswordHash
	} else if !db.IsNotFound(err) {
		return NewSession{}, fmt.Errorf("load user: %w", err)
	}
	ok, verr := VerifyPassword(password, hash)
	if verr != nil {
		return NewSession{}, fmt.Errorf("verify password: %w", verr)
	}
	if err != nil || !ok || user.DisabledAt != nil {
		s.limiter.Fail(client.IP)
		return NewSession{}, errInvalidCredentials
	}
	s.limiter.Reset(client.IP)

	token, err := NewSessionToken()
	if err != nil {
		return NewSession{}, err
	}
	csrf, err := randomString(24)
	if err != nil {
		return NewSession{}, err
	}
	var sess dbgen.Session
	err = s.db.InTx(ctx, func(q *dbgen.Queries) error {
		sess, err = q.CreateSession(ctx, dbgen.CreateSessionParams{
			ID:        hashToken(token),
			UserID:    user.ID,
			CsrfToken: csrf,
			ExpiresAt: s.now().Add(s.sessionTTL),
			UserAgent: truncate(client.UserAgent, 512),
			Ip:        client.IP,
		})
		if err != nil {
			return err
		}
		actx := audit.WithActor(ctx, audit.Actor{Type: audit.ActorUser, ID: audit.Ptr(user.ID)})
		return audit.Record(actx, q, audit.Entry{
			Action: "auth.login", EntityType: "user", EntityID: audit.Ptr(user.ID),
			Payload: map[string]string{"ip": client.IP},
		})
	})
	if err != nil {
		return NewSession{}, fmt.Errorf("create session: %w", err)
	}
	return NewSession{Token: token, Principal: Principal{
		User: user, Method: MethodSession, SessionID: sess.ID, CSRFToken: csrf, ExpiresAt: sess.ExpiresAt,
	}}, nil
}

// Logout ends the given session.
func (s *Service) Logout(ctx context.Context, p Principal) error {
	if p.Method != MethodSession {
		return apperr.BadRequest("logout applies to cookie sessions only")
	}
	return s.db.InTx(ctx, func(q *dbgen.Queries) error {
		if err := q.DeleteSession(ctx, p.SessionID); err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{Action: "auth.logout", EntityType: "user", EntityID: audit.Ptr(p.User.ID)})
	})
}

// AuthenticateSession resolves a session cookie value. Sessions slide: their
// expiry is extended when more than a tenth of the TTL has elapsed.
func (s *Service) AuthenticateSession(ctx context.Context, token string) (Principal, error) {
	row, err := s.db.GetActiveSession(ctx, hashToken(token))
	if db.IsNotFound(err) {
		return Principal{}, apperr.Unauthorized("session expired or invalid")
	}
	if err != nil {
		return Principal{}, fmt.Errorf("load session: %w", err)
	}
	expires, renewed := row.Session.ExpiresAt, false
	if newExp := s.now().Add(s.sessionTTL); newExp.Sub(expires) > s.sessionTTL/10 {
		if err := s.db.TouchSession(ctx, dbgen.TouchSessionParams{ID: row.Session.ID, ExpiresAt: newExp}); err != nil {
			return Principal{}, fmt.Errorf("touch session: %w", err)
		}
		expires, renewed = newExp, true
	}
	return Principal{
		User: row.User, Method: MethodSession, SessionID: row.Session.ID,
		CSRFToken: row.Session.CsrfToken, ExpiresAt: expires, Renewed: renewed,
	}, nil
}

// AuthenticateAPIToken resolves a bearer token.
func (s *Service) AuthenticateAPIToken(ctx context.Context, token string) (Principal, error) {
	prefix, err := parseAPIToken(token)
	if err != nil {
		return Principal{}, apperr.Unauthorized("invalid api token")
	}
	row, err := s.db.GetActiveAPITokenByPrefix(ctx, prefix)
	if db.IsNotFound(err) {
		return Principal{}, apperr.Unauthorized("invalid api token")
	}
	if err != nil {
		return Principal{}, fmt.Errorf("load api token: %w", err)
	}
	if !tokenHashEqual(token, row.ApiToken.TokenHash) {
		return Principal{}, apperr.Unauthorized("invalid api token")
	}
	if err := s.db.TouchAPIToken(ctx, row.ApiToken.ID); err != nil {
		return Principal{}, fmt.Errorf("touch api token: %w", err)
	}
	id := row.ApiToken.ID
	return Principal{User: row.User, Method: MethodAPIToken, TokenID: &id}, nil
}

// CreatedToken is a newly issued API token with its one-time secret.
type CreatedToken struct {
	Token  dbgen.ApiToken
	Secret string
}

// CreateAPIToken issues a personal API token for the principal's user.
func (s *Service) CreateAPIToken(ctx context.Context, p Principal, name string, expiresInDays *int) (CreatedToken, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return CreatedToken{}, apperr.Validation(apperr.FieldError{Field: "name", Message: "must not be blank"})
	}
	prefix, full, err := newAPIToken()
	if err != nil {
		return CreatedToken{}, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return CreatedToken{}, err
	}
	var expiresAt *time.Time
	if expiresInDays != nil {
		t := s.now().Add(time.Duration(*expiresInDays) * 24 * time.Hour)
		expiresAt = &t
	}
	var tok dbgen.ApiToken
	err = s.db.InTx(ctx, func(q *dbgen.Queries) error {
		tok, err = q.CreateAPIToken(ctx, dbgen.CreateAPITokenParams{
			ID: id, UserID: p.User.ID, Name: name, Prefix: prefix,
			TokenHash: hashToken(full), Scopes: []string{"*"}, ExpiresAt: expiresAt,
		})
		if err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{
			Action: "api_token.create", EntityType: "api_token", EntityID: audit.Ptr(id),
			Payload: map[string]any{"name": name, "prefix": prefix},
		})
	})
	if err != nil {
		return CreatedToken{}, fmt.Errorf("create api token: %w", err)
	}
	return CreatedToken{Token: tok, Secret: full}, nil
}

func (s *Service) ListAPITokens(ctx context.Context, p Principal) ([]dbgen.ApiToken, error) {
	return s.db.ListAPITokensByUser(ctx, p.User.ID)
}

func (s *Service) RevokeAPIToken(ctx context.Context, p Principal, id uuid.UUID) error {
	return s.db.InTx(ctx, func(q *dbgen.Queries) error {
		n, err := q.RevokeAPIToken(ctx, dbgen.RevokeAPITokenParams{ID: id, UserID: p.User.ID})
		if err != nil {
			return err
		}
		if n == 0 {
			return apperr.NotFound("api token not found")
		}
		return audit.Record(ctx, q, audit.Entry{Action: "api_token.revoke", EntityType: "api_token", EntityID: audit.Ptr(id)})
	})
}

// CreateUser creates a local user. Used by the CLI and first-boot bootstrap.
func (s *Service) CreateUser(ctx context.Context, username, password, role string) (dbgen.User, error) {
	username = strings.TrimSpace(username)
	if username == "" || len(username) > 64 {
		return dbgen.User{}, errors.New("username must be 1-64 characters")
	}
	if len(password) < MinPasswordLength {
		return dbgen.User{}, fmt.Errorf("password must be at least %d characters", MinPasswordLength)
	}
	hash, err := HashPassword(password)
	if err != nil {
		return dbgen.User{}, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return dbgen.User{}, err
	}
	var user dbgen.User
	err = s.db.InTx(ctx, func(q *dbgen.Queries) error {
		user, err = q.CreateUser(ctx, dbgen.CreateUserParams{ID: id, Username: username, PasswordHash: hash, Role: role})
		if db.IsUniqueViolation(err) {
			return fmt.Errorf("user %q already exists", username)
		}
		if err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{
			Action: "user.create", EntityType: "user", EntityID: audit.Ptr(id),
			Payload: map[string]string{"username": username, "role": role},
		})
	})
	return user, err
}

// SetPassword replaces a user's password.
func (s *Service) SetPassword(ctx context.Context, username, password string) error {
	if len(password) < MinPasswordLength {
		return fmt.Errorf("password must be at least %d characters", MinPasswordLength)
	}
	user, err := s.db.GetUserByUsername(ctx, username)
	if err != nil {
		return fmt.Errorf("load user %q: %w", username, err)
	}
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	return s.db.InTx(ctx, func(q *dbgen.Queries) error {
		if err := q.UpdateUserPassword(ctx, dbgen.UpdateUserPasswordParams{ID: user.ID, PasswordHash: hash}); err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{Action: "user.set_password", EntityType: "user", EntityID: audit.Ptr(user.ID)})
	})
}

// Bootstrap creates the first admin if no users exist. It reports whether a user was created.
func (s *Service) Bootstrap(ctx context.Context, username, password string) (bool, error) {
	if username == "" {
		return false, nil
	}
	n, err := s.db.CountUsers(ctx)
	if err != nil {
		return false, err
	}
	if n > 0 {
		return false, nil
	}
	if _, err := s.CreateUser(ctx, username, password, "admin"); err != nil {
		return false, err
	}
	return true, nil
}

// PruneSessions deletes expired sessions.
func (s *Service) PruneSessions(ctx context.Context) (int64, error) {
	return s.db.DeleteExpiredSessions(ctx)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
