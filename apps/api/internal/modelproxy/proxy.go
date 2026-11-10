// Package modelproxy is the Runtime Proxy between coding-CLI runners and model
// vendors. A runner only ever holds a per-run token; the proxy swaps it for
// the account's real API key on the trusted side, records usage and cost, and
// refuses further calls once the run's budget is spent. The API registers and
// revokes runs over a token-guarded control endpoint.
package modelproxy

import (
	"bufio"
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Vendors the proxy forwards to (base URLs; tests may override).
var defaultUpstreams = map[string]string{
	"anthropic": "https://api.anthropic.com",
	"openai":    "https://api.openai.com",
}

// Run is a registered run.
type Run struct {
	RunID         string    `json:"runId"`
	Vendor        string    `json:"vendor"` // anthropic | openai
	APIKey        string    `json:"apiKey"`
	BudgetUSD     float64   `json:"budgetUsd"` // 0 = unlimited
	InputPerMTok  float64   `json:"inputPerMTok"`
	OutputPerMTok float64   `json:"outputPerMTok"`
	Expires       time.Time `json:"expires"`
}

// Call is one forwarded model call.
type Call struct {
	Model        string    `json:"model"`
	Status       int       `json:"status"`
	InputTokens  int64     `json:"inputTokens"`
	OutputTokens int64     `json:"outputTokens"`
	CostUSD      float64   `json:"costUsd"`
	DurationMs   int64     `json:"durationMs"`
	Error        string    `json:"error,omitempty"`
	At           time.Time `json:"at"`
}

// Usage is a run's calls so far.
type Usage struct {
	Calls        []Call  `json:"calls"`
	InputTokens  int64   `json:"inputTokens"`
	OutputTokens int64   `json:"outputTokens"`
	CostUSD      float64 `json:"costUsd"`
	Exhausted    bool    `json:"exhausted"`
}

type entry struct {
	run   Run
	usage Usage
}

// Proxy is the Runtime Proxy.
type Proxy struct {
	Token     string // control token
	Log       *slog.Logger
	Upstreams map[string]string
	HTTP      *http.Client

	mu   sync.Mutex
	runs map[string]*entry // by run token
}

// New returns a Proxy.
func New(token string, log *slog.Logger) *Proxy {
	return &Proxy{Token: token, Log: log, Upstreams: defaultUpstreams,
		HTTP: &http.Client{Timeout: 30 * time.Minute}, runs: map[string]*entry{}}
}

// Handler serves the data plane (/anthropic/…, /openai/…) and the control
// plane (/v1/runs/…, bearer control token).
func (p *Proxy) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("PUT /v1/runs/{token}", p.control(p.register))
	mux.HandleFunc("DELETE /v1/runs/{token}", p.control(p.revoke))
	mux.HandleFunc("GET /v1/runs/{token}/usage", p.control(p.usage))
	mux.HandleFunc("/anthropic/", func(w http.ResponseWriter, r *http.Request) { p.forward(w, r, "anthropic") })
	mux.HandleFunc("/openai/", func(w http.ResponseWriter, r *http.Request) { p.forward(w, r, "openai") })
	return mux
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (p *Proxy) control(h func(http.ResponseWriter, *http.Request)) http.HandlerFunc {
	want := []byte("Bearer " + p.Token)
	return func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), want) != 1 {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		h(w, r)
	}
}

func (p *Proxy) register(w http.ResponseWriter, r *http.Request) {
	var run Run
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&run); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	tok := r.PathValue("token")
	if len(tok) < 32 || run.APIKey == "" || p.Upstreams[run.Vendor] == "" || run.Expires.Before(time.Now()) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "token (32+ chars), vendor, apiKey and a future expiry are required"})
		return
	}
	p.mu.Lock()
	p.gc()
	p.runs[tok] = &entry{run: run, usage: Usage{Calls: []Call{}}}
	p.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (p *Proxy) revoke(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	e := p.runs[r.PathValue("token")]
	delete(p.runs, r.PathValue("token"))
	p.mu.Unlock()
	if e == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown run"})
		return
	}
	writeJSON(w, http.StatusOK, e.usage)
}

func (p *Proxy) usage(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	e := p.runs[r.PathValue("token")]
	var u Usage
	if e != nil {
		u = e.usage
		u.Calls = append([]Call(nil), e.usage.Calls...)
	}
	p.mu.Unlock()
	if e == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown run"})
		return
	}
	writeJSON(w, http.StatusOK, u)
}

