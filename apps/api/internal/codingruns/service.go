// Package codingruns runs implementing agents on external coding CLIs
// (Claude Code, Codex): it checks the runtime is allowed (setting, project
// policy, cloud policy, budget), resolves the credential (Runtime Proxy or
// subscription), renders the RunSpec through the adapter, starts the runner
// in the task workspace, streams normalized events to the agent runtime and
// builds the result: the report validated against the agent's contract,
// usage recorded like native runs.
package codingruns

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"wise/apps/api/internal/accounts"
	"wise/apps/api/internal/agents"
	"wise/apps/api/internal/apperr"
	"wise/apps/api/internal/audit"
	"wise/apps/api/internal/coding"
	"wise/apps/api/internal/db"
	"wise/apps/api/internal/db/dbgen"
	"wise/apps/api/internal/executor"
	"wise/apps/api/internal/modelproxy"
	"wise/apps/api/internal/schemas"
	"wise/apps/api/internal/workspaces"
)

// Runner starts and follows runner jobs (the executor client).
type Runner interface {
	Runner(ctx context.Context, id string, req executor.RunnerRequest) (string, error)
	Job(ctx context.Context, jobID string, offset int64, wait int) (executor.JobStatus, error)
	CancelJob(ctx context.Context, jobID string) error
}

// Proxy registers proxy-mode runs (the model proxy client).
type Proxy interface {
	Register(ctx context.Context, token string, run modelproxy.Run) error
	Revoke(ctx context.Context, token string) (modelproxy.Usage, error)
}

// Policy is the part of the model gateway's policy that applies.
type Policy interface {
	CloudAllowed(agent string) bool
	DailyRemaining(ctx context.Context, agent string) (float64, bool, error)
}

// Service runs external coding sessions.
type Service struct {
	db         *db.DB
	runner     Runner // nil: the executor is not configured
	proxy      Proxy  // nil: proxy mode unavailable
	ws         *workspaces.Service
	accounts   *accounts.Service
	policy     Policy
	ContextFor func(ctx context.Context, run dbgen.AgentRun) (string, error)
	// EnvToken is the Claude Code token from .env (subscription mode, no account).
	EnvToken string
	// SaveArtifact stores the rendered instructions of each run (tasks.Service.SaveArtifact).
	SaveArtifact func(ctx context.Context, workflowRunID, taskID uuid.UUID, kind string, content json.RawMessage, agentRunID *uuid.UUID) error
	log          *slog.Logger

	mu       sync.RWMutex
	settings Settings

	sessMu   sync.Mutex
	sessions map[uuid.UUID]*session
}

type session struct {
	runtime   string
	mode      string
	jobID     string
	adapter   coding.Adapter
	token     string // proxy token
	accountID *uuid.UUID
	secret    string // the subscription credential (to detect a refresh)
	offset    int64
	partial   string
	events    []coding.Event
	model     string
	started   time.Time
}

// New returns a Service.
func New(d *db.DB, runner Runner, proxy Proxy, ws *workspaces.Service, acc *accounts.Service, policy Policy, log *slog.Logger) *Service {
	return &Service{db: d, runner: runner, proxy: proxy, ws: ws, accounts: acc, policy: policy, log: log,
		EnvToken: os.Getenv("CLAUDE_CODE_OAUTH_TOKEN"), sessions: map[uuid.UUID]*session{}}
}

// Settings returns the current coding_runtimes setting.
func (s *Service) Settings() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.settings
}

// SetSettings applies a new coding_runtimes setting.
func (s *Service) SetSettings(st Settings) {
	s.mu.Lock()
	s.settings = st
	s.mu.Unlock()
}

// ---- project policy ----

// ProjectPolicy returns a project's runtime policy.
func (s *Service) ProjectPolicy(ctx context.Context, projectID uuid.UUID) (ProjectPolicy, error) {
	p, err := s.db.GetProject(ctx, projectID)
	if db.IsNotFound(err) {
		return ProjectPolicy{}, apperr.NotFound("project not found")
	}
	if err != nil {
		return ProjectPolicy{}, err
	}
	return PolicyFrom(p.Project.CodingRuntimes), nil
}

