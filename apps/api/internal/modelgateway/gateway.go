package modelgateway

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"wise/apps/api/internal/db"
	"wise/apps/api/internal/db/dbgen"
)

// EventSink receives model.* events for a run.
type EventSink interface {
	Emit(ctx context.Context, runID uuid.UUID, typ string, payload any) error
}

// TurnRequest is one agent turn routed through the gateway.
type TurnRequest struct {
	RunID           *uuid.UUID
	AgentName       string
	Alias           string
	Fallbacks       []string
	System          string
	Messages        []Message
	Tools           []ToolSpec
	OutputSchema    json.RawMessage
	Temperature     *float64
	MaxOutputTokens int
	// Run budget from the agent's execution_limits (0 = unlimited).
	MaxRunTokens  int64
	MaxRunCostUSD float64
	// Account pins one provider account (a workflow step's choice). When set,
	// alias and fallbacks are ignored: the chosen account is used or the turn fails.
	Account string
	// Model overrides the account's default model (with Account only).
	Model string
}

// TurnResponse is the parsed model turn plus accounting.
type TurnResponse struct {
	Envelope   Envelope `json:"envelope"`
	ParseError string   `json:"parseError,omitempty"`
	RawText    string   `json:"rawText,omitempty"`
	Alias      string   `json:"alias"`
	Provider   string   `json:"provider"`
	Model      string   `json:"model"`
	Usage      Usage    `json:"usage"`
	CostUSD    float64  `json:"costUsd"`
	CacheHit   bool     `json:"cacheHit"`
	LatencyMS  int64    `json:"latencyMs"`
}

type Gateway struct {
	db     *db.DB
	cfg    Config
	policy Policy
	events EventSink

	// providers holds the models.yaml providers; accounts the stored ones.
	mu          sync.RWMutex
	providers   map[string]Provider
	custom      map[string]Provider // added with WithProvider (tests, adapters); survive Reconfigure
	accounts    map[string]accountEntry
	saveSecret  SecretSaver
	runnerURL   string
	runnerToken string

	log   *slog.Logger
	now   func() time.Time
	sleep func(context.Context, time.Duration) error

	// cooldown skips a provider briefly after it failed for good (e.g. not
	// logged in), so every turn of every run does not pay for the same error.
	cooldownMu  sync.Mutex
	cooldown    map[string]time.Time
	cooldownErr map[string]string
}

// ProviderCooldown is how long a provider is skipped after a provider-wide failure.
const ProviderCooldown = 60 * time.Second

// New builds a gateway with the providers declared in cfg.
func New(d *db.DB, cfg Config, pol Policy, events EventSink, log *slog.Logger) *Gateway {
	g := &Gateway{db: d, cfg: cfg, policy: pol, providers: map[string]Provider{}, events: events, log: log,
		now: time.Now, sleep: sleepCtx, cooldown: map[string]time.Time{}, cooldownErr: map[string]string{}}
	for name, pc := range cfg.Providers {
		switch pc.Type {
		case "openai-compatible":
			g.providers[name] = NewOpenAICompat(name, pc)
		case "claude-code":
			g.providers[name] = NewClaudeCode(name, pc)
		case "codex-cli":
			g.providers[name] = NewCodexCLI(name, pc)
		}
	}
	g.accounts = map[string]accountEntry{}
	g.custom = map[string]Provider{}
	return g
}

// WithProvider replaces or adds a provider (tests, custom adapters).
func (g *Gateway) WithProvider(p Provider) *Gateway {
	g.mu.Lock()
	g.custom[p.Name()] = p
	g.mu.Unlock()
	return g
}

// Has reports whether name is a provider that survives any Reconfigure: an
// adapter added in code or a stored account (built-in providers are not, since
// a new configuration may remove them).
func (g *Gateway) Has(name string) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if _, ok := g.custom[name]; ok {
		return true
	}
	_, ok := g.accounts[name]
	return ok
}

// AccountSpec is a stored account the gateway can route to.
type AccountSpec struct {
	ID           uuid.UUID
	Name         string
	Kind         string // claude_code | codex_cli | openai_compatible
	BaseURL      string
	DefaultModel string
	Cloud        bool
	Secret       string
}

