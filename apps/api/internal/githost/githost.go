// Package githost talks to git hosting services (GitHub, GitLab) for the
// release stage: reading a repository, opening (draft) pull requests and
// reading their state. It never merges: merging always stays manual.
package githost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Kinds of hosts (the account kinds).
const (
	GitHub = "github"
	GitLab = "gitlab"
)

// Remote is a normalized HTTPS remote: https://<host>/<path>.git
type Remote struct {
	Host string // host[:port]
	Path string // owner/repo (GitLab: group/subgroup/repo)
}

// URL is the HTTPS clone/push URL.
func (r Remote) URL() string { return "https://" + r.Host + "/" + r.Path + ".git" }

// Web is the repository's web page.
func (r Remote) Web() string { return "https://" + r.Host + "/" + r.Path }

var (
	scpRe  = regexp.MustCompile(`^(?:[A-Za-z0-9._-]+@)?([A-Za-z0-9.-]+):([A-Za-z0-9._~/-]+?)(?:\.git)?/?$`)
	pathRe = regexp.MustCompile(`^[A-Za-z0-9._-]+(/[A-Za-z0-9._-]+)+$`)
)

// ParseRemote normalizes a git remote (https, ssh:// or scp-like
// git@host:owner/repo.git) to an HTTPS remote. Credentials in the URL are dropped.
func ParseRemote(raw string) (Remote, error) {
	raw = strings.TrimSpace(raw)
	var host, path string
	switch {
	case strings.HasPrefix(raw, "https://"), strings.HasPrefix(raw, "ssh://"), strings.HasPrefix(raw, "http://"):
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			return Remote{}, fmt.Errorf("not a git remote URL: %q", raw)
		}
		if u.Scheme == "http" {
			return Remote{}, errors.New("plain http remotes are not supported (tokens would travel unencrypted)")
		}
		host = u.Hostname()
		if p := u.Port(); p != "" && u.Scheme == "https" {
			host += ":" + p
		}
		path = u.Path
	default:
		m := scpRe.FindStringSubmatch(raw)
		if m == nil {
			return Remote{}, fmt.Errorf("not a git remote: %q", raw)
		}
		host, path = m[1], m[2]
	}
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	if !pathRe.MatchString(path) || strings.Contains(path, "..") {
		return Remote{}, fmt.Errorf("unexpected repository path %q", path)
	}
	return Remote{Host: strings.ToLower(host), Path: path}, nil
}

// HostOf returns the host[:port] of a base URL ("https://gitlab.example.org").
func HostOf(baseURL string) string {
	u, err := url.Parse(baseURL)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Host)
}

// DefaultBaseURL is the web URL of a host kind.
func DefaultBaseURL(kind string) string {
	if kind == GitLab {
		return "https://gitlab.com"
	}
	return "https://github.com"
}

// Matches reports whether an account (kind, base URL) serves a remote.
func Matches(kind, baseURL string, r Remote) bool {
	if baseURL == "" {
		baseURL = DefaultBaseURL(kind)
	}
	return HostOf(baseURL) == r.Host
}

// PR is a pull (merge) request.
type PR struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
	State  string `json:"state"` // open, merged, closed
	Draft  bool   `json:"draft"`
	Title  string `json:"title"`
}

// PRInput opens a pull request.
type PRInput struct {
	Title, Body, Head, Base string
	Draft                   bool
}

// Client is one account's API access.
type Client struct {
	Kind    string
	BaseURL string // web base URL (https://github.com, https://gitlab.com, self-hosted)
	Token   string
	HTTP    *http.Client
	// APIBase overrides the API root (tests).
	APIBase string
}

// ErrAuth means the host rejected the token.
var ErrAuth = errors.New("the git host rejected the token")

