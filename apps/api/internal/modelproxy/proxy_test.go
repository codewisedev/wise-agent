package modelproxy

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	control  = "control-token-0123456789abcdef0123456789"
	runToken = "run-token-0123456789abcdef0123456789abcd"
	realKey  = "sk-ant-real-key"
)

type upstream struct {
	mu   sync.Mutex
	seen []http.Header
}

func (u *upstream) server() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		u.seen = append(u.seen, r.Header.Clone())
		u.mu.Unlock()
		body, _ := io.ReadAll(r.Body)
		switch {
		case r.URL.Path == "/v1/messages" && strings.Contains(string(body), `"stream":true`):
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"model\":\"claude-x\",\"usage\":{\"input_tokens\":100,\"cache_read_input_tokens\":50,\"output_tokens\":1}}}\n\n")
			fmt.Fprint(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"hi\"}}\n\n")
			fmt.Fprint(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":40}}\n\n")
		case r.URL.Path == "/v1/messages":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"model":"claude-x","usage":{"input_tokens":1000,"output_tokens":500}}`)
		case r.URL.Path == "/v1/responses":
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"type\":\"response.created\"}\n\n")
			fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"model\":\"gpt-x\",\"usage\":{\"input_tokens\":300,\"output_tokens\":30}}}\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
}

func setup(t *testing.T) (*Proxy, *httptest.Server, *upstream, *Client) {
	t.Helper()
	up := &upstream{}
	us := up.server()
	t.Cleanup(us.Close)
	p := New(control, slog.New(slog.NewTextHandler(io.Discard, nil)))
	p.Upstreams = map[string]string{"anthropic": us.URL, "openai": us.URL}
	ps := httptest.NewServer(p.Handler())
	t.Cleanup(ps.Close)
	return p, ps, up, NewClient(ps.URL, control)
}

func call(t *testing.T, url, header, value, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	req.Header.Set(header, value)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

func TestAnthropicKeySwapUsageAndBudget(t *testing.T) {
	_, ps, up, c := setup(t)
	ctx := context.Background()
	err := c.Register(ctx, runToken, Run{RunID: "r1", Vendor: "anthropic", APIKey: realKey, BudgetUSD: 0.01,
		InputPerMTok: 3, OutputPerMTok: 15, Expires: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	// Unknown token: refused in the vendor's error shape, nothing forwarded.
	if code, body := call(t, ps.URL+"/anthropic/v1/messages", "x-api-key", "nope", `{}`); code != 401 || !strings.Contains(body, "authentication_error") {
		t.Fatalf("unknown token: %d %s", code, body)
	}
	// Streaming call: the upstream sees the real key, never the run token.
	code, body := call(t, ps.URL+"/anthropic/v1/messages", "x-api-key", runToken, `{"model":"claude-x","stream":true}`)
	if code != 200 || !strings.Contains(body, "message_delta") {
		t.Fatalf("stream: %d %s", code, body)
	}
	up.mu.Lock()
	h := up.seen[0]
	up.mu.Unlock()
	if h.Get("x-api-key") != realKey || strings.Contains(fmt.Sprint(h), runToken) {
		t.Fatalf("upstream headers: %v", h)
	}
	// Non-streaming call (cost 1000*3/1e6 + 500*15/1e6 = 0.0105): over budget afterwards.
	if code, _ := call(t, ps.URL+"/anthropic/v1/messages", "Authorization", "Bearer "+runToken, `{"model":"claude-x"}`); code != 200 {
		t.Fatalf("json call: %d", code)
	}
	if code, body := call(t, ps.URL+"/anthropic/v1/messages", "x-api-key", runToken, `{"model":"claude-x"}`); code != 402 || !strings.Contains(body, "budget") {
		t.Fatalf("budget: %d %s", code, body)
	}
	u, err := c.Revoke(ctx, runToken)
	if err != nil {
		t.Fatal(err)
	}
	if len(u.Calls) != 2 || u.InputTokens != 1150 || u.OutputTokens != 540 || !u.Exhausted || u.Calls[0].Model != "claude-x" {
		t.Fatalf("usage: %+v", u)
	}
	// Revoked: the token no longer works.
	if code, _ := call(t, ps.URL+"/anthropic/v1/messages", "x-api-key", runToken, `{}`); code != 401 {
		t.Fatalf("after revoke: %d", code)
	}
}

func TestOpenAIResponsesAndVendorScoping(t *testing.T) {
	_, ps, up, c := setup(t)
	ctx := context.Background()
	if err := c.Register(ctx, runToken, Run{RunID: "r2", Vendor: "openai", APIKey: "sk-real-openai", InputPerMTok: 1, OutputPerMTok: 2,
		Expires: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if code, _ := call(t, ps.URL+"/openai/v1/responses", "Authorization", "Bearer "+runToken, `{"model":"gpt-x","stream":true}`); code != 200 {
		t.Fatalf("responses: %d", code)
	}
	up.mu.Lock()
	auth := up.seen[0].Get("Authorization")
	up.mu.Unlock()
	if auth != "Bearer sk-real-openai" {
		t.Fatalf("upstream auth %q", auth)
	}
	// An OpenAI run token cannot be used on the Anthropic route.
	if code, _ := call(t, ps.URL+"/anthropic/v1/messages", "x-api-key", runToken, `{}`); code != 401 {
		t.Fatalf("cross-vendor: %d", code)
	}
	u, _ := c.Revoke(ctx, runToken)
	if u.InputTokens != 300 || u.OutputTokens != 30 || u.CostUSD < 0.00035 || u.CostUSD > 0.00037 {
		t.Fatalf("usage: %+v", u)
	}
}

func TestControlNeedsTokenAndValidRuns(t *testing.T) {
	_, ps, _, c := setup(t)
	bad := NewClient(ps.URL, "wrong")
	if err := bad.Register(context.Background(), runToken, Run{Vendor: "anthropic", APIKey: "k", Expires: time.Now().Add(time.Hour)}); err == nil {
		t.Fatal("control without the token")
	}
	for _, r := range []Run{{Vendor: "gemini", APIKey: "k", Expires: time.Now().Add(time.Hour)}, {Vendor: "anthropic", Expires: time.Now().Add(time.Hour)},
		{Vendor: "anthropic", APIKey: "k", Expires: time.Now().Add(-time.Minute)}} {
		if err := c.Register(context.Background(), runToken, r); err == nil {
			t.Errorf("accepted %+v", r)
		}
	}
	if err := c.Register(context.Background(), "short", Run{Vendor: "anthropic", APIKey: "k", Expires: time.Now().Add(time.Hour)}); err == nil {
		t.Error("short token accepted")
	}
}
