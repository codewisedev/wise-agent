// Package accounts stores provider credentials (model providers and task
// sources) encrypted at rest and keeps the model gateway's account list current.
package accounts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"wise/apps/api/internal/apperr"
	"wise/apps/api/internal/audit"
	"wise/apps/api/internal/db"
	"wise/apps/api/internal/db/dbgen"
	"wise/apps/api/internal/modelgateway"
	"wise/apps/api/internal/secrets"
)

// Kinds.
const (
	KindClaudeCode = "claude_code"
	KindCodexCLI   = "codex_cli"
	KindOpenAI     = "openai_compatible"
	KindJira       = "jira"
	KindClickUp    = "clickup"
	KindTrello     = "trello"
)

// IsModelKind reports whether accounts of this kind serve model calls.
func IsModelKind(kind string) bool {
	return kind == KindClaudeCode || kind == KindCodexCLI || kind == KindOpenAI
}

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

type Service struct {
	db  *db.DB
	box *secrets.Box
	gw  *modelgateway.Gateway
	log *slog.Logger
	// http is used for connection tests of task-source accounts.
	http *http.Client
}

func NewService(d *db.DB, box *secrets.Box, gw *modelgateway.Gateway, log *slog.Logger) *Service {
	return &Service{db: d, box: box, gw: gw, log: log, http: &http.Client{Timeout: 15 * time.Second}}
}

// SecretsEnabled reports whether credentials can be stored.
func (s *Service) SecretsEnabled() bool { return s.box.Enabled() }

// Input creates or updates an account. Nil pointers keep the current value on update.
type Input struct {
	Name         *string
	Kind         string
	BaseURL      *string
	DefaultModel *string
	Cloud        *bool
	Enabled      *bool
	Metadata     map[string]any
	// Secret replaces the stored secret when non-nil ("" clears it).
	Secret *string
}

func (s *Service) List(ctx context.Context) ([]dbgen.Account, error) { return s.db.ListAccounts(ctx) }

func (s *Service) Get(ctx context.Context, id uuid.UUID) (dbgen.Account, error) {
	a, err := s.db.GetAccount(ctx, id)
	if db.IsNotFound(err) {
		return a, apperr.NotFound("account not found")
	}
	return a, err
}

func (s *Service) Create(ctx context.Context, in Input, userID *uuid.UUID) (dbgen.Account, error) {
	if in.Name == nil {
		return dbgen.Account{}, apperr.Validation(apperr.FieldError{Field: "name", Message: "is required"})
	}
	a := dbgen.Account{Name: strings.TrimSpace(*in.Name), Kind: in.Kind, Enabled: true, Metadata: []byte("{}")}
	if err := s.apply(&a, in, true); err != nil {
		return dbgen.Account{}, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return dbgen.Account{}, err
	}
	sealed, hint, err := s.seal(id, in.Secret)
	if err != nil {
		return dbgen.Account{}, err
	}
	err = s.db.InTx(ctx, func(q *dbgen.Queries) error {
		a, err = q.CreateAccount(ctx, dbgen.CreateAccountParams{
			ID: id, Name: a.Name, Kind: a.Kind, BaseUrl: a.BaseUrl, DefaultModel: a.DefaultModel, Cloud: a.Cloud,
			Enabled: a.Enabled, SecretCiphertext: sealed, SecretHint: hint, Metadata: a.Metadata, CreatedBy: userID,
		})
		if db.IsUniqueViolation(err) {
			return apperr.Conflict(fmt.Sprintf("an account named %q already exists", a.Name))
		}
		if err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{Action: "account.create", EntityType: "account", EntityID: audit.Ptr(id),
			Payload: map[string]any{"name": a.Name, "kind": a.Kind, "hasSecret": sealed != nil}})
	})
	if err != nil {
		return dbgen.Account{}, err
	}
	s.Reload(ctx)
	return a, nil
}

