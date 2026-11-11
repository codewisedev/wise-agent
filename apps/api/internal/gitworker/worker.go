// Package gitworker is wise-git-worker: the only component that pushes to a
// git host or opens pull requests (ADR 0010). It receives only approval or
// push ids; the API grants each operation after checking the approval and
// hands over the token for that one operation, which the worker keeps in
// memory. It pushes the approved commit from the workspace's executor-private
// trusted repository, create-only, to a wise/… branch, and never merges.
package gitworker

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"wise/apps/api/internal/githost"
	"wise/apps/api/internal/release"
)

// Config configures the worker.
type Config struct {
	APIURL        string // the API's base URL (…/api/v1)
	APIToken      string // WISE_GIT_WORKER_TOKEN: grants on the API
	WorkspacesDir string // the workspaces volume, mounted read-only
	Git           string // git binary
	// ExtraGitConfig adds "key=value" git settings (tests: http.sslCAInfo).
	ExtraGitConfig []string
	// HostAPIBase overrides the git host API root (tests).
	HostAPIBase string
	HTTP        *http.Client
}

// Worker pushes and opens pull requests.
type Worker struct {
	cfg Config
	log *slog.Logger
}

// New returns a Worker.
func New(cfg Config, log *slog.Logger) *Worker {
	if cfg.Git == "" {
		cfg.Git = "git"
	}
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{Timeout: 60 * time.Second}
	}
	return &Worker{cfg: cfg, log: log}
}