// SecretSaver persists a credential a provider refreshed (Codex OAuth tokens).
type SecretSaver func(ctx context.Context, accountID uuid.UUID, secret string) error

type accountEntry struct {
	spec AccountSpec
	prov Provider
}

// WithSecretSaver sets where refreshed account credentials are stored.
func (g *Gateway) WithSecretSaver(fn SecretSaver) *Gateway {
	g.saveSecret = fn
	return g
}

// UseCLIRunner routes every CLI provider (Claude Code, Codex) through the
// secret-free wise-cli-runner container instead of executing it in-process.
func (g *Gateway) UseCLIRunner(url, token string) *Gateway {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.runnerURL, g.runnerToken = url, token
	for name, pc := range g.cfg.Providers {
		if pc.Type == "claude-code" || pc.Type == "codex-cli" {
			if pc.Secret == "" && pc.Type == "claude-code" {
				pc.Secret = os.Getenv("CLAUDE_CODE_OAUTH_TOKEN")
			}
			g.providers[name] = NewRemoteCLI(name, pc.Type, url, token, pc)
		}
	}
	return g
}

// SetAccounts replaces the stored-account providers (called on start and after
// every account change). Accounts whose kind is not a model provider are ignored.
func (g *Gateway) SetAccounts(specs []AccountSpec) {
	g.mu.Lock()
	defer g.mu.Unlock()
	next := map[string]accountEntry{}
	for _, sp := range specs {
		if prov := g.buildAccountProvider(sp); prov != nil {
			next[sp.Name] = accountEntry{spec: sp, prov: prov}
		}
	}
	g.accounts = next
}

// buildAccountProvider must be called with g.mu held.
func (g *Gateway) buildAccountProvider(sp AccountSpec) Provider {
	pc := ProviderConfig{BaseURL: sp.BaseURL, Cloud: sp.Cloud, Secret: sp.Secret, Timeout: 10 * time.Minute}
	switch sp.Kind {
	case "openai_compatible":
		pc.Type = "openai-compatible"
		return NewOpenAICompat(sp.Name, pc)
	case "claude_code", "codex_cli":
		pc.Type = strings.ReplaceAll(sp.Kind, "_", "-")
		pc.Cloud = true
		if g.runnerURL != "" {
			return NewRemoteCLI(sp.Name, pc.Type, g.runnerURL, g.runnerToken, pc)
		}
		if sp.Kind == "claude_code" {
			pc.Binary = "claude"
			return NewClaudeCode(sp.Name, pc)
		}
		return NewCodexCLI(sp.Name, pc)
	}
	return nil
}

func (g *Gateway) provider(name string) (Provider, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if p, ok := g.custom[name]; ok {
		return p, true
	}
	if p, ok := g.providers[name]; ok {
		return p, true
	}
	a, ok := g.accounts[name]
	return a.prov, ok
}