func (c *Client) api() string {
	if c.APIBase != "" {
		return strings.TrimSuffix(c.APIBase, "/")
	}
	base := c.BaseURL
	if base == "" {
		base = DefaultBaseURL(c.Kind)
	}
	base = strings.TrimSuffix(base, "/")
	if c.Kind == GitLab {
		return base + "/api/v4"
	}
	if HostOf(base) == "github.com" {
		return "https://api.github.com"
	}
	return base + "/api/v3" // GitHub Enterprise Server
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.api()+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Kind == GitLab {
		req.Header.Set("PRIVATE-TOKEN", c.Token)
	} else {
		req.Header.Set("Authorization", "Bearer "+c.Token)
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	}
	base := c.HTTP
	if base == nil {
		base = &http.Client{Timeout: 30 * time.Second}
	}
	hc := *base
	hc.CheckRedirect = sameOrigin // the token header must never follow a redirect elsewhere
	res, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("%s unreachable: %w", c.Kind, scrub(err, c.Token))
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	switch {
	case res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden:
		return fmt.Errorf("%w (HTTP %d)", ErrAuth, res.StatusCode)
	case res.StatusCode >= 300:
		return fmt.Errorf("%s: HTTP %d: %s", c.Kind, res.StatusCode, hostMessage(raw))
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// sameOrigin refuses redirects that change the scheme, host or port.
func sameOrigin(req *http.Request, via []*http.Request) error {
	if len(via) >= 5 {
		return errors.New("too many redirects")
	}
	first := via[0].URL
	if req.URL.Scheme != first.Scheme || req.URL.Host != first.Host {
		return fmt.Errorf("refused a redirect from %s to %s", first.Host, req.URL.Host)
	}
	return nil
}

func hostMessage(raw []byte) string {
	var m struct {
		Message any `json:"message"`
		Error   any `json:"error"`
	}
	if json.Unmarshal(raw, &m) == nil {
		if m.Message != nil {
			return fmt.Sprint(m.Message)
		}
		if m.Error != nil {
			return fmt.Sprint(m.Error)
		}
	}
	s := string(raw)
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

func scrub(err error, token string) error {
	if token == "" {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), token, "[token]"))
}

func gitlabID(path string) string { return url.PathEscape(path) }

// User returns the token's user name (a credential check).
func (c *Client) User(ctx context.Context) (string, error) {
	var u struct {
		Login    string `json:"login"`
		Username string `json:"username"`
	}
	if err := c.do(ctx, http.MethodGet, "/user", nil, &u); err != nil {
		return "", err
	}
	if u.Login != "" {
		return u.Login, nil
	}
	return u.Username, nil
}

// CheckRepo verifies the token can see the repository and returns its default branch.
func (c *Client) CheckRepo(ctx context.Context, r Remote) (string, error) {
	if c.Kind == GitLab {
		var p struct {
			DefaultBranch string `json:"default_branch"`
		}
		if err := c.do(ctx, http.MethodGet, "/projects/"+gitlabID(r.Path), nil, &p); err != nil {
			return "", err
		}
		return p.DefaultBranch, nil
	}
	var p struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := c.do(ctx, http.MethodGet, "/repos/"+r.Path, nil, &p); err != nil {
		return "", err
	}
	return p.DefaultBranch, nil
}

// CreatePR opens a pull request (GitLab: a merge request; a draft is a "Draft:" title).
func (c *Client) CreatePR(ctx context.Context, r Remote, in PRInput) (PR, error) {
	if c.Kind == GitLab {
		title := in.Title
		if in.Draft && !strings.HasPrefix(strings.ToLower(title), "draft:") {
			title = "Draft: " + title
		}
		var mr struct {
			IID    int    `json:"iid"`
			WebURL string `json:"web_url"`
			State  string `json:"state"`
			Title  string `json:"title"`
		}
		err := c.do(ctx, http.MethodPost, "/projects/"+gitlabID(r.Path)+"/merge_requests",
			map[string]any{"source_branch": in.Head, "target_branch": in.Base, "title": title, "description": in.Body}, &mr)
		if err != nil {
			return PR{}, err
		}
		return PR{Number: mr.IID, URL: mr.WebURL, State: gitlabState(mr.State), Draft: in.Draft, Title: mr.Title}, nil
	}
	var pr ghPR
	err := c.do(ctx, http.MethodPost, "/repos/"+r.Path+"/pulls",
		map[string]any{"title": in.Title, "body": in.Body, "head": in.Head, "base": in.Base, "draft": in.Draft}, &pr)
	if err != nil {
		return PR{}, err
	}
	return pr.toPR(), nil
}

// FindPR returns an open pull request from head to base, if there is one (makes opening idempotent).
func (c *Client) FindPR(ctx context.Context, r Remote, head, base string) (*PR, error) {
	if c.Kind == GitLab {
		var mrs []struct {
			IID    int    `json:"iid"`
			WebURL string `json:"web_url"`
			State  string `json:"state"`
			Title  string `json:"title"`
			Draft  bool   `json:"draft"`
		}
		q := url.Values{"source_branch": {head}, "target_branch": {base}, "state": {"opened"}}
		if err := c.do(ctx, http.MethodGet, "/projects/"+gitlabID(r.Path)+"/merge_requests?"+q.Encode(), nil, &mrs); err != nil {
			return nil, err
		}
		if len(mrs) == 0 {
			return nil, nil
		}
		m := mrs[0]
		return &PR{Number: m.IID, URL: m.WebURL, State: gitlabState(m.State), Draft: m.Draft, Title: m.Title}, nil
	}
	owner, _, _ := strings.Cut(r.Path, "/")
	q := url.Values{"head": {owner + ":" + head}, "base": {base}, "state": {"open"}}
	var prs []ghPR
	if err := c.do(ctx, http.MethodGet, "/repos/"+r.Path+"/pulls?"+q.Encode(), nil, &prs); err != nil {
		return nil, err
	}
	if len(prs) == 0 {
		return nil, nil
	}
	p := prs[0].toPR()
	return &p, nil
}

// GetPR reads a pull request's state.
func (c *Client) GetPR(ctx context.Context, r Remote, number int) (PR, error) {
	if c.Kind == GitLab {
		var m struct {
			IID    int    `json:"iid"`
			WebURL string `json:"web_url"`
			State  string `json:"state"`
			Title  string `json:"title"`
			Draft  bool   `json:"draft"`
		}
		if err := c.do(ctx, http.MethodGet, fmt.Sprintf("/projects/%s/merge_requests/%d", gitlabID(r.Path), number), nil, &m); err != nil {
			return PR{}, err
		}
		return PR{Number: m.IID, URL: m.WebURL, State: gitlabState(m.State), Draft: m.Draft, Title: m.Title}, nil
	}
	var pr ghPR
	if err := c.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/pulls/%d", r.Path, number), nil, &pr); err != nil {
		return PR{}, err
	}
	return pr.toPR(), nil
}

type ghPR struct {
	Number  int     `json:"number"`
	HTMLURL string  `json:"html_url"`
	State   string  `json:"state"`
	Draft   bool    `json:"draft"`
	Title   string  `json:"title"`
	Merged  *string `json:"merged_at"`
}

func (p ghPR) toPR() PR {
	st := p.State
	if p.Merged != nil {
		st = "merged"
	}
	return PR{Number: p.Number, URL: p.HTMLURL, State: st, Draft: p.Draft, Title: p.Title}
}

func gitlabState(s string) string {
	switch s {
	case "opened":
		return "open"
	case "merged":
		return "merged"
	default:
		return "closed"
	}
}

// CompareURL links to the branch's changes against base.
func CompareURL(kind string, r Remote, base, head string) string {
	if kind == GitLab {
		return r.Web() + "/-/compare/" + url.PathEscape(base) + "..." + url.PathEscape(head)
	}
	return r.Web() + "/compare/" + url.PathEscape(base) + "..." + url.PathEscape(head)
}