// SetProjectPolicy changes a project's runtime policy (a security policy:
// audited with the old and new values).
func (s *Service) SetProjectPolicy(ctx context.Context, projectID uuid.UUID, in ProjectPolicy) (ProjectPolicy, error) {
	if err := in.Validate(); err != nil {
		return in, err
	}
	err := s.db.InTx(ctx, func(q *dbgen.Queries) error {
		p, err := q.GetProject(ctx, projectID)
		if db.IsNotFound(err) {
			return apperr.NotFound("project not found")
		}
		if err != nil {
			return err
		}
		old := PolicyFrom(p.Project.CodingRuntimes)
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		if err := q.SetProjectCodingRuntimes(ctx, dbgen.SetProjectCodingRuntimesParams{ID: projectID, CodingRuntimes: raw}); err != nil {
			return err
		}
		pid := projectID
		return audit.Record(ctx, q, audit.Entry{Action: "project.coding_runtimes_updated", EntityType: "project", EntityID: &pid,
			Payload: map[string]any{"from": old, "to": in}})
	})
	return in, err
}

// CheckRuntime reports why a project's tasks cannot use a runtime (nil = they can).
func (s *Service) CheckRuntime(ctx context.Context, projectID uuid.UUID, runtime string) error {
	if rs, ok := s.Settings().Runtimes[runtime]; !ok || !rs.Enabled {
		return fmt.Errorf("%s is disabled in Settings → Coding runtimes", runtime)
	}
	pol, err := s.ProjectPolicy(ctx, projectID)
	if err != nil {
		return err
	}
	if !slices.Contains(pol.Enabled, runtime) {
		return fmt.Errorf("the project has not enabled %s (Project → Coding runtimes)", runtime)
	}
	if s.Settings().Runtimes[runtime].Credential.Mode == coding.ModeSubscription && !pol.AllowSubscription {
		return fmt.Errorf("%s uses a subscription, which the project does not allow (reduced isolation)", runtime)
	}
	return nil
}

// ---- sessions ----

// Start describes a started session.
type Start struct {
	Runtime          string `json:"runtime"`
	Mode             string `json:"mode"`
	ReducedIsolation bool   `json:"reducedIsolation"`
	CLIVersion       string `json:"cliVersion"`
}

func newToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Start begins (or rejoins) the external session of a run.
func (s *Service) Start(ctx context.Context, run dbgen.AgentRun) (Start, error) {
	runtime := agents.RunRuntime(run)
	adapter, ok := coding.For(runtime)
	if !ok {
		return Start{}, apperr.BadRequest("this run uses Wise's own runtime")
	}
	s.sessMu.Lock()
	if sess, ok := s.sessions[run.ID]; ok {
		s.sessMu.Unlock()
		return Start{Runtime: sess.runtime, Mode: sess.mode, ReducedIsolation: sess.mode == coding.ModeSubscription, CLIVersion: CLIVersions[runtime]}, nil
	}
	s.sessMu.Unlock()
	if s.runner == nil {
		return Start{}, apperr.Unavailable("the executor is not configured")
	}
	rs, ok := s.Settings().Runtimes[runtime]
	if !ok || !rs.Enabled {
		return Start{}, apperr.Conflict(runtime + " is disabled in Settings → Coding runtimes")
	}
	projectID := run.ProjectID
	if projectID == nil && run.WorkflowRunID != nil {
		if wr, err := s.db.GetWorkflowRun(ctx, *run.WorkflowRunID); err == nil {
			if t, err := s.db.GetTask(ctx, wr.TaskID); err == nil {
				projectID = &t.ProjectID
			}
		}
	}
	if projectID == nil {
		return Start{}, apperr.Conflict("external coding runtimes need a project")
	}
	pol, err := s.ProjectPolicy(ctx, *projectID)
	if err != nil {
		return Start{}, err
	}
	if !slices.Contains(pol.Enabled, runtime) {
		return Start{}, apperr.Conflict("the project has not enabled " + runtime + " (Project → Coding runtimes)")
	}
	if !s.policy.CloudAllowed(run.AgentName) {
		return Start{}, apperr.Forbidden(run.AgentName + " may not use cloud models (Settings → Policy & budgets)")
	}
	maxCost := rs.Limits.MaxCostUSD
	if left, limited, err := s.policy.DailyRemaining(ctx, run.AgentName); err != nil {
		return Start{}, err
	} else if limited {
		if left <= 0 {
			return Start{}, apperr.TooManyRequests(run.AgentName + " has used its daily budget")
		}
		if maxCost == 0 || left < maxCost {
			maxCost = left
		}
	}
	w, err := s.ws.ForRun(ctx, run)
	if err != nil {
		return Start{}, err
	}
	if w == nil || (w.Status != "ready" && w.Status != "stopped") {
		return Start{}, apperr.Conflict("the run has no ready task workspace")
	}
	defRow, err := s.db.GetAgentDefinition(ctx, run.AgentDefinitionID)
	if err != nil {
		return Start{}, err
	}
	def, err := agents.ParseDefinition(defRow.Definition)
	if err != nil {
		return Start{}, err
	}
	instructions := defRow.Prompt
	if s.ContextFor != nil {
		if text, err := s.ContextFor(ctx, run); err == nil && text != "" {
			instructions += "\n\n" + text
		}
	}
	input := map[string]any{}
	_ = json.Unmarshal(run.Input, &input)

	// Credential.
	sess := &session{runtime: runtime, mode: rs.Credential.Mode, adapter: adapter, model: rs.Model, started: time.Now()}
	accountName := rs.Credential.Account
	if o := agents.ParseModelOverride(run.ModelOverride); o != nil {
		if o.Account != "" {
			accountName = o.Account
		}
		if o.Model != "" {
			sess.model = o.Model
		}
	}
	cred, err := s.credential(ctx, run, runtime, rs, pol, accountName, maxCost, sess)
	if err != nil {
		return Start{}, err
	}
	spec := coding.Spec{SpecVersion: coding.SpecVersion, RunID: run.ID.String(), Agent: run.AgentName, Runtime: runtime,
		CLIVersion: CLIVersions[runtime], Model: sess.model, Instructions: instructions, Input: input, OutputSchema: def.OutputSchema,
		ProtectedPaths: s.ws.Settings().ProtectedPaths, Tools: rs.Tools, Sandbox: rs.Sandbox,
		Limits:     coding.Limits{WallClockS: rs.Limits.WallClockS, IdleS: rs.Limits.IdleS, MaxTurns: rs.Limits.MaxTurns, MaxCostUSD: maxCost},
		Credential: cred}
	if w := def.ExecutionLimits; w.MaxTurns > 0 && w.MaxTurns < spec.Limits.MaxTurns {
		spec.Limits.MaxTurns = w.MaxTurns
	}
	if taskID, err := s.db.GetWorkflowRun(ctx, derefUUID(run.WorkflowRunID)); err == nil {
		spec.TaskID = taskID.TaskID.String()
	}
	req, err := adapter.Build(spec)
	if err != nil {
		s.revoke(sess)
		return Start{}, apperr.BadRequest(err.Error())
	}
	if s.SaveArtifact != nil && run.WorkflowRunID != nil && spec.TaskID != "" {
		art, _ := json.Marshal(map[string]any{"runtime": runtime, "cliVersion": spec.CLIVersion, "mode": cred.Mode, "model": spec.Model,
			"limits": spec.Limits, "tools": spec.Tools, "sandbox": spec.Sandbox, "protectedPaths": spec.ProtectedPaths,
			"instructions": spec.Instructions, "prompt": spec.Prompt()})
		if tid, err := uuid.Parse(spec.TaskID); err == nil {
			if err := s.SaveArtifact(ctx, *run.WorkflowRunID, tid, "runtime_instructions", art, &run.ID); err != nil {
				s.log.Warn("store runtime instructions", "run", run.ID, "err", err)
			}
		}
	}
	job, err := s.runner.Runner(ctx, w.ID.String(), req)
	if err != nil {
		s.revoke(sess)
		return Start{}, err
	}
	sess.jobID = job
	s.sessMu.Lock()
	s.sessions[run.ID] = sess
	s.sessMu.Unlock()
	return Start{Runtime: runtime, Mode: sess.mode, ReducedIsolation: sess.mode == coding.ModeSubscription, CLIVersion: CLIVersions[runtime]}, nil
}

func derefUUID(p *uuid.UUID) uuid.UUID {
	if p == nil {
		return uuid.Nil
	}
	return *p
}

