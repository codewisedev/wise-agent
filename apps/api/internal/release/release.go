// Package release holds where each repository releases to (remote, git
// account, base branch), each project's pull request policy and the
// "release" setting. Pushes and pull requests themselves are made only by the
// trusted git worker, after an approval (see ADR 0010).
package release

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/google/uuid"

	"wise/apps/api/internal/accounts"
	"wise/apps/api/internal/apperr"
	"wise/apps/api/internal/audit"
	"wise/apps/api/internal/db"
	"wise/apps/api/internal/db/dbgen"
	"wise/apps/api/internal/githost"
	"wise/apps/api/internal/settings"
)

// BranchPrefix is the only branch namespace Wise pushes to (fixed).
const BranchPrefix = "wise/"

// Repo is a repository's release target.
type Repo struct {
	Remote     string     `json:"remote,omitempty"` // normalized https URL
	AccountID  *uuid.UUID `json:"accountId,omitempty"`
	BaseBranch string     `json:"baseBranch,omitempty"`
}

// Configured reports whether the repository can be released.
func (r Repo) Configured() bool { return r.Remote != "" && r.AccountID != nil && r.BaseBranch != "" }

// Policy is a project's pull request policy.
type Policy struct {
	PullRequests string `json:"pullRequests"` // off, ask, auto
	Draft        bool   `json:"draft"`
}

// Settings is the "release" setting.
type Settings struct {
	PRTitleTemplate      string `json:"pr_title_template"`
	PRBodyIncludesReport bool   `json:"pr_body_includes_report"`
	PushTimeoutS         int    `json:"push_timeout_s"`
	PRRefreshMinutes     int    `json:"pr_refresh_minutes"`
}

// DefaultSettings applies until the setting loads.
func DefaultSettings() Settings {
	return Settings{PRTitleTemplate: "{task}", PRBodyIncludesReport: true, PushTimeoutS: 120, PRRefreshMinutes: 10}
}

// ParseSettings validates the setting.
func ParseSettings(raw json.RawMessage) (Settings, error) {
	st := DefaultSettings()
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&st); err != nil {
		return st, apperr.Validation(apperr.FieldError{Field: "value", Message: err.Error()})
	}
	var errs []apperr.FieldError
	if t := strings.TrimSpace(st.PRTitleTemplate); t == "" || len(t) > 200 {
		errs = append(errs, apperr.FieldError{Field: "pr_title_template", Message: "1-200 characters"})
	}
	if st.PushTimeoutS < 10 || st.PushTimeoutS > 1800 {
		errs = append(errs, apperr.FieldError{Field: "push_timeout_s", Message: "10-1800"})
	}
	if st.PRRefreshMinutes < 1 || st.PRRefreshMinutes > 1440 {
		errs = append(errs, apperr.FieldError{Field: "pr_refresh_minutes", Message: "1-1440"})
	}
	if len(errs) > 0 {
		return st, apperr.Validation(errs...)
	}
	return st, nil
}

// Service manages release configuration.
type Service struct {
	db       *db.DB
	accounts *accounts.Service
	mu       sync.RWMutex
	st       Settings
}

// New returns a Service.
func New(d *db.DB, acc *accounts.Service) *Service {
	return &Service{db: d, accounts: acc, st: DefaultSettings()}
}

// SetSettings applies the setting.
func (s *Service) SetSettings(st Settings) { s.mu.Lock(); s.st = st; s.mu.Unlock() }

// Settings returns the setting.
func (s *Service) Settings() Settings { s.mu.RLock(); defer s.mu.RUnlock(); return s.st }

// RegisterSetting declares the "release" setting.
func RegisterSetting(st *settings.Service, file string, svc *Service) {
	st.Register(settings.Def{
		Key: "release", Title: "Release",
		Description: "Pull request title and description, push timeout and how often pull request states are refreshed. Where each repository pushes is set on the repository; pull requests per project.",
		Default:     settings.FileDefault(file),
		Validate: func(_ context.Context, v json.RawMessage) (json.RawMessage, error) {
			p, err := ParseSettings(v)
			if err != nil {
				return nil, err
			}
			return json.Marshal(p)
		},
		Apply: func(_ context.Context, v json.RawMessage) error {
			p, err := ParseSettings(v)
			if err == nil {
				svc.SetSettings(p)
			}
			return err
		},
	})
}

