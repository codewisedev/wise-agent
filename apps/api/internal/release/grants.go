package release

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"wise/apps/api/internal/accounts"
	"wise/apps/api/internal/apperr"
	"wise/apps/api/internal/audit"
	"wise/apps/api/internal/db"
	"wise/apps/api/internal/db/dbgen"
	"wise/apps/api/internal/githost"
	"wise/apps/api/internal/secretguard"
)

// Artifact kinds of the release stage.
const (
	KindRequest     = "release_request" // what a push approval pins
	KindPush        = "push"
	KindPullRequest = "pull_request"
)

// Request is the release_request artifact: exactly what the push approval covers.
type Request struct {
	WorkspaceID  uuid.UUID `json:"workspaceId"`
	RepositoryID uuid.UUID `json:"repositoryId"`
	SHA          string    `json:"sha"`
	Branch       string    `json:"branch"`
	Remote       string    `json:"remote"`
	AccountID    uuid.UUID `json:"accountId"`
	AccountName  string    `json:"accountName"`
	BaseBranch   string    `json:"baseBranch"`
}

var shaRe = regexp.MustCompile(`^[0-9a-f]{40,64}$`)

// CheckTarget applies the fixed push rules: only wise/ branches, never the
// base branch, a full commit id, an https remote on the account's host.
func CheckTarget(r Request, kind, baseURL string) error {
	switch {
	case !strings.HasPrefix(r.Branch, BranchPrefix) || len(r.Branch) <= len(BranchPrefix) || strings.ContainsAny(r.Branch, " ~^:?*[\\") ||
		strings.Contains(r.Branch, ".."):
		return fmt.Errorf("Wise pushes only wise/… branches (not %q)", r.Branch)
	case r.Branch == r.BaseBranch:
		return errors.New("Wise never pushes to the base branch")
	case !shaRe.MatchString(r.SHA):
		return errors.New("the push needs a full commit id")
	}
	rm, err := githost.ParseRemote(r.Remote)
	if err != nil {
		return err
	}
	if rm.URL() != r.Remote {
		return errors.New("the remote is not normalized")
	}
	if !accounts.IsGitKind(kind) || !githost.Matches(kind, baseURL, rm) {
		return fmt.Errorf("the account does not serve %s", rm.Host)
	}
	return nil
}

// Grant is what the git worker receives for one approved push or pull request.
type Grant struct {
	PushID      uuid.UUID `json:"pushId"`
	PRID        uuid.UUID `json:"prId,omitempty"`
	WorkspaceID uuid.UUID `json:"workspaceId"`
	SHA         string    `json:"sha"`
	Branch      string    `json:"branch"`
	Remote      string    `json:"remote"`
	BaseBranch  string    `json:"baseBranch"`
	Kind        string    `json:"kind"`
	BaseURL     string    `json:"baseUrl"`
	Token       string    `json:"token"`
	TimeoutS    int       `json:"timeoutS"`
	// pull requests
	Title string `json:"title,omitempty"`
	Body  string `json:"body,omitempty"`
	Draft bool   `json:"draft,omitempty"`
}

// approvedRequest loads the release request an approval pins.
func (s *Service) approvedRequest(ctx context.Context, approvalID uuid.UUID) (dbgen.Approval, Request, error) {
	ap, err := s.db.GetApproval(ctx, approvalID)
	if db.IsNotFound(err) {
		return ap, Request{}, apperr.NotFound("approval not found")
	}
	if err != nil {
		return ap, Request{}, err
	}
	if ap.Status != "approved" {
		return ap, Request{}, apperr.Conflict("the push is not approved (" + ap.Status + ")")
	}
	var pinned []struct {
		ArtifactID uuid.UUID `json:"artifactId"`
		Kind       string    `json:"kind"`
	}
	_ = json.Unmarshal(ap.Artifacts, &pinned)
	for _, p := range pinned {
		if p.Kind != KindRequest {
			continue
		}
		art, err := s.db.GetArtifact(ctx, p.ArtifactID)
		if err != nil {
			return ap, Request{}, err
		}
		var r Request
		if err := json.Unmarshal(art.Content, &r); err != nil {
			return ap, Request{}, err
		}
		return ap, r, nil
	}
	return ap, Request{}, apperr.Conflict("the approval is not a push approval")
}