func (s *Service) Update(ctx context.Context, id uuid.UUID, in Input) (dbgen.Account, error) {
	var out dbgen.Account
	err := s.db.InTx(ctx, func(q *dbgen.Queries) error {
		cur, err := q.GetAccount(ctx, id)
		if db.IsNotFound(err) {
			return apperr.NotFound("account not found")
		}
		if err != nil {
			return err
		}
		in.Kind = cur.Kind
		oldBaseURL := cur.BaseUrl
		if in.Name != nil {
			cur.Name = strings.TrimSpace(*in.Name)
		}
		if err := s.apply(&cur, in, false); err != nil {
			return err
		}
		// The stored credential is bound to its endpoint: pointing the account at
		// another host must not ship the old secret there.
		if cur.BaseUrl != oldBaseURL && cur.SecretCiphertext != nil && (in.Secret == nil || strings.TrimSpace(*in.Secret) == "") {
			return apperr.Validation(apperr.FieldError{Field: "secret", Message: "re-enter the secret when changing the base URL"})
		}
		if out, err = q.UpdateAccount(ctx, dbgen.UpdateAccountParams{
			ID: id, Name: cur.Name, BaseUrl: cur.BaseUrl, DefaultModel: cur.DefaultModel, Cloud: cur.Cloud,
			Enabled: cur.Enabled, Metadata: cur.Metadata,
		}); db.IsUniqueViolation(err) {
			return apperr.Conflict(fmt.Sprintf("an account named %q already exists", cur.Name))
		} else if err != nil {
			return err
		}
		if in.Secret != nil {
			sealed, hint, err := s.seal(id, in.Secret)
			if err != nil {
				return err
			}
			if err := q.SetAccountSecret(ctx, dbgen.SetAccountSecretParams{ID: id, SecretCiphertext: sealed, SecretHint: hint}); err != nil {
				return err
			}
			out.SecretCiphertext, out.SecretHint = sealed, hint
		}
		return audit.Record(ctx, q, audit.Entry{Action: "account.update", EntityType: "account", EntityID: audit.Ptr(id),
			Payload: map[string]any{"name": out.Name, "secretChanged": in.Secret != nil}})
	})
	if err != nil {
		return dbgen.Account{}, err
	}
	s.Reload(ctx)
	return out, nil
}

func (s *Service) Delete(ctx context.Context, id uuid.UUID) error {
	err := s.db.InTx(ctx, func(q *dbgen.Queries) error {
		n, err := q.DeleteAccount(ctx, id)
		if err != nil {
			return err
		}
		if n == 0 {
			return apperr.NotFound("account not found")
		}
		return audit.Record(ctx, q, audit.Entry{Action: "account.delete", EntityType: "account", EntityID: audit.Ptr(id)})
	})
	if err != nil {
		return err
	}
	s.Reload(ctx)
	return nil
}