func (g *Gateway) persistSecret(ctx context.Context, provider, secret string) {
	if secret == "" || g.saveSecret == nil {
		return
	}
	g.mu.RLock()
	a, ok := g.accounts[provider]
	g.mu.RUnlock()
	if !ok || a.spec.Secret == secret {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := g.saveSecret(ctx, a.spec.ID, secret); err != nil {
		g.log.WarnContext(ctx, "persist refreshed account credential", "account", provider, "err", err)
		return
	}
	a.spec.Secret = secret
	g.reloadAccount(a.spec)
}

// reloadAccount swaps in a refreshed credential for one account, unless the
// account was deleted, disabled or replaced meanwhile.
func (g *Gateway) reloadAccount(spec AccountSpec) {
	g.mu.Lock()
	defer g.mu.Unlock()
	cur, ok := g.accounts[spec.Name]
	if !ok || cur.spec.ID != spec.ID {
		return
	}
	cur.spec.Secret = spec.Secret
	if prov := g.buildAccountProvider(cur.spec); prov != nil {
		g.accounts[spec.Name] = accountEntry{spec: cur.spec, prov: prov}
	}
}

type candidate struct {
	alias string
	t     Target
}

// candidates lists the targets to try: the pinned account, or alias + fallbacks.
func (g *Gateway) candidates(req TurnRequest) ([]candidate, error) {
	if req.Account != "" {
		g.mu.RLock()
		a, ok := g.accounts[req.Account]
		_, builtin := g.providers[req.Account]
		if _, ok := g.custom[req.Account]; ok {
			builtin = true
		}
		g.mu.RUnlock()
		model := req.Model
		switch {
		case ok:
			if model == "" {
				model = a.spec.DefaultModel
			}
		case builtin:
		default:
			return nil, fmt.Errorf("%w: account %q is not available (deleted, disabled or not a model account)", ErrNoTarget, req.Account)
		}
		return []candidate{{alias: "account:" + req.Account, t: Target{Provider: req.Account, Model: model}}}, nil
	}
	var out []candidate
	for _, alias := range append([]string{req.Alias}, req.Fallbacks...) {
		acfg, ok := g.curCfg().Aliases[alias]
		if !ok {
			continue
		}
		for _, t := range acfg.Targets {
			out = append(out, candidate{alias: alias, t: t})
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: %q", ErrUnknownAlias, req.Alias)
	}
	return out, nil
}

// Complete routes one turn: alias → fallbacks → targets, honouring policy and budgets.
func (g *Gateway) Complete(ctx context.Context, req TurnRequest) (TurnResponse, error) {
	schema, err := EnvelopeSchema(req.Tools, req.OutputSchema)
	if err != nil {
		return TurnResponse{}, err
	}
	system := req.System + ProtocolInstructions(req.Tools)

	costBlocked, remainingCost, err := g.budgetState(ctx, req)
	if err != nil {
		return TurnResponse{}, err
	}

	cands, err := g.candidates(req)
	if err != nil {
		return TurnResponse{}, err
	}
	var lastErr error
	tried, blockedByBudget := 0, 0
	for _, c := range cands {
		alias, t := c.alias, c.t
		{
			prov, ok := g.provider(t.Provider)
			if !ok {
				continue
			}
			if prov.Cloud() && !g.curPolicy().cloudAllowed(req.AgentName) {
				g.reject(ctx, req, alias, t, "cloud models are not allowed for this agent (configs/policies/models.yaml)")
				continue
			}
			if prov.Cloud() && costBlocked {
				blockedByBudget++
				g.reject(ctx, req, alias, t, "cost budget exhausted")
				continue
			}
			if until, cooling := g.coolingDown(prov.Name()); cooling {
				lastErr = fmt.Errorf("%s skipped until %s after a recent failure", prov.Name(), until.Format(time.TimeOnly))
				g.emit(ctx, req, "model.error", map[string]any{
					"alias": alias, "provider": t.Provider, "model": t.Model, "rejected": true,
					"error": fmt.Sprintf("skipped: failed recently, retrying after %s", until.Format(time.TimeOnly)),
				})
				continue
			}
			tried++
			preq := ProviderRequest{
				Model: t.Model, System: system, Messages: req.Messages, Schema: schema,
				Temperature: req.Temperature, MaxOutputTokens: req.MaxOutputTokens,
			}
			if prov.Cloud() {
				preq.MaxCostUSD = remainingCost
			}
			resp, err := g.callTarget(ctx, req, alias, t, prov, preq)
			if err == nil {
				return resp, nil
			}
			lastErr = err
			if ctx.Err() == nil && providerWide(err) {
				g.coolDown(prov.Name(), err)
			}
			if ctx.Err() != nil {
				return TurnResponse{}, ctx.Err()
			}
		}
	}
	if tried == 0 && blockedByBudget > 0 {
		return TurnResponse{}, ErrBudgetExceeded
	}
	if lastErr == nil {
		lastErr = errors.New("no target is allowed for this agent")
	}
	return TurnResponse{}, fmt.Errorf("%w: %v", ErrNoTarget, lastErr)
}

func (g *Gateway) callTarget(ctx context.Context, req TurnRequest, alias string, t Target, prov Provider, preq ProviderRequest) (TurnResponse, error) {
	cacheable := g.curCfg().Cache.Enabled && req.Temperature != nil && *req.Temperature == 0
	var key []byte
	if cacheable {
		key = cacheKey(prov.Name(), preq)
		if raw, err := g.db.GetModelCache(ctx, key); err == nil {
			var cached TurnResponse
			if json.Unmarshal(raw, &cached) == nil {
				cached.CacheHit, cached.Usage, cached.CostUSD, cached.LatencyMS = true, Usage{}, 0, 0
				g.record(ctx, req, alias, prov.Name(), cached.Model, "ok", "", Usage{}, 0, 0, true)
				g.emit(ctx, req, "model.response", responsePayload(cached))
				return cached, nil
			}
		}
	}

	var lastErr error
	for attempt := 0; attempt <= g.curPolicy().MaxRetries; attempt++ {
		g.emit(ctx, req, "model.request", map[string]any{
			"alias": alias, "provider": prov.Name(), "model": t.Model, "attempt": attempt + 1,
			"messages": len(req.Messages), "cloud": prov.Cloud(),
		})
		start := g.now()
		pr, err := prov.Complete(ctx, preq)
		latency := g.now().Sub(start).Milliseconds()
		// A rotated credential must be stored even if this run is being cancelled.
		g.persistSecret(context.WithoutCancel(ctx), prov.Name(), pr.NewSecret)
		if pr.NewSecret != "" {
			// Later attempts must use the refreshed credential.
			if p, ok := g.provider(t.Provider); ok {
				prov = p
			}
		}
		if err != nil {
			lastErr = err
			g.record(ctx, req, alias, prov.Name(), t.Model, "error", err.Error(), Usage{}, 0, latency, false)
			g.emit(ctx, req, "model.error", map[string]any{
				"alias": alias, "provider": prov.Name(), "model": t.Model, "attempt": attempt + 1,
				"error": err.Error(), "transient": transient(err),
			})
			if !transient(err) || attempt == g.curPolicy().MaxRetries {
				break
			}
			if serr := g.sleep(ctx, time.Duration(1<<attempt)*time.Second); serr != nil {
				return TurnResponse{}, serr
			}
			continue
		}

		model := pr.Model
		if model == "" {
			model = t.Model
		}
		g.record(ctx, req, alias, prov.Name(), model, "ok", "", pr.Usage, pr.CostUSD, latency, false)
		if req.RunID != nil {
			if err := g.db.AddRunUsage(ctx, dbgen.AddRunUsageParams{
				ID: *req.RunID, InputTokens: pr.Usage.InputTokens, OutputTokens: pr.Usage.OutputTokens, CostUsd: pr.CostUSD,
			}); err != nil {
				g.log.WarnContext(ctx, "add run usage", "err", err)
			}
		}
		out := TurnResponse{
			RawText: pr.Text, Alias: alias, Provider: prov.Name(), Model: model,
			Usage: pr.Usage, CostUSD: pr.CostUSD, LatencyMS: latency,
		}
		env, perr := ParseEnvelope(pr.JSON, pr.Text, req.Tools)
		if perr != nil {
			out.Envelope = Envelope{Action: ActionInvalid}
			out.ParseError = perr.Error()
		} else {
			out.Envelope = env
			if cacheable {
				if b, err := json.Marshal(out); err == nil {
					ttl := time.Duration(max(g.curCfg().Cache.TTLH, 1)) * time.Hour
					_ = g.db.PutModelCache(ctx, dbgen.PutModelCacheParams{Key: key, Response: b, ExpiresAt: g.now().Add(ttl)})
				}
			}
		}
		g.emit(ctx, req, "model.response", responsePayload(out))
		return out, nil
	}
	return TurnResponse{}, lastErr
}

// budgetState returns whether cost-incurring (cloud) targets are blocked and the remaining run budget.
func (g *Gateway) budgetState(ctx context.Context, req TurnRequest) (bool, float64, error) {
	blocked := false
	remaining := 0.0
	if req.RunID != nil {
		run, err := g.db.GetAgentRun(ctx, *req.RunID)
		if err != nil {
			return false, 0, fmt.Errorf("load run for budget: %w", err)
		}
		if req.MaxRunTokens > 0 && run.TotalInputTokens+run.TotalOutputTokens >= req.MaxRunTokens {
			return false, 0, fmt.Errorf("%w: run used %d of %d tokens", ErrBudgetExceeded,
				run.TotalInputTokens+run.TotalOutputTokens, req.MaxRunTokens)
		}
		if req.MaxRunCostUSD > 0 {
			remaining = req.MaxRunCostUSD - run.TotalCostUsd
			if remaining <= 0 {
				blocked = true
			}
		}
	}
	if limit, ok := g.curPolicy().dailyBudget(req.AgentName); ok {
		y, m, d := g.now().UTC().Date()
		spent, err := g.db.AgentCostSince(ctx, dbgen.AgentCostSinceParams{
			AgentName: req.AgentName, CreatedAt: time.Date(y, m, d, 0, 0, 0, 0, time.UTC),
		})
		if err != nil {
			return false, 0, fmt.Errorf("load daily spend: %w", err)
		}
		if spent >= limit {
			blocked = true
		} else if left := limit - spent; remaining == 0 || left < remaining {
			remaining = left
		}
	}
	return blocked, remaining, nil
}

func (g *Gateway) coolingDown(provider string) (time.Time, bool) {
	g.cooldownMu.Lock()
	defer g.cooldownMu.Unlock()
	until, ok := g.cooldown[provider]
	if ok && g.now().Before(until) {
		return until, true
	}
	delete(g.cooldown, provider)
	return time.Time{}, false
}

func (g *Gateway) coolDown(provider string, err error) {
	g.cooldownMu.Lock()
	g.cooldown[provider] = g.now().Add(ProviderCooldown)
	g.cooldownErr[provider] = err.Error()
	g.cooldownMu.Unlock()
}

// recentFailure returns the error that put a provider in cooldown, if still active.
func (g *Gateway) recentFailure(provider string) string {
	if _, cooling := g.coolingDown(provider); !cooling {
		return ""
	}
	g.cooldownMu.Lock()
	defer g.cooldownMu.Unlock()
	return g.cooldownErr[provider]
}

func (g *Gateway) reject(ctx context.Context, req TurnRequest, alias string, t Target, reason string) {
	g.record(ctx, req, alias, t.Provider, t.Model, "rejected", reason, Usage{}, 0, 0, false)
	g.emit(ctx, req, "model.error", map[string]any{
		"alias": alias, "provider": t.Provider, "model": t.Model, "error": reason, "rejected": true,
	})
}

func (g *Gateway) record(ctx context.Context, req TurnRequest, alias, provider, model, status, errMsg string, u Usage, cost float64, latency int64, cacheHit bool) {
	id, _ := uuid.NewV7()
	var e *string
	if errMsg != "" {
		e = &errMsg
	}
	if err := g.db.InsertModelCall(ctx, dbgen.InsertModelCallParams{
		ID: id, RunID: req.RunID, AgentName: req.AgentName, Alias: alias, Provider: provider, Model: model,
		Status: status, Error: e, InputTokens: int32(u.InputTokens), OutputTokens: int32(u.OutputTokens),
		CostUsd: cost, LatencyMs: int32(latency), CacheHit: cacheHit,
	}); err != nil {
		g.log.WarnContext(ctx, "record model call", "err", err)
	}
}

func (g *Gateway) emit(ctx context.Context, req TurnRequest, typ string, payload any) {
	if req.RunID == nil || g.events == nil {
		return
	}
	if err := g.events.Emit(ctx, *req.RunID, typ, payload); err != nil {
		g.log.WarnContext(ctx, "emit model event", "type", typ, "err", err)
	}
}

func responsePayload(r TurnResponse) map[string]any {
	p := map[string]any{
		"alias": r.Alias, "provider": r.Provider, "model": r.Model, "action": r.Envelope.Action,
		"thought": r.Envelope.Thought, "usage": r.Usage, "costUsd": r.CostUSD, "latencyMs": r.LatencyMS,
		"cacheHit": r.CacheHit,
	}
	if r.ParseError != "" {
		p["parseError"] = r.ParseError
		p["rawText"] = truncate(r.RawText, 2000)
	}
	if len(r.Envelope.ToolCalls) > 0 {
		names := make([]string, len(r.Envelope.ToolCalls))
		for i, c := range r.Envelope.ToolCalls {
			names[i] = c.Tool
		}
		p["toolCalls"] = names
	}
	return p
}

func cacheKey(provider string, req ProviderRequest) []byte {
	b, _ := json.Marshal(struct {
		P string
		R ProviderRequest
	}{provider, req})
	sum := sha256.Sum256(b)
	return sum[:]
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// ---- introspection for the Models page ----

type AliasInfo struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Targets     []Target `json:"targets"`
}

type ProviderStatus struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Cloud   bool   `json:"cloud"`
	Healthy bool   `json:"healthy"`
	Error   string `json:"error,omitempty"`
}

func (g *Gateway) Aliases() []AliasInfo {
	out := make([]AliasInfo, 0, len(g.curCfg().Aliases))
	for name, a := range g.curCfg().Aliases {
		out = append(out, AliasInfo{Name: name, Description: a.Description, Targets: a.Targets})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (g *Gateway) Health(ctx context.Context) []ProviderStatus {
	g.mu.RLock()
	type item struct {
		name, typ string
		p         Provider
	}
	items := make([]item, 0, len(g.providers)+len(g.accounts))
	for name, p := range g.providers {
		items = append(items, item{name, g.cfg.Providers[name].Type, p})
	}
	for name, p := range g.custom {
		items = append(items, item{name, "custom", p})
	}
	for name, a := range g.accounts {
		items = append(items, item{name, "account:" + a.spec.Kind, a.prov})
	}
	g.mu.RUnlock()
	out := make([]ProviderStatus, 0, len(items))
	for _, it := range items {
		name, p := it.name, it.p
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := p.Health(ctx)
		cancel()
		st := ProviderStatus{Name: name, Type: it.typ, Cloud: p.Cloud(), Healthy: err == nil}
		if err != nil {
			st.Error = err.Error()
		} else if recent := g.recentFailure(name); recent != "" {
			// Health checks are shallow (e.g. CLI installed); a recent real failure is more telling.
			st.Healthy, st.Error = false, "last call failed: "+recent
		}
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// CloudAllowed reports the policy for an agent.
func (g *Gateway) CloudAllowed(agent string) bool { return g.curPolicy().cloudAllowed(agent) }

// HealthOf checks one provider or account by name.
func (g *Gateway) HealthOf(ctx context.Context, name string) error {
	p, ok := g.provider(name)
	if !ok {
		return fmt.Errorf("unknown provider or account %q", name)
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return p.Health(ctx)
}

// ProviderNames lists the built-in (models.yaml) provider names, which stored
// accounts may not reuse.
func (g *Gateway) ProviderNames() []string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := make([]string, 0, len(g.cfg.Providers)+len(g.custom))
	for name := range g.cfg.Providers {
		out = append(out, name)
	}
	for name := range g.custom {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func (g *Gateway) curCfg() Config {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.cfg
}

func (g *Gateway) curPolicy() Policy {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.policy
}

// Reconfigure swaps the provider/alias configuration and the policy live
// (settings edited in the panel). Built-in providers are rebuilt; stored
// accounts, cooldowns and providers added with WithProvider are kept.
func (g *Gateway) Reconfigure(cfg Config, pol Policy) {
	g.mu.Lock()
	defer g.mu.Unlock()
	next := map[string]Provider{}
	for name, pc := range cfg.Providers {
		switch pc.Type {
		case "openai-compatible":
			next[name] = NewOpenAICompat(name, pc)
		case "claude-code":
			if g.runnerURL != "" {
				if pc.Secret == "" {
					pc.Secret = os.Getenv("CLAUDE_CODE_OAUTH_TOKEN")
				}
				next[name] = NewRemoteCLI(name, pc.Type, g.runnerURL, g.runnerToken, pc)
			} else {
				next[name] = NewClaudeCode(name, pc)
			}
		}
	}
	g.cfg, g.policy, g.providers = cfg, pol, next
}