func (s *Service) gitAccount(ctx context.Context, id uuid.UUID) (dbgen.Account, string, error) {
	a, err := s.accounts.Get(ctx, id)
	if err != nil {
		return a, "", err
	}
	if !accounts.IsGitKind(a.Kind) || !a.Enabled {
		return a, "", apperr.Conflict("the git account is disabled or not a git account")
	}
	tok, err := s.accounts.Secret(a)
	if err != nil || tok == "" {
		return a, "", apperr.Conflict("the git account has no token")
	}
	return a, tok, nil
}

// PushGrant checks a push approval and grants the git worker one push of
// exactly what was approved (retries are allowed after a failure; a push
// that succeeded is never repeated).
func (s *Service) PushGrant(ctx context.Context, approvalID uuid.UUID) (Grant, error) {
	ap, r, err := s.approvedRequest(ctx, approvalID)
	if err != nil {
		return Grant{}, err
	}
	a, tok, err := s.gitAccount(ctx, r.AccountID)
	if err != nil {
		return Grant{}, err
	}
	if err := CheckTarget(r, a.Kind, a.BaseUrl); err != nil {
		return Grant{}, apperr.Conflict(err.Error())
	}
	w, err := s.db.GetWorkspace(ctx, r.WorkspaceID)
	if err != nil {
		return Grant{}, apperr.Conflict("the task workspace is gone")
	}
	if w.TaskID != ap.TaskID || w.DeliveredSha == nil || *w.DeliveredSha != r.SHA {
		return Grant{}, apperr.Conflict("the approved commit is not the task's delivered commit")
	}
	timeout := s.Settings().PushTimeoutS
	var push dbgen.GitPush
	err = s.db.InTx(ctx, func(q *dbgen.Queries) error {
		cur, err := q.LockGitPushByApproval(ctx, approvalID)
		switch {
		case db.IsNotFound(err):
			id, _ := uuid.NewV7()
			repo := r.RepositoryID
			push, err = q.InsertGitPush(ctx, dbgen.InsertGitPushParams{ID: id, ApprovalID: approvalID, TaskID: ap.TaskID, WorkflowRunID: &ap.WorkflowRunID,
				RepositoryID: &repo, AccountID: &a.ID, WorkspaceID: r.WorkspaceID, Remote: r.Remote, Branch: r.Branch, Sha: r.SHA, BaseBranch: r.BaseBranch})
			if err != nil {
				return err
			}
		case err != nil:
			return err
		case cur.Status == "pushed":
			return apperr.Conflict("already pushed")
		case cur.Status == "granted" && time.Since(cur.UpdatedAt) < 2*time.Duration(timeout)*time.Second:
			return apperr.Conflict("a push of this approval is in progress")
		default: // failed, or a grant that never reported back
			if cur.Status == "granted" {
				msg := "the push did not report back"
				if _, err := q.FinishGitPush(ctx, dbgen.FinishGitPushParams{ID: cur.ID, Status: "failed", Error: &msg}); err != nil {
					return err
				}
			}
			if push, err = q.RetryGitPush(ctx, cur.ID); err != nil {
				return err
			}
		}
		return audit.Record(ctx, q, audit.Entry{Action: "git.push_granted", EntityType: "git_push", EntityID: &push.ID,
			Payload: map[string]any{"approvalId": approvalID, "branch": r.Branch, "sha": r.SHA, "remote": r.Remote, "attempt": push.Attempts}})
	})
	if err != nil {
		return Grant{}, err
	}
	return Grant{PushID: push.ID, WorkspaceID: r.WorkspaceID, SHA: r.SHA, Branch: r.Branch, Remote: r.Remote, BaseBranch: r.BaseBranch,
		Kind: a.Kind, BaseURL: a.BaseUrl, Token: tok, TimeoutS: timeout}, nil
}