// credential resolves the run's model access.
func (s *Service) credential(ctx context.Context, run dbgen.AgentRun, runtime string, rs RuntimeSettings, pol ProjectPolicy,
	accountName string, maxCost float64, sess *session) (coding.Credential, error) {
	account := func(want ...string) (dbgen.Account, string, error) {
		a, err := s.db.GetAccountByName(ctx, accountName)
		if db.IsNotFound(err) {
			return a, "", apperr.Conflict(fmt.Sprintf("account %q not found (Settings → Accounts)", accountName))
		}
		if err != nil {
			return a, "", err
		}
		if !slices.Contains(want, a.Kind) || !a.Enabled {
			return a, "", apperr.Conflict(fmt.Sprintf("account %q must be an enabled %s account", accountName, strings.Join(want, " or ")))
		}
		_, secret, _, err := s.accounts.Credential(ctx, a.ID)
		if err == nil && secret == "" {
			err = apperr.Conflict(fmt.Sprintf("account %q has no credential", accountName))
		}
		return a, secret, err
	}
	switch rs.Credential.Mode {
	case coding.ModeSubscription:
		if !pol.AllowSubscription {
			return coding.Credential{}, apperr.Conflict("the project does not allow subscription (reduced-isolation) mode for coding runtimes")
		}
		if runtime == "claude-code" && accountName == "" {
			if s.EnvToken == "" {
				return coding.Credential{}, apperr.Conflict("no Claude Code token: add a Claude Code account or set CLAUDE_CODE_OAUTH_TOKEN")
			}
			sess.secret = s.EnvToken
			return coding.Credential{Mode: coding.ModeSubscription, Secret: s.EnvToken}, nil
		}
		kind := accounts.KindClaudeCode
		if runtime == "codex" {
			kind = accounts.KindCodexCLI
			if accountName == "" {
				return coding.Credential{}, apperr.Conflict("Codex needs a Codex account (auth.json): choose it in Settings → Coding runtimes")
			}
		}
		a, secret, err := account(kind)
		if err != nil {
			return coding.Credential{}, err
		}
		sess.accountID, sess.secret = &a.ID, secret
		return coding.Credential{Mode: coding.ModeSubscription, Secret: secret}, nil
	case coding.ModeProxy:
		if s.proxy == nil {
			return coding.Credential{}, apperr.Unavailable("the Runtime Proxy is not configured (WISE_MODEL_PROXY_URL)")
		}
		kind, vendor := accounts.KindAnthropic, "anthropic"
		if runtime == "codex" {
			kind, vendor = accounts.KindOpenAI, "openai"
		}
		a, secret, err := account(kind)
		if err != nil {
			return coding.Credential{}, err
		}
		if vendor == "openai" {
			if u, err := url.Parse(a.BaseUrl); err != nil || u.Host != "api.openai.com" {
				return coding.Credential{}, apperr.Conflict("Codex proxy mode needs an OpenAI-compatible account with base URL https://api.openai.com/v1")
			}
		}
		tok := newToken()
		if err := s.proxy.Register(ctx, tok, modelproxy.Run{RunID: run.ID.String(), Vendor: vendor, APIKey: secret, BudgetUSD: maxCost,
			InputPerMTok: rs.Prices.InputPerMTok, OutputPerMTok: rs.Prices.OutputPerMTok,
			Expires: time.Now().Add(time.Duration(rs.Limits.WallClockS+600) * time.Second)}); err != nil {
			return coding.Credential{}, err
		}
		sess.token, sess.accountID = tok, &a.ID
		return coding.Credential{Mode: coding.ModeProxy, Secret: tok}, nil
	}
	return coding.Credential{}, apperr.Conflict("unknown credential mode")
}

func (s *Service) revoke(sess *session) {
	if sess.token == "" || s.proxy == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, _ = s.proxy.Revoke(ctx, sess.token)
}

// Poll is the news of a session.
type Poll struct {
	Events  []coding.Event  `json:"events"`
	Done    bool            `json:"done"`
	Outcome *coding.Outcome `json:"outcome,omitempty"`
}

// Poll returns new events, waiting up to wait seconds; when the run has
// ended it builds the outcome (validated report, recorded usage).
func (s *Service) Poll(ctx context.Context, run dbgen.AgentRun, wait int) (Poll, error) {
	s.sessMu.Lock()
	sess, ok := s.sessions[run.ID]
	s.sessMu.Unlock()
	if !ok {
		return Poll{}, apperr.NotFound("no external session for this run (the API may have restarted); the run will be retried")
	}
	st, err := s.runner.Job(ctx, sess.jobID, sess.offset, min(max(wait, 0), 25))
	if err != nil {
		return Poll{}, err
	}
	sess.offset = st.NextOffset
	text := sess.partial + st.Output
	lines := strings.Split(text, "\n")
	sess.partial = lines[len(lines)-1]
	var news []coding.Event
	for _, l := range lines[:len(lines)-1] {
		l = strings.TrimSpace(l)
		if l == "" || l[0] != '{' {
			continue
		}
		evs := sess.adapter.Parse([]byte(l))
		news = append(news, evs...)
		if len(sess.events) < 20000 {
			sess.events = append(sess.events, evs...)
		}
	}
	out := Poll{Events: news}
	if st.Status == executor.JobRunning || st.More {
		return out, nil
	}
	if rest := strings.TrimSpace(sess.partial); rest != "" && rest[0] == '{' {
		evs := sess.adapter.Parse([]byte(rest))
		out.Events = append(out.Events, evs...)
		sess.events = append(sess.events, evs...)
	}
	var res executor.RunnerResult
	raw, _ := json.Marshal(st.Result)
	_ = json.Unmarshal(raw, &res)
	o := sess.adapter.Outcome(res, sess.events)
	if st.Status != executor.JobSucceeded && o.Class == coding.Succeeded {
		o.Class, o.Message = coding.RuntimeCrash, st.Error
	} else if st.Status == executor.JobFailed && o.Message == "" {
		o.Message = st.Error
	}
	s.finish(ctx, run, sess, &o)
	out.Done, out.Outcome = true, &o
	s.sessMu.Lock()
	delete(s.sessions, run.ID)
	s.sessMu.Unlock()
	return out, nil
}

