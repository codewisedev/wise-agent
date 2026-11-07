package modelgateway_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"wise/apps/api/internal/db/dbgen"
	mg "wise/apps/api/internal/modelgateway"
	"wise/apps/api/internal/testutil"
)

type fakeProvider struct {
	name    string
	cloud   bool
	mu      sync.Mutex
	calls   int
	respond func(call int, req mg.ProviderRequest) (mg.ProviderResponse, error)
}

func (f *fakeProvider) Name() string                 { return f.name }
func (f *fakeProvider) Cloud() bool                  { return f.cloud }
func (f *fakeProvider) Health(context.Context) error { return nil }
func (f *fakeProvider) Complete(_ context.Context, req mg.ProviderRequest) (mg.ProviderResponse, error) {
	f.mu.Lock()
	f.calls++
	n := f.calls
	f.mu.Unlock()
	return f.respond(n, req)
}

func final(cost float64) func(int, mg.ProviderRequest) (mg.ProviderResponse, error) {
	return func(int, mg.ProviderRequest) (mg.ProviderResponse, error) {
		return mg.ProviderResponse{Text: `{"action":"final","final":{"ok":true}}`, Usage: mg.Usage{InputTokens: 10, OutputTokens: 5}, CostUSD: cost}, nil
	}
}

func setup(t *testing.T, policy mg.Policy, cloud, local *fakeProvider) (*mg.Gateway, *testutil.DBHandle) {
	t.Helper()
	d := testutil.NewDB(t)
	cfg := mg.Config{
		Providers: map[string]mg.ProviderConfig{"cloud": {Type: "fake", Cloud: true}, "local": {Type: "fake"}},
		Aliases: map[string]mg.AliasConfig{
			"reasoning": {Targets: []mg.Target{{Provider: "cloud", Model: "big"}}},
			"local":     {Targets: []mg.Target{{Provider: "local", Model: "small"}}},
		},
		Cache: mg.CacheConfig{Enabled: true, TTLH: 1},
	}
	if policy.MaxRetries == 0 {
		policy.MaxRetries = 2
	}
	g := mg.New(d, cfg, policy, nil, slog.New(slog.NewTextHandler(io.Discard, nil))).WithProvider(cloud).WithProvider(local)
	mg.SetSleepForTest(g, func(context.Context, time.Duration) error { return nil })
	return g, &testutil.DBHandle{DB: d}
}