// PushResult records what the git worker reports.
func (s *Service) PushResult(ctx context.Context, pushID uuid.UUID, ok, existed bool, msg string) (dbgen.GitPush, error) {
	status := "pushed"
	var e *string
	if !ok {
		status = "failed"
		m := clip(msg, 1000)
		e = &m
	}
	var p dbgen.GitPush
	err := s.db.InTx(ctx, func(q *dbgen.Queries) error {
		var err error
		p, err = q.FinishGitPush(ctx, dbgen.FinishGitPushParams{ID: pushID, Status: status, Existed: existed, Error: e})
		if db.IsNotFound(err) {
			return apperr.Conflict("no push in progress")
		}
		if err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{Action: "git.push_" + status, EntityType: "git_push", EntityID: &pushID,
			Payload: map[string]any{"branch": p.Branch, "sha": p.Sha, "existed": existed, "error": e}})
	})
	return p, err
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// PRGrant grants opening one pull request for a successful push, when the
// project's policy is "auto", or "ask" and you approved it (prApprovalID).
func (s *Service) PRGrant(ctx context.Context, pushID uuid.UUID, prApprovalID *uuid.UUID) (Grant, error) {
	p, err := s.db.GetGitPush(ctx, pushID)
	if db.IsNotFound(err) {
		return Grant{}, apperr.NotFound("push not found")
	}
	if err != nil {
		return Grant{}, err
	}
	if p.Status != "pushed" {
		return Grant{}, apperr.Conflict("the branch is not pushed")
	}
	task, err := s.db.GetTask(ctx, p.TaskID)
	if err != nil {
		return Grant{}, err
	}
	pol, err := s.Policy(ctx, task.ProjectID)
	if err != nil {
		return Grant{}, err
	}
	switch pol.PullRequests {
	case "off":
		return Grant{}, apperr.Conflict("the project does not open pull requests")
	case "ask":
		if prApprovalID == nil {
			return Grant{}, apperr.Conflict("opening a pull request needs your approval")
		}
		ap, err := s.db.GetApproval(ctx, *prApprovalID)
		if err != nil || ap.Status != "approved" || ap.TaskID != p.TaskID || !strings.HasSuffix(ap.Gate, "-pr") {
			return Grant{}, apperr.Conflict("the pull request is not approved")
		}
	}
	if p.AccountID == nil {
		return Grant{}, apperr.Conflict("the push has no git account")
	}
	a, tok, err := s.gitAccount(ctx, *p.AccountID)
	if err != nil {
		return Grant{}, err
	}
	st := s.Settings()
	title := clip(strings.ReplaceAll(st.PRTitleTemplate, "{task}", task.Title), 250)
	body := "Opened by Wise after your approval. Merging stays with you.\n"
	if st.PRBodyIncludesReport && p.WorkflowRunID != nil {
		if rep, err := s.db.GetLatestArtifact(ctx, dbgen.GetLatestArtifactParams{TaskID: p.TaskID, Kind: "final_report"}); err == nil {
			body = Markdown(rep.Content) + "\n---\n" + body
		}
	}
	// The description comes from agent outputs and goes to a git host: redact credentials (the secret guard).
	title, body = secretguard.Redact(title), secretguard.Redact(body)
	var pr dbgen.GitPullRequest
	err = s.db.InTx(ctx, func(q *dbgen.Queries) error {
		cur, err := q.GetPullRequestByPush(ctx, pushID)
		switch {
		case db.IsNotFound(err):
			id, _ := uuid.NewV7()
			pr, err = q.InsertPullRequest(ctx, dbgen.InsertPullRequestParams{ID: id, PushID: pushID, ApprovalID: prApprovalID, Draft: pol.Draft, Title: title})
			if err != nil {
				return err
			}
		case err != nil:
			return err
		case cur.Status == "opened":
			return apperr.Conflict("the pull request is already open")
		case cur.Status == "failed":
			if pr, err = q.RetryPullRequest(ctx, cur.ID); err != nil {
				return err
			}
		default:
			pr = cur // granted before but never reported: the worker finds an existing one first
		}
		return audit.Record(ctx, q, audit.Entry{Action: "git.pr_granted", EntityType: "git_pull_request", EntityID: &pr.ID,
			Payload: map[string]any{"pushId": pushID, "draft": pol.Draft}})
	})
	if err != nil {
		return Grant{}, err
	}
	return Grant{PushID: pushID, PRID: pr.ID, WorkspaceID: p.WorkspaceID, SHA: p.Sha, Branch: p.Branch, Remote: p.Remote,
		BaseBranch: p.BaseBranch, Kind: a.Kind, BaseURL: a.BaseUrl, Token: tok, TimeoutS: st.PushTimeoutS, Title: title, Body: body, Draft: pol.Draft}, nil
}