// finish validates the report, records usage and keeps a refreshed login.
func (s *Service) finish(ctx context.Context, run dbgen.AgentRun, sess *session, o *coding.Outcome) {
	provider := sess.runtime
	calls := []dbgen.InsertModelCallParams{}
	if sess.token != "" && s.proxy != nil {
		// Proxy mode: usage measured on the trusted side.
		rctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		u, err := s.proxy.Revoke(rctx, sess.token)
		cancel()
		if err == nil {
			o.Usage = coding.Usage{InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, CostUSD: u.CostUSD, Turns: o.Usage.Turns}
			for _, c := range u.Calls {
				st := "ok"
				var msg *string
				if c.Status >= 400 {
					st, msg = "error", &c.Error
				}
				calls = append(calls, dbgen.InsertModelCallParams{Model: c.Model, Status: st, Error: msg, InputTokens: int32(c.InputTokens),
					OutputTokens: int32(c.OutputTokens), CostUsd: c.CostUSD, LatencyMs: int32(c.DurationMs)})
			}
			if u.Exhausted && o.Class != coding.Succeeded {
				o.Class, o.Message = coding.BudgetExceeded, "the run reached its cost limit"
			}
		}
	} else {
		st := "ok"
		var msg *string
		if o.Class != coding.Succeeded {
			st, msg = "error", &o.Message
		}
		calls = append(calls, dbgen.InsertModelCallParams{Model: sess.model, Status: st, Error: msg, InputTokens: int32(o.Usage.InputTokens),
			OutputTokens: int32(o.Usage.OutputTokens), CostUsd: o.Usage.CostUSD, LatencyMs: int32(time.Since(sess.started).Milliseconds())})
	}
	for _, c := range calls {
		c.ID, _ = uuid.NewV7()
		c.RunID, c.AgentName, c.Alias, c.Provider = &run.ID, run.AgentName, "coding:"+sess.runtime, provider
		if c.Model == "" {
			c.Model = "default"
		}
		if err := s.db.InsertModelCall(ctx, c); err != nil {
			s.log.Warn("record external model call", "run", run.ID, "err", err)
		}
	}
	if err := s.db.AddRunUsage(ctx, dbgen.AddRunUsageParams{ID: run.ID, InputTokens: o.Usage.InputTokens, OutputTokens: o.Usage.OutputTokens,
		CostUsd: o.Usage.CostUSD}); err != nil {
		s.log.Warn("record external run usage", "run", run.ID, "err", err)
	}
	// A refreshed Codex login replaces the stored one.
	if fresh, ok := o.Files[".codex/auth.json"]; ok && sess.accountID != nil && strings.TrimSpace(fresh) != "" &&
		strings.TrimSpace(fresh) != strings.TrimSpace(sess.secret) && json.Valid([]byte(fresh)) {
		if err := s.accounts.SaveSecret(ctx, *sess.accountID, fresh); err != nil {
			s.log.Warn("save refreshed codex login", "err", err)
		}
	}
	// The report must satisfy the agent's contract.
	if o.Class == coding.Succeeded {
		defRow, err := s.db.GetAgentDefinition(ctx, run.AgentDefinitionID)
		if err == nil {
			if def, err := agents.ParseDefinition(defRow.Definition); err == nil {
				if err := schemas.Validate(def.OutputSchema, o.Output, "output"); err != nil {
					o.Class, o.Message = coding.Incomplete, "the report does not match the output contract: "+err.Error()
				}
			}
		}
	}
}

// Cancel stops a run's session.
func (s *Service) Cancel(ctx context.Context, run dbgen.AgentRun) error {
	s.sessMu.Lock()
	sess, ok := s.sessions[run.ID]
	delete(s.sessions, run.ID)
	s.sessMu.Unlock()
	if !ok {
		return nil
	}
	err := s.runner.CancelJob(ctx, sess.jobID)
	s.revoke(sess)
	return err
}