// apiCall posts to an internal git route on the API.
func (w *Worker) apiCall(ctx context.Context, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(w.cfg.APIURL, "/")+"/internal/git"+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+w.cfg.APIToken)
	req.Header.Set("Content-Type", "application/json")
	res, err := w.cfg.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("API unreachable: %w", err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 500 {
		return fmt.Errorf("API error (HTTP %d)", res.StatusCode) // transient: retried
	}
	if res.StatusCode >= 300 {
		var p struct {
			Detail string `json:"detail"`
			Title  string `json:"title"`
		}
		_ = json.Unmarshal(raw, &p)
		msg := p.Detail
		if msg == "" {
			msg = p.Title
		}
		return &RefusedError{Status: res.StatusCode, Msg: msg}
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// RefusedError is a refusal by the API (approval missing, already pushed…).
type RefusedError struct {
	Status int
	Msg    string
}

func (e *RefusedError) Error() string { return e.Msg }

// report records a result, retrying a few times.
func (w *Worker) report(ctx context.Context, path string, body any) error {
	ctx = context.WithoutCancel(ctx)
	var err error
	for i := 0; i < 4; i++ {
		if err = w.apiCall(ctx, path, body, nil); err == nil {
			return nil
		}
		var re *RefusedError
		if errors.As(err, &re) {
			return err
		}
		time.Sleep(time.Duration(1<<i) * 500 * time.Millisecond)
	}
	w.log.Warn("report result", "path", path, "err", err)
	return err
}

// PushResult is the outcome of a push.
type PushResult struct {
	PushID  uuid.UUID `json:"pushId"`
	Branch  string    `json:"branch"`
	SHA     string    `json:"sha"`
	Remote  string    `json:"remote"`
	Existed bool      `json:"existed"` // the branch was already at this commit
}

// Push performs the push an approval grants.
func (w *Worker) Push(ctx context.Context, approvalID uuid.UUID) (PushResult, error) {
	var g release.Grant
	if err := w.apiCall(ctx, "/push-grants/"+approvalID.String(), nil, &g); err != nil {
		return PushResult{}, err
	}
	res := PushResult{PushID: g.PushID, Branch: g.Branch, SHA: g.SHA, Remote: g.Remote}
	existed, err := w.push(ctx, g)
	res.Existed = existed
	report := map[string]any{"ok": err == nil, "existed": existed}
	if err != nil {
		report["error"] = err.Error()
	}
	if rerr := w.report(ctx, "/pushes/"+g.PushID.String()+"/result", report); rerr != nil {
		// Not recorded: say so. A retry is granted again and finds the branch already pushed.
		return res, fmt.Errorf("the push result could not be recorded: %w", rerr)
	}
	return res, err
}

// ErrBranchExists means the remote branch holds other work: it is never moved.
var ErrBranchExists = errors.New("the branch already exists on the remote with other commits; Wise never moves an existing branch")

func basicUser(kind string) string {
	if kind == githost.GitLab {
		return "oauth2"
	}
	return "x-access-token"
}

// git runs git with an isolated configuration; the token travels only in the
// environment (GIT_CONFIG_*), never in argv or on disk.
func (w *Worker) git(ctx context.Context, home string, cfg map[string]string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, w.cfg.Git, args...)
	env := []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=/bin/false", "SSH_ASKPASS=/bin/false", "LC_ALL=C"}
	all := map[string]string{"core.hooksPath": "/dev/null", "protocol.allow": "never", "protocol.https.allow": "always", "protocol.file.allow": "always",
		"credential.helper": "", "safe.directory": "*",
		// Never follow a redirect: http.extraHeader (the token) would go along.
		"http.followRedirects": "false"}
	for _, kv := range w.cfg.ExtraGitConfig {
		if k, v, ok := strings.Cut(kv, "="); ok {
			all[k] = v
		}
	}
	for k, v := range cfg {
		all[k] = v
	}
	i := 0
	for k, v := range all {
		env = append(env, "GIT_CONFIG_KEY_"+strconv.Itoa(i)+"="+k, "GIT_CONFIG_VALUE_"+strconv.Itoa(i)+"="+v)
		i++
	}
	cmd.Env = append(env, "GIT_CONFIG_COUNT="+strconv.Itoa(i))
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.String(), err
}

func scrub(s, token string) string {
	if token != "" {
		s = strings.ReplaceAll(s, token, "[token]")
	}
	if len(s) > 800 {
		s = s[len(s)-800:]
	}
	return strings.TrimSpace(s)
}

// push pushes the approved commit, create-only.
func (w *Worker) push(ctx context.Context, g release.Grant) (bool, error) {
	// The API checked these; the worker checks them again (defence in depth).
	if err := release.CheckTarget(release.Request{SHA: g.SHA, Branch: g.Branch, Remote: g.Remote, BaseBranch: g.BaseBranch}, g.Kind, g.BaseURL); err != nil {
		return false, err
	}
	timeout := time.Duration(max(g.TimeoutS, 10)) * time.Second
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	trusted := filepath.Join(w.cfg.WorkspacesDir, g.WorkspaceID.String(), "trusted.git")
	if st, err := os.Stat(trusted); err != nil || !st.IsDir() {
		return false, errors.New("the workspace's verified repository is gone")
	}
	tmp, err := os.MkdirTemp("", "wise-push-")
	if err != nil {
		return false, err
	}
	defer os.RemoveAll(tmp)
	repo := filepath.Join(tmp, "repo.git")
	if out, err := w.git(ctx, tmp, nil, "init", "-q", "--bare", repo); err != nil {
		return false, fmt.Errorf("init: %s", out)
	}
	if out, err := w.git(ctx, tmp, map[string]string{"transfer.fsckObjects": "true", "fetch.fsckObjects": "true"},
		"-C", repo, "fetch", "-q", "--no-tags", "--no-write-fetch-head", trusted, g.SHA); err != nil {
		return false, fmt.Errorf("read the approved commit: %s", scrub(out, ""))
	}
	if out, err := w.git(ctx, tmp, nil, "-C", repo, "cat-file", "-e", g.SHA+"^{commit}"); err != nil {
		return false, fmt.Errorf("the approved commit is missing: %s", out)
	}
	auth := map[string]string{"http.extraHeader": "Authorization: Basic " +
		base64.StdEncoding.EncodeToString([]byte(basicUser(g.Kind)+":"+g.Token))}
	ref := "refs/heads/" + g.Branch
	// Create-only: the lease expects the branch not to exist.
	out, err := w.git(ctx, tmp, auth, "-C", repo, "push", "--porcelain", "--no-verify",
		"--force-with-lease="+ref+":", g.Remote, g.SHA+":"+ref)
	if err == nil {
		// Porcelain: "=" marks a ref that was already up to date (already pushed).
		for _, line := range strings.Split(out, "\n") {
			if strings.HasPrefix(line, "=\t") {
				return true, nil
			}
		}
		return false, nil
	}
	pushErr := scrub(out, g.Token)
	// Already there at the same commit (e.g. a retry after a lost report) counts as pushed.
	if ls, lerr := w.git(ctx, tmp, auth, "-C", repo, "ls-remote", g.Remote, ref); lerr == nil {
		if f := strings.Fields(ls); len(f) >= 1 {
			if f[0] == g.SHA {
				return true, nil
			}
			return false, ErrBranchExists
		}
	}
	low := strings.ToLower(pushErr)
	switch {
	case strings.Contains(low, "authentication failed") || strings.Contains(low, "403") || strings.Contains(low, "401") ||
		strings.Contains(low, "permission") || strings.Contains(low, "denied"):
		return false, fmt.Errorf("the git host refused the token (it needs write access to the repository's contents): %s", pushErr)
	case ctx.Err() != nil:
		return false, fmt.Errorf("the push timed out after %s", timeout)
	}
	return false, fmt.Errorf("push failed: %s", pushErr)
}

// OpenPR opens (or finds) the pull request a successful push is granted.
func (w *Worker) OpenPR(ctx context.Context, pushID uuid.UUID, approvalID *uuid.UUID) (githost.PR, error) {
	path := "/pr-grants/" + pushID.String()
	if approvalID != nil {
		path += "?approvalId=" + approvalID.String()
	}
	var g release.Grant
	if err := w.apiCall(ctx, path, nil, &g); err != nil {
		return githost.PR{}, err
	}
	pr, err := w.openPR(ctx, g)
	report := map[string]any{"ok": err == nil, "pr": pr}
	if err != nil {
		report["error"] = scrub(err.Error(), g.Token)
	}
	if rerr := w.report(ctx, "/pull-requests/"+g.PRID.String()+"/result", report); rerr != nil {
		// A retry is granted the same record and finds the open pull request first.
		return pr, fmt.Errorf("the pull request result could not be recorded: %w", rerr)
	}
	return pr, err
}

func (w *Worker) openPR(ctx context.Context, g release.Grant) (githost.PR, error) {
	rm, err := githost.ParseRemote(g.Remote)
	if err != nil {
		return githost.PR{}, err
	}
	if !githost.Matches(g.Kind, g.BaseURL, rm) {
		return githost.PR{}, errors.New("the account does not serve the remote")
	}
	c := &githost.Client{Kind: g.Kind, BaseURL: g.BaseURL, Token: g.Token, HTTP: w.cfg.HTTP, APIBase: w.cfg.HostAPIBase}
	if existing, err := c.FindPR(ctx, rm, g.Branch, g.BaseBranch); err != nil {
		return githost.PR{}, err
	} else if existing != nil {
		return *existing, nil
	}
	return c.CreatePR(ctx, rm, githost.PRInput{Title: g.Title, Body: g.Body, Head: g.Branch, Base: g.BaseBranch, Draft: g.Draft})
}