// PRResult records what the git worker reports about a pull request.
func (s *Service) PRResult(ctx context.Context, prID uuid.UUID, ok bool, pr githost.PR, msg string) (dbgen.GitPullRequest, error) {
	status := "opened"
	var e *string
	var num *int32
	var url, state *string
	if ok {
		n := int32(pr.Number)
		num, url, state = &n, &pr.URL, &pr.State
	} else {
		status = "failed"
		m := clip(msg, 1000)
		e = &m
	}
	var out dbgen.GitPullRequest
	err := s.db.InTx(ctx, func(q *dbgen.Queries) error {
		var err error
		out, err = q.FinishPullRequest(ctx, dbgen.FinishPullRequestParams{ID: prID, Status: status, Number: num, Url: url, State: state, Error: e})
		if db.IsNotFound(err) {
			return apperr.Conflict("no pull request in progress")
		}
		if err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{Action: "git.pr_" + status, EntityType: "git_pull_request", EntityID: &prID,
			Payload: map[string]any{"number": num, "url": url, "error": e}})
	})
	return out, err
}

// RefreshPullRequests reads the state (open, merged, closed) of open pull requests.
func (s *Service) RefreshPullRequests(ctx context.Context) {
	rows, err := s.db.PullRequestsToRefresh(ctx, int32(s.Settings().PRRefreshMinutes))
	if err != nil {
		return
	}
	for _, r := range rows {
		if r.AccountID == nil || r.Number == nil {
			continue
		}
		a, tok, err := s.gitAccount(ctx, *r.AccountID)
		if err != nil {
			continue
		}
		rm, err := githost.ParseRemote(r.Remote)
		if err != nil {
			continue
		}
		c := &githost.Client{Kind: a.Kind, BaseURL: a.BaseUrl, Token: tok}
		if pr, err := c.GetPR(ctx, rm, int(*r.Number)); err == nil {
			_ = s.db.SetPullRequestState(ctx, dbgen.SetPullRequestStateParams{ID: r.ID, State: &pr.State, Draft: pr.Draft})
		}
	}
}

// RunRefresher refreshes pull request states until ctx ends.
func (s *Service) RunRefresher(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.RefreshPullRequests(ctx)
		}
	}
}

// Markdown renders a final report artifact (JSON) as markdown for a pull request.
func Markdown(raw json.RawMessage) string {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return clip(string(raw), 20000)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if i := indexOf(keys, "summary"); i > 0 {
		keys[0], keys[i] = keys[i], keys[0]
	}
	var b strings.Builder
	for _, k := range keys {
		b.WriteString("### " + strings.ReplaceAll(strings.ToUpper(k[:1])+k[1:], "_", " ") + "\n\n")
		writeValue(&b, m[k], 0)
		b.WriteString("\n")
	}
	return clip(b.String(), 60000)
}

func indexOf(xs []string, x string) int {
	for i, v := range xs {
		if v == x {
			return i
		}
	}
	return -1
}

func writeValue(b *strings.Builder, v any, depth int) {
	indent := strings.Repeat("  ", depth)
	switch t := v.(type) {
	case []any:
		for _, e := range t {
			switch e.(type) {
			case map[string]any, []any:
				b.WriteString(indent + "- \n")
				writeValue(b, e, depth+1)
			default:
				b.WriteString(indent + "- " + fmt.Sprint(e) + "\n")
			}
		}
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			switch t[k].(type) {
			case map[string]any, []any:
				b.WriteString(indent + "- **" + k + "**:\n")
				writeValue(b, t[k], depth+1)
			default:
				b.WriteString(indent + "- **" + k + "**: " + fmt.Sprint(t[k]) + "\n")
			}
		}
	default:
		b.WriteString(indent + fmt.Sprint(t) + "\n")
	}
}
