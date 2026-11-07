package modelgateway

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// CLI providers (Claude Code, Codex) execute third-party agents that ship their
// own tools. They run in wise-cli-runner: a container with no database access,
// no repositories and no control-plane secrets, which receives one account's
// credential per call. RemoteCLI is the API-side Provider; RunnerHandler is the
// runner's HTTP server. Both sides use the same local provider code.

// RunnerRequest is one model call executed by the runner.
type RunnerRequest struct {
	Kind     string          `json:"kind"` // claude-code | codex-cli
	Secret   string          `json:"secret,omitempty"`
	TimeoutS int             `json:"timeoutS"`
	Request  ProviderRequest `json:"request"`
}

// RunnerResponse carries either a response or a classified error.
type RunnerResponse struct {
	Response ProviderResponse `json:"response"`
	Error    *RunnerError     `json:"error,omitempty"`
}

type RunnerError struct {
	Message      string `json:"message"`
	Transient    bool   `json:"transient"`
	ProviderWide bool   `json:"providerWide"`
	Status       int    `json:"status"`
}

// RemoteCLI forwards calls for one CLI account to the runner.
type RemoteCLI struct {
	name, kind, url, token string
	cfg                    ProviderConfig
	client                 *http.Client
}

func NewRemoteCLI(name, kind, url, token string, cfg ProviderConfig) *RemoteCLI {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Minute
	}
	return &RemoteCLI{name: name, kind: kind, url: strings.TrimSuffix(url, "/"), token: token, cfg: cfg,
		client: &http.Client{Timeout: cfg.Timeout + 30*time.Second}}
}

func (p *RemoteCLI) Name() string { return p.name }
func (p *RemoteCLI) Cloud() bool  { return p.cfg.Cloud || p.kind == "codex-cli" }

func (p *RemoteCLI) Complete(ctx context.Context, req ProviderRequest) (ProviderResponse, error) {
	var out RunnerResponse
	err := p.post(ctx, "/v1/complete", RunnerRequest{Kind: p.kind, Secret: p.cfg.Secret, TimeoutS: int(p.cfg.Timeout / time.Second), Request: req}, &out)
	if err != nil {
		return ProviderResponse{}, err
	}
	if out.Error != nil {
		return ProviderResponse{NewSecret: out.Response.NewSecret}, &ProviderError{
			Transient: out.Error.Transient, ProviderWide: out.Error.ProviderWide, Status: out.Error.Status,
			Err: errors.New(out.Error.Message),
		}
	}
	return out.Response, nil
}

func (p *RemoteCLI) Health(ctx context.Context) error {
	var out RunnerResponse
	if err := p.post(ctx, "/v1/health", RunnerRequest{Kind: p.kind}, &out); err != nil {
		return err
	}
	if out.Error != nil {
		return errors.New(out.Error.Message)
	}
	return nil
}

func (p *RemoteCLI) post(ctx context.Context, path string, body any, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.url+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.token)
	res, err := p.client.Do(req)
	if err != nil {
		return &ProviderError{Transient: true, ProviderWide: true, Err: fmt.Errorf("cli runner unreachable: %w", err)}
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 16<<20))
	if res.StatusCode != http.StatusOK {
		return &ProviderError{ProviderWide: true, Status: res.StatusCode, Err: fmt.Errorf("cli runner: %s", truncate(strings.TrimSpace(string(data)), 300))}
	}
	return json.Unmarshal(data, out)
}

// localCLI builds the in-process provider the runner executes.
func localCLI(kind, secret string, timeout time.Duration) (Provider, error) {
	cfg := ProviderConfig{Secret: secret, Timeout: timeout, Cloud: true}
	switch kind {
	case "claude-code":
		cfg.Binary = "claude"
		return NewClaudeCode("claude-code", cfg), nil
	case "codex-cli":
		cfg.Binary = "codex"
		return NewCodexCLI("codex-cli", cfg), nil
	}
	return nil, fmt.Errorf("unknown CLI kind %q", kind)
}

// RunnerHandler serves the runner API. token is required on every request.
func RunnerHandler(token string, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	auth := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if token == "" || subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next(w, r)
		}
	}
	decode := func(w http.ResponseWriter, r *http.Request) (RunnerRequest, bool) {
		var in RunnerRequest
		if err := json.NewDecoder(io.LimitReader(r.Body, 16<<20)).Decode(&in); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return in, false
		}
		return in, true
	}
	write := func(w http.ResponseWriter, resp ProviderResponse, err error) {
		out := RunnerResponse{Response: resp}
		if err != nil {
			re := &RunnerError{Message: err.Error()}
			var pe *ProviderError
			if errors.As(err, &pe) {
				re.Transient, re.ProviderWide, re.Status = pe.Transient, pe.ProviderWide, pe.Status
				re.Message = pe.Err.Error()
			}
			out.Error = re
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	}
	mux.HandleFunc("POST /v1/complete", auth(func(w http.ResponseWriter, r *http.Request) {
		in, ok := decode(w, r)
		if !ok {
			return
		}
		timeout := time.Duration(min(max(in.TimeoutS, 30), 3600)) * time.Second
		prov, err := localCLI(in.Kind, in.Secret, timeout)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		start := time.Now()
		resp, err := prov.Complete(r.Context(), in.Request)
		// Never log prompts, outputs or credentials.
		log.Info("cli call", "kind", in.Kind, "model", in.Request.Model, "ok", err == nil, "duration_ms", time.Since(start).Milliseconds())
		write(w, resp, err)
	}))
	mux.HandleFunc("POST /v1/health", auth(func(w http.ResponseWriter, r *http.Request) {
		in, ok := decode(w, r)
		if !ok {
			return
		}
		prov, err := localCLI(in.Kind, "", time.Minute)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		write(w, ProviderResponse{}, prov.Health(r.Context()))
	}))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	return mux
}