func countCalls(t *testing.T, h *testutil.DBHandle, status string) int {
	t.Helper()
	var n int
	if err := h.DB.Pool.QueryRow(context.Background(), `SELECT count(*) FROM model_calls WHERE status = $1`, status).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

var allowAll = mg.Policy{CloudAllowedAgents: []string{"*"}}

func req(agent string, fallbacks ...string) mg.TurnRequest {
	return mg.TurnRequest{AgentName: agent, Alias: "reasoning", Fallbacks: fallbacks, System: "s",
		Messages: []mg.Message{{Role: "user", Content: "hi"}}, OutputSchema: json.RawMessage(`{"type":"object"}`)}
}

func TestFallbackAfterPermanentFailure(t *testing.T) {
	cloud := &fakeProvider{name: "cloud", cloud: true, respond: func(int, mg.ProviderRequest) (mg.ProviderResponse, error) {
		return mg.ProviderResponse{}, &mg.ProviderError{ProviderWide: true, Err: errors.New("auth failed")}
	}}
	local := &fakeProvider{name: "local", respond: final(0)}
	g, h := setup(t, allowAll, cloud, local)
	resp, err := g.Complete(context.Background(), req("product-agent", "local"))
	if err != nil || resp.Provider != "local" || resp.Envelope.Action != mg.ActionFinal {
		t.Fatalf("expected local fallback: %+v %v", resp, err)
	}
	if cloud.calls != 1 {
		t.Fatalf("permanent errors must not be retried, got %d calls", cloud.calls)
	}
	if countCalls(t, h, "error") != 1 || countCalls(t, h, "ok") != 1 {
		t.Fatal("each attempt must be recorded")
	}
	for _, st := range g.Health(context.Background()) {
		if st.Name == "cloud" && (st.Healthy || st.Error == "") {
			t.Fatalf("recently failed provider should report unhealthy: %+v", st)
		}
	}
	// The failed provider cools down: the next turn goes straight to the fallback.
	if _, err := g.Complete(context.Background(), req("product-agent", "local")); err != nil || cloud.calls != 1 || local.calls != 2 {
		t.Fatalf("cooldown: cloud=%d local=%d err=%v", cloud.calls, local.calls, err)
	}
	mg.SetNowForTest(g, func() time.Time { return time.Now().Add(mg.ProviderCooldown + time.Second) })
	if _, err := g.Complete(context.Background(), req("product-agent", "local")); err != nil || cloud.calls != 2 {
		t.Fatalf("provider should be retried after the cooldown: cloud=%d err=%v", cloud.calls, err)
	}
}

func TestRequestScopedFailuresDoNotCoolDown(t *testing.T) {
	cloud := &fakeProvider{name: "cloud", cloud: true, respond: func(n int, r mg.ProviderRequest) (mg.ProviderResponse, error) {
		if n == 1 {
			return mg.ProviderResponse{}, &mg.ProviderError{Err: errors.New("exceeded --max-budget-usd")}
		}
		return final(0.01)(n, r)
	}}
	g, _ := setup(t, allowAll, cloud, &fakeProvider{name: "local", respond: final(0)})
	if r, err := g.Complete(context.Background(), req("product-agent", "local")); err != nil || r.Provider != "local" {
		t.Fatalf("first turn should fall back: %+v %v", r, err)
	}
	if r, err := g.Complete(context.Background(), req("product-agent", "local")); err != nil || r.Provider != "cloud" {
		t.Fatalf("a per-request failure must not block the provider: %+v %v", r, err)
	}
}

func TestTransientErrorsAreRetried(t *testing.T) {
	cloud := &fakeProvider{name: "cloud", cloud: true, respond: func(n int, r mg.ProviderRequest) (mg.ProviderResponse, error) {
		if n < 3 {
			return mg.ProviderResponse{}, &mg.ProviderError{Transient: true, Err: errors.New("overloaded")}
		}
		return final(0.01)(n, r)
	}}
	g, _ := setup(t, allowAll, cloud, &fakeProvider{name: "local", respond: final(0)})
	resp, err := g.Complete(context.Background(), req("product-agent"))
	if err != nil || cloud.calls != 3 || resp.CostUSD != 0.01 {
		t.Fatalf("retries: calls=%d resp=%+v err=%v", cloud.calls, resp, err)
	}
}

func TestCloudPolicy(t *testing.T) {
	cloud := &fakeProvider{name: "cloud", cloud: true, respond: final(0.01)}
	local := &fakeProvider{name: "local", respond: final(0)}
	g, h := setup(t, mg.Policy{CloudAllowedAgents: []string{"product-agent"}}, cloud, local)

	resp, err := g.Complete(context.Background(), req("qa-agent", "local"))
	if err != nil || resp.Provider != "local" || cloud.calls != 0 {
		t.Fatalf("disallowed agent must not reach the cloud: %+v %v calls=%d", resp, err, cloud.calls)
	}
	if countCalls(t, h, "rejected") != 1 {
		t.Fatal("rejection should be recorded")
	}
	if _, err := g.Complete(context.Background(), req("qa-agent")); !errors.Is(err, mg.ErrNoTarget) {
		t.Fatalf("no allowed target: %v", err)
	}
	if _, err := g.Complete(context.Background(), req("product-agent")); err != nil || cloud.calls != 1 {
		t.Fatalf("allowed agent: %v", err)
	}
}

func TestDailyBudget(t *testing.T) {
	cloud := &fakeProvider{name: "cloud", cloud: true, respond: final(0.5)}
	local := &fakeProvider{name: "local", respond: final(0)}
	g, h := setup(t, mg.Policy{CloudAllowedAgents: []string{"*"}, DailyBudgetUSD: map[string]float64{"default": 1}}, cloud, local)
	if err := h.DB.InsertModelCall(context.Background(), dbgen.InsertModelCallParams{
		ID: uuid.New(), AgentName: "product-agent", Alias: "reasoning", Provider: "cloud", Model: "big", Status: "ok", CostUsd: 1.5,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Complete(context.Background(), req("product-agent")); !errors.Is(err, mg.ErrBudgetExceeded) {
		t.Fatalf("expected budget exceeded, got %v", err)
	}
	resp, err := g.Complete(context.Background(), req("product-agent", "local"))
	if err != nil || resp.Provider != "local" || cloud.calls != 0 {
		t.Fatalf("free local fallback should still work: %+v %v", resp, err)
	}
	// Another agent has its own budget.
	if _, err := g.Complete(context.Background(), req("requirements-agent")); err != nil {
		t.Fatalf("other agent: %v", err)
	}
}

func TestCacheAndInvalidOutput(t *testing.T) {
	calls := 0
	cloud := &fakeProvider{name: "cloud", cloud: true, respond: func(n int, r mg.ProviderRequest) (mg.ProviderResponse, error) {
		calls++
		return final(0.01)(n, r)
	}}
	g, h := setup(t, allowAll, cloud, &fakeProvider{name: "local", respond: final(0)})
	zero := 0.0
	r := req("product-agent")
	r.Temperature = &zero
	first, err := g.Complete(context.Background(), r)
	if err != nil || first.CacheHit {
		t.Fatalf("first: %+v %v", first, err)
	}
	second, err := g.Complete(context.Background(), r)
	if err != nil || !second.CacheHit || calls != 1 || second.CostUSD != 0 {
		t.Fatalf("second should be a free cache hit: %+v %v calls=%d", second, err, calls)
	}
	var hits int
	_ = h.DB.Pool.QueryRow(context.Background(), `SELECT count(*) FROM model_calls WHERE cache_hit`).Scan(&hits)
	if hits != 1 {
		t.Fatalf("cache hit not recorded: %d", hits)
	}

	bad := &fakeProvider{name: "cloud", cloud: true, respond: func(int, mg.ProviderRequest) (mg.ProviderResponse, error) {
		return mg.ProviderResponse{Text: "I refuse to answer in JSON"}, nil
	}}
	g2, _ := setup(t, allowAll, bad, &fakeProvider{name: "local", respond: final(0)})
	resp, err := g2.Complete(context.Background(), req("product-agent"))
	if err != nil || resp.Envelope.Action != mg.ActionInvalid || resp.ParseError == "" {
		t.Fatalf("invalid output should be reported, not failed: %+v %v", resp, err)
	}
}