// apply validates and merges input into a.
func (s *Service) apply(a *dbgen.Account, in Input, create bool) error {
	var errs []apperr.FieldError
	if !nameRe.MatchString(a.Name) {
		errs = append(errs, apperr.FieldError{Field: "name", Message: "lowercase letters, digits and dashes (max 63)"})
	} else if s.gw != nil && slices.Contains(s.gw.ProviderNames(), a.Name) {
		errs = append(errs, apperr.FieldError{Field: "name", Message: "is used by a built-in provider in models.yaml"})
	}
	switch a.Kind {
	case KindClaudeCode, KindCodexCLI, KindOpenAI, KindJira, KindClickUp, KindTrello:
	default:
		return apperr.Validation(apperr.FieldError{Field: "kind", Message: "unknown account kind"})
	}
	if in.BaseURL != nil {
		a.BaseUrl = strings.TrimSuffix(strings.TrimSpace(*in.BaseURL), "/")
	}
	if in.DefaultModel != nil {
		a.DefaultModel = strings.TrimSpace(*in.DefaultModel)
	}
	if in.Enabled != nil {
		a.Enabled = *in.Enabled
	}
	if in.Metadata != nil {
		b, err := json.Marshal(in.Metadata)
		if err != nil {
			return apperr.BadRequest("metadata must be a JSON object")
		}
		a.Metadata = b
	}
	switch a.Kind {
	case KindOpenAI, KindJira:
		if u, err := url.Parse(a.BaseUrl); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			errs = append(errs, apperr.FieldError{Field: "baseUrl", Message: "must be an http(s) URL"})
		} else if a.Kind == KindJira && u.Scheme != "https" {
			errs = append(errs, apperr.FieldError{Field: "baseUrl", Message: "Jira must use https"})
		}
	}
	meta := map[string]any{}
	_ = json.Unmarshal(a.Metadata, &meta)
	if a.Kind == KindJira {
		if e, _ := meta["email"].(string); !strings.Contains(e, "@") {
			errs = append(errs, apperr.FieldError{Field: "metadata.email", Message: "the Atlassian account email is required"})
		}
	}
	if a.Kind == KindTrello {
		if k, _ := meta["apiKey"].(string); len(k) < 8 {
			errs = append(errs, apperr.FieldError{Field: "metadata.apiKey", Message: "the Trello API key is required"})
		}
	}
	// Cloud decides whether the cloud policy applies, so it is derived, never
	// relaxed by the user: CLI providers always are; OpenAI-compatible endpoints
	// are local only when they point at this machine or the private network (a
	// user may still mark a local endpoint as cloud).
	switch a.Kind {
	case KindClaudeCode, KindCodexCLI:
		a.Cloud = true
	case KindOpenAI:
		a.Cloud = !isLocalURL(a.BaseUrl) || (in.Cloud != nil && *in.Cloud)
	default:
		if create || in.Cloud != nil {
			a.Cloud = true
		}
	}
	if in.Secret != nil {
		v := strings.TrimSpace(*in.Secret)
		switch {
		case len(v) > 64<<10:
			errs = append(errs, apperr.FieldError{Field: "secret", Message: "is too large"})
		case a.Kind == KindCodexCLI && v != "" && !json.Valid([]byte(v)):
			errs = append(errs, apperr.FieldError{Field: "secret", Message: "paste the whole ~/.codex/auth.json file"})
		}
	} else if create && a.Kind != KindOpenAI {
		errs = append(errs, apperr.FieldError{Field: "secret", Message: "is required for this kind"})
	}
	if len(errs) > 0 {
		return apperr.Validation(errs...)
	}
	return nil
}

func (s *Service) seal(id uuid.UUID, secret *string) (*string, string, error) {
	if secret == nil || strings.TrimSpace(*secret) == "" {
		return nil, "", nil
	}
	v := strings.TrimSpace(*secret)
	sealed, err := s.box.Seal(id[:], []byte(v))
	if errors.Is(err, secrets.ErrNoKey) {
		return nil, "", apperr.Unavailable(err.Error())
	}
	if err != nil {
		return nil, "", err
	}
	hint := secrets.Hint(v)
	if json.Valid([]byte(v)) && strings.HasPrefix(v, "{") {
		hint = "auth.json"
	}
	return &sealed, hint, nil
}

// Secret decrypts an account's secret (empty when none is stored).
func (s *Service) Secret(a dbgen.Account) (string, error) {
	if a.SecretCiphertext == nil {
		return "", nil
	}
	b, err := s.box.Open(a.ID[:], *a.SecretCiphertext)
	return string(b), err
}

// SaveSecret stores a refreshed credential (used by the gateway for Codex).
func (s *Service) SaveSecret(ctx context.Context, id uuid.UUID, secret string) error {
	sealed, hint, err := s.seal(id, &secret)
	if err != nil {
		return err
	}
	return s.db.SetAccountSecret(ctx, dbgen.SetAccountSecretParams{ID: id, SecretCiphertext: sealed, SecretHint: hint})
}