// ParseRepo reads a repository's release column.
func ParseRepo(raw []byte) Repo {
	var r Repo
	_ = json.Unmarshal(raw, &r)
	return r
}

var branchRe = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,200}$`)

// Suggestion is what Detect proposes from the repository's git metadata.
type Suggestion struct {
	Remote     string      `json:"remote,omitempty"`
	BaseBranch string      `json:"baseBranch,omitempty"`
	Accounts   []uuid.UUID `json:"accounts"` // git accounts serving the remote's host
	Problem    string      `json:"problem,omitempty"`
}

func (s *Service) repo(ctx context.Context, id uuid.UUID) (dbgen.Repository, error) {
	r, err := s.db.GetRepository(ctx, id)
	if db.IsNotFound(err) {
		return r, apperr.NotFound("repository not found")
	}
	return r, err
}

// Detect proposes a release target from the repository's remotes ("origin" first).
func (s *Service) Detect(ctx context.Context, id uuid.UUID) (Suggestion, error) {
	r, err := s.repo(ctx, id)
	if err != nil {
		return Suggestion{}, err
	}
	sug := Suggestion{Accounts: []uuid.UUID{}}
	if r.DefaultBranch != nil {
		sug.BaseBranch = *r.DefaultBranch
	}
	var remotes []struct {
		Name string `json:"name"`
		URL  string `json:"url"`
	}
	_ = json.Unmarshal(r.GitRemotes, &remotes)
	candidates := []string{}
	for _, rm := range remotes {
		if rm.Name == "origin" {
			candidates = append([]string{rm.URL}, candidates...)
		} else {
			candidates = append(candidates, rm.URL)
		}
	}
	if r.RemoteUrl != nil {
		candidates = append(candidates, *r.RemoteUrl)
	}
	var remote githost.Remote
	for _, c := range candidates {
		if rm, err := githost.ParseRemote(c); err == nil {
			remote, sug.Remote = rm, rm.URL()
			break
		}
	}
	if sug.Remote == "" {
		sug.Problem = "no GitHub or GitLab remote found: enter the repository's https address"
		return sug, nil
	}
	accs, err := s.accounts.List(ctx)
	if err != nil {
		return sug, err
	}
	for _, a := range accs {
		if accounts.IsGitKind(a.Kind) && a.Enabled && githost.Matches(a.Kind, a.BaseUrl, remote) {
			sug.Accounts = append(sug.Accounts, a.ID)
		}
	}
	if len(sug.Accounts) == 0 {
		sug.Problem = "no git account for " + remote.Host + ": add a GitHub or GitLab token in Settings -> Accounts"
	}
	return sug, nil
}

// account loads a git account and checks it serves the remote.
func (s *Service) account(ctx context.Context, id uuid.UUID, remote githost.Remote) (dbgen.Account, error) {
	a, err := s.accounts.Get(ctx, id)
	if err != nil {
		return a, err
	}
	if !accounts.IsGitKind(a.Kind) {
		return a, apperr.Validation(apperr.FieldError{Field: "accountId", Message: "not a GitHub or GitLab account"})
	}
	if !githost.Matches(a.Kind, a.BaseUrl, remote) {
		return a, apperr.Validation(apperr.FieldError{Field: "accountId", Message: fmt.Sprintf("the account is for %s, the remote is on %s", githost.HostOf(a.BaseUrl), remote.Host)})
	}
	return a, nil
}

// SetRepo validates and stores a repository's release target.
func (s *Service) SetRepo(ctx context.Context, id uuid.UUID, in Repo, userID *uuid.UUID) (Repo, error) {
	if _, err := s.repo(ctx, id); err != nil {
		return Repo{}, err
	}
	out := Repo{}
	if strings.TrimSpace(in.Remote) != "" {
		rm, err := githost.ParseRemote(in.Remote)
		if err != nil {
			return Repo{}, apperr.Validation(apperr.FieldError{Field: "remote", Message: err.Error()})
		}
		out.Remote = rm.URL()
		if in.AccountID != nil {
			if _, err := s.account(ctx, *in.AccountID, rm); err != nil {
				return Repo{}, err
			}
			out.AccountID = in.AccountID
		}
	} else if in.AccountID != nil {
		return Repo{}, apperr.Validation(apperr.FieldError{Field: "remote", Message: "set the remote first"})
	}
	b := strings.TrimSpace(in.BaseBranch)
	if b != "" {
		if !branchRe.MatchString(b) || strings.Contains(b, "..") || strings.HasPrefix(b, BranchPrefix) {
			return Repo{}, apperr.Validation(apperr.FieldError{Field: "baseBranch", Message: "a branch name outside wise/"})
		}
		out.BaseBranch = b
	}
	raw, _ := json.Marshal(out)
	err := s.db.InTx(ctx, func(q *dbgen.Queries) error {
		if _, err := q.SetRepositoryRelease(ctx, dbgen.SetRepositoryReleaseParams{ID: id, Release: raw}); err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{Action: "repository.release", EntityType: "repository", EntityID: &id,
			Payload: map[string]any{"remote": out.Remote, "accountId": out.AccountID, "baseBranch": out.BaseBranch}})
	})
	return out, err
}

// Repo returns a repository's release target.
func (s *Service) Repo(ctx context.Context, id uuid.UUID) (Repo, error) {
	r, err := s.repo(ctx, id)
	if err != nil {
		return Repo{}, err
	}
	return ParseRepo(r.Release), nil
}

// Test reads the repository with the account's token (it proves access, not
// push permission: the first push does).
func (s *Service) Test(ctx context.Context, id uuid.UUID) (string, error) {
	cfg, err := s.Repo(ctx, id)
	if err != nil {
		return "", err
	}
	if cfg.Remote == "" || cfg.AccountID == nil {
		return "", apperr.Conflict("set the remote and the account first")
	}
	rm, err := githost.ParseRemote(cfg.Remote)
	if err != nil {
		return "", err
	}
	a, err := s.account(ctx, *cfg.AccountID, rm)
	if err != nil {
		return "", err
	}
	tok, err := s.accounts.Secret(a)
	if err != nil {
		return "", err
	}
	c := &githost.Client{Kind: a.Kind, BaseURL: a.BaseUrl, Token: tok}
	def, err := c.CheckRepo(ctx, rm)
	if err != nil {
		return "", apperr.BadRequest(err.Error())
	}
	return def, nil
}

// ParsePolicy reads a project's policy.
func ParsePolicy(raw []byte) Policy {
	p := Policy{PullRequests: "ask", Draft: true}
	_ = json.Unmarshal(raw, &p)
	return p
}

// Policy returns a project's pull request policy.
func (s *Service) Policy(ctx context.Context, projectID uuid.UUID) (Policy, error) {
	raw, err := s.db.GetProjectReleasePolicy(ctx, projectID)
	if db.IsNotFound(err) {
		return Policy{}, apperr.NotFound("project not found")
	}
	return ParsePolicy(raw), err
}

// SetPolicy stores a project's pull request policy.
func (s *Service) SetPolicy(ctx context.Context, projectID uuid.UUID, p Policy) (Policy, error) {
	switch p.PullRequests {
	case "off", "ask", "auto":
	default:
		return p, apperr.Validation(apperr.FieldError{Field: "pullRequests", Message: "off, ask or auto"})
	}
	raw, _ := json.Marshal(p)
	err := s.db.InTx(ctx, func(q *dbgen.Queries) error {
		if _, err := q.SetProjectReleasePolicy(ctx, dbgen.SetProjectReleasePolicyParams{ID: projectID, ReleasePolicy: raw}); db.IsNotFound(err) {
			return apperr.NotFound("project not found")
		} else if err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{Action: "project.release_policy", EntityType: "project", EntityID: &projectID,
			Payload: map[string]any{"pullRequests": p.PullRequests, "draft": p.Draft}})
	})
	return p, err
}