// gc drops expired runs (callers hold p.mu).
func (p *Proxy) gc() {
	now := time.Now()
	for k, e := range p.runs {
		if now.After(e.run.Expires.Add(time.Hour)) {
			delete(p.runs, k)
		}
	}
}

// runFor authenticates a data-plane request by its run token.
func (p *Proxy) runFor(r *http.Request, vendor string) (string, *entry) {
	tok := r.Header.Get("x-api-key")
	if tok == "" {
		tok = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	e := p.runs[tok]
	if e == nil || e.run.Vendor != vendor || time.Now().After(e.run.Expires) {
		return "", nil
	}
	return tok, e
}

// vendorError answers in the vendor's error shape so the CLI reports it.
func vendorError(w http.ResponseWriter, vendor string, code int, kind, msg string) {
	if vendor == "anthropic" {
		writeJSON(w, code, map[string]any{"type": "error", "error": map[string]string{"type": kind, "message": msg}})
		return
	}
	writeJSON(w, code, map[string]any{"error": map[string]string{"type": kind, "message": msg, "code": kind}})
}

// hop-by-hop and credential headers never forwarded upstream.
var dropHeaders = []string{"Authorization", "X-Api-Key", "Proxy-Authorization", "Connection", "Keep-Alive", "Te", "Trailer",
	"Upgrade", "Proxy-Connection", "Cookie", "Accept-Encoding"}

func (p *Proxy) forward(w http.ResponseWriter, r *http.Request, vendor string) {
	tok, e := p.runFor(r, vendor)
	if e == nil {
		vendorError(w, vendor, http.StatusUnauthorized, "authentication_error", "wise: unknown or expired run token")
		return
	}
	p.mu.Lock()
	run := e.run
	exhausted := run.BudgetUSD > 0 && e.usage.CostUSD >= run.BudgetUSD
	if exhausted {
		e.usage.Exhausted = true
	}
	p.mu.Unlock()
	if exhausted {
		vendorError(w, vendor, http.StatusPaymentRequired, "budget_exceeded", fmt.Sprintf("wise: the run's budget of $%.2f is spent", run.BudgetUSD))
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/"+vendor)
	target, err := url.Parse(p.Upstreams[vendor] + path)
	if err != nil {
		vendorError(w, vendor, http.StatusBadRequest, "invalid_request_error", "bad path")
		return
	}
	target.RawQuery = r.URL.RawQuery
	body, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	if err != nil {
		vendorError(w, vendor, http.StatusBadRequest, "invalid_request_error", "bad body")
		return
	}
	var reqMeta struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &reqMeta)
	out, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), bytes.NewReader(body))
	if err != nil {
		vendorError(w, vendor, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	out.Header = r.Header.Clone()
	for _, h := range dropHeaders {
		out.Header.Del(h)
	}
	if vendor == "anthropic" {
		out.Header.Set("x-api-key", run.APIKey)
	} else {
		out.Header.Set("Authorization", "Bearer "+run.APIKey)
	}
	start := time.Now()
	res, err := p.HTTP.Do(out)
	if err != nil {
		p.record(tok, Call{Model: reqMeta.Model, Status: 502, Error: err.Error(), At: start, DurationMs: time.Since(start).Milliseconds()})
		vendorError(w, vendor, http.StatusBadGateway, "api_error", "wise: upstream unreachable")
		return
	}
	defer res.Body.Close()
	for k, vs := range res.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(res.StatusCode)
	// Copy through while metering usage (JSON or server-sent events).
	m := &meter{vendor: vendor}
	flusher, _ := w.(http.Flusher)
	sc := bufio.NewReader(res.Body)
	var whole bytes.Buffer
	sse := strings.Contains(res.Header.Get("Content-Type"), "event-stream")
	for {
		line, err := sc.ReadBytes('\n')
		if len(line) > 0 {
			_, _ = w.Write(line)
			if sse {
				m.sseLine(line)
				if flusher != nil {
					flusher.Flush()
				}
			} else if whole.Len() < 8<<20 {
				whole.Write(line)
			}
		}
		if err != nil {
			break
		}
	}
	if !sse {
		m.json(whole.Bytes())
	}
	c := Call{Model: reqMeta.Model, Status: res.StatusCode, InputTokens: m.in, OutputTokens: m.out, At: start, DurationMs: time.Since(start).Milliseconds()}
	if m.model != "" {
		c.Model = m.model
	}
	c.CostUSD = (float64(c.InputTokens)*run.InputPerMTok + float64(c.OutputTokens)*run.OutputPerMTok) / 1e6
	if res.StatusCode >= 400 {
		c.Error = http.StatusText(res.StatusCode)
	}
	p.record(tok, c)
}

func (p *Proxy) record(tok string, c Call) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e := p.runs[tok]
	if e == nil {
		return
	}
	if len(e.usage.Calls) < 5000 {
		e.usage.Calls = append(e.usage.Calls, c)
	}
	e.usage.InputTokens += c.InputTokens
	e.usage.OutputTokens += c.OutputTokens
	e.usage.CostUSD += c.CostUSD
}