// Reload pushes enabled model accounts (decrypted) to the gateway.
func (s *Service) Reload(ctx context.Context) {
	if s.gw == nil {
		return
	}
	rows, err := s.db.ListAccounts(ctx)
	if err != nil {
		s.log.Warn("load accounts", "err", err)
		return
	}
	var specs []modelgateway.AccountSpec
	for _, a := range rows {
		if !a.Enabled || !IsModelKind(a.Kind) {
			continue
		}
		secret, err := s.Secret(a)
		if err != nil {
			s.log.Warn("account secret unavailable; account skipped", "account", a.Name, "err", err)
			continue
		}
		specs = append(specs, modelgateway.AccountSpec{
			ID: a.ID, Name: a.Name, Kind: a.Kind, BaseURL: a.BaseUrl, DefaultModel: a.DefaultModel, Cloud: a.Cloud, Secret: secret,
		})
	}
	s.gw.SetAccounts(specs)
}

// Test checks that an account works without sending any task data.
func (s *Service) Test(ctx context.Context, id uuid.UUID) error {
	a, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if IsModelKind(a.Kind) {
		if !a.Enabled {
			return apperr.Conflict("account is disabled")
		}
		return s.gw.HealthOf(ctx, a.Name)
	}
	secret, err := s.Secret(a)
	if err != nil {
		return err
	}
	meta := map[string]any{}
	_ = json.Unmarshal(a.Metadata, &meta)
	var req *http.Request
	switch a.Kind {
	case KindJira:
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, a.BaseUrl+"/rest/api/3/myself", nil)
		if err == nil {
			email, _ := meta["email"].(string)
			req.SetBasicAuth(email, secret)
		}
	case KindClickUp:
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, "https://api.clickup.com/api/v2/user", nil)
		if err == nil {
			req.Header.Set("Authorization", secret)
		}
	case KindTrello:
		key, _ := meta["apiKey"].(string)
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, "https://api.trello.com/1/members/me?fields=username", nil)
		if err == nil {
			req.Header.Set("Authorization", fmt.Sprintf(`OAuth oauth_consumer_key="%s", oauth_token="%s"`, key, secret))
		}
	}
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	res, err := s.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s unreachable: %w", a.Kind, err)
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 300 {
		return fmt.Errorf("%s rejected the credentials (HTTP %d)", a.Kind, res.StatusCode)
	}
	return nil
}

func isLocalURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := u.Hostname()
	if host == "localhost" || host == "host.docker.internal" || strings.HasSuffix(host, ".local") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate())
}

// ValidateModelChoice checks that name is an enabled model account or a
// built-in provider, so a run fails at creation rather than mid-workflow.
func (s *Service) ValidateModelChoice(ctx context.Context, name string) error {
	if s.gw != nil && slices.Contains(s.gw.ProviderNames(), name) {
		return nil
	}
	a, err := s.db.GetAccountByName(ctx, name)
	if db.IsNotFound(err) {
		return apperr.Validation(apperr.FieldError{Field: "account", Message: fmt.Sprintf("no account named %q", name)})
	}
	if err != nil {
		return err
	}
	if !IsModelKind(a.Kind) {
		return apperr.Validation(apperr.FieldError{Field: "account", Message: fmt.Sprintf("%q is a %s account, not a model provider", name, a.Kind)})
	}
	if !a.Enabled {
		return apperr.Validation(apperr.FieldError{Field: "account", Message: fmt.Sprintf("account %q is disabled", name)})
	}
	return nil
}

// Credential returns an account with its decrypted secret and metadata.
func (s *Service) Credential(ctx context.Context, id uuid.UUID) (dbgen.Account, string, map[string]any, error) {
	a, err := s.Get(ctx, id)
	if err != nil {
		return a, "", nil, err
	}
	secret, err := s.Secret(a)
	if err != nil {
		return a, "", nil, err
	}
	meta := map[string]any{}
	_ = json.Unmarshal(a.Metadata, &meta)
	return a, secret, meta, nil
}