// meter reads token usage from vendor responses.
type meter struct {
	vendor  string
	model   string
	in, out int64
}

type anthropicUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
}

func (u anthropicUsage) input() int64 {
	return u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens
}

type openaiUsage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	InputTokens      int64 `json:"input_tokens"`
	OutputTokens     int64 `json:"output_tokens"`
}

func (u openaiUsage) tokens() (int64, int64) {
	return u.PromptTokens + u.InputTokens, u.CompletionTokens + u.OutputTokens
}

func (m *meter) sseLine(line []byte) {
	data, ok := bytes.CutPrefix(bytes.TrimSpace(line), []byte("data:"))
	if !ok {
		return
	}
	data = bytes.TrimSpace(data)
	if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
		return
	}
	var ev struct {
		Type    string `json:"type"`
		Model   string `json:"model"`
		Message struct {
			Model string         `json:"model"`
			Usage anthropicUsage `json:"usage"`
		} `json:"message"`
		Usage    json.RawMessage `json:"usage"`
		Response struct {
			Model string      `json:"model"`
			Usage openaiUsage `json:"usage"`
		} `json:"response"`
	}
	if json.Unmarshal(data, &ev) != nil {
		return
	}
	switch {
	case m.vendor == "anthropic" && ev.Type == "message_start":
		m.model = ev.Message.Model
		m.in += ev.Message.Usage.input()
		m.out += ev.Message.Usage.OutputTokens
	case m.vendor == "anthropic" && ev.Type == "message_delta":
		// message_delta carries the cumulative output count for the message.
		var u anthropicUsage
		if json.Unmarshal(ev.Usage, &u) == nil && u.OutputTokens > 0 {
			m.out = u.OutputTokens
		}
	case m.vendor == "openai" && ev.Type == "response.completed":
		m.model = ev.Response.Model
		m.in, m.out = ev.Response.Usage.tokens()
	case m.vendor == "openai" && len(ev.Usage) > 0 && string(ev.Usage) != "null":
		// Chat completions with stream_options.include_usage.
		var u openaiUsage
		if json.Unmarshal(ev.Usage, &u) == nil {
			m.in, m.out = u.tokens()
			if ev.Model != "" {
				m.model = ev.Model
			}
		}
	}
}

func (m *meter) json(body []byte) {
	var r struct {
		Model string          `json:"model"`
		Usage json.RawMessage `json:"usage"`
	}
	if json.Unmarshal(body, &r) != nil || len(r.Usage) == 0 {
		return
	}
	m.model = r.Model
	if m.vendor == "anthropic" {
		var u anthropicUsage
		if json.Unmarshal(r.Usage, &u) == nil {
			m.in, m.out = u.input(), u.OutputTokens
		}
		return
	}
	var u openaiUsage
	if json.Unmarshal(r.Usage, &u) == nil {
		m.in, m.out = u.tokens()
	}
}

// ---- client (used by the API) ----

// Client registers runs with the proxy.
type Client struct {
	base, token string
	http        *http.Client
}

// NewClient returns a client, or nil when the proxy is not configured.
func NewClient(base, token string) *Client {
	if base == "" || token == "" {
		return nil
	}
	return &Client{base: base, token: token, http: &http.Client{Timeout: 15 * time.Second}}
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("model proxy unreachable: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		return fmt.Errorf("model proxy: %d %s", res.StatusCode, strings.TrimSpace(string(b)))
	}
	if out != nil {
		return json.NewDecoder(res.Body).Decode(out)
	}
	return nil
}

// Register adds a run under its token.
func (c *Client) Register(ctx context.Context, token string, run Run) error {
	return c.do(ctx, http.MethodPut, "/v1/runs/"+url.PathEscape(token), run, nil)
}

// Revoke ends a run and returns its usage.
func (c *Client) Revoke(ctx context.Context, token string) (Usage, error) {
	var u Usage
	err := c.do(ctx, http.MethodDelete, "/v1/runs/"+url.PathEscape(token), nil, &u)
	return u, err
}

// ErrNotConfigured is returned when proxy mode is used without a proxy.
var ErrNotConfigured = errors.New("the model proxy is not configured")
