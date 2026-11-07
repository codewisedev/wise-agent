package modelgateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
)

// OpenAICompat talks to any /v1/chat/completions server (LM Studio, vLLM, ...).
type OpenAICompat struct {
	name   string
	cfg    ProviderConfig
	client *http.Client

	mu           sync.Mutex
	defaultModel string
}

func NewOpenAICompat(name string, cfg ProviderConfig) *OpenAICompat {
	return &OpenAICompat{name: name, cfg: cfg, client: &http.Client{Timeout: cfg.Timeout}}
}

func (p *OpenAICompat) Name() string { return p.name }
func (p *OpenAICompat) Cloud() bool  { return p.cfg.Cloud }

type oaMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func (p *OpenAICompat) Complete(ctx context.Context, req ProviderRequest) (ProviderResponse, error) {
	model := req.Model
	if model == "" {
		m, err := p.loadedModel(ctx)
		if err != nil {
			return ProviderResponse{}, err
		}
		model = m
	}
	msgs := []oaMessage{{Role: "system", Content: req.System}}
	for _, m := range req.Messages {
		msgs = append(msgs, oaMessage{Role: openAIRole(m.Role), Content: renderContent(m)})
	}
	body := map[string]any{"model": model, "messages": msgs, "stream": false}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.MaxOutputTokens > 0 {
		body["max_tokens"] = req.MaxOutputTokens
	}
	if len(req.Schema) > 0 {
		body["response_format"] = map[string]any{
			"type":        "json_schema",
			"json_schema": map[string]any{"name": "agent_turn", "schema": req.Schema},
		}
	}
	var out struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int64 `json:"prompt_tokens"`
			CompletionTokens int64 `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := p.do(ctx, http.MethodPost, "/chat/completions", body, &out); err != nil {
		return ProviderResponse{}, err
	}
	if len(out.Choices) == 0 {
		return ProviderResponse{}, &ProviderError{Transient: true, Err: errors.New("response has no choices")}
	}
	usage := Usage{InputTokens: out.Usage.PromptTokens, OutputTokens: out.Usage.CompletionTokens}
	if out.Model == "" {
		out.Model = model
	}
	return ProviderResponse{
		Text:    out.Choices[0].Message.Content,
		Usage:   usage,
		CostUSD: price(p.cfg, usage),
		Model:   out.Model,
	}, nil
}

func (p *OpenAICompat) Health(ctx context.Context) error {
	_, err := p.models(ctx)
	return err
}

func (p *OpenAICompat) models(ctx context.Context) ([]string, error) {
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := p.do(ctx, http.MethodGet, "/models", nil, &out); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(out.Data))
	for _, d := range out.Data {
		ids = append(ids, d.ID)
	}
	return ids, nil
}

// loadedModel picks the first model the server reports when an alias leaves the model empty.
func (p *OpenAICompat) loadedModel(ctx context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.defaultModel != "" {
		return p.defaultModel, nil
	}
	ids, err := p.models(ctx)
	if err != nil {
		return "", err
	}
	for _, id := range ids {
		if !strings.Contains(strings.ToLower(id), "embed") {
			p.defaultModel = id
			return id, nil
		}
	}
	return "", &ProviderError{Err: fmt.Errorf("%s has no chat model loaded", p.name)}
}

func (p *OpenAICompat) do(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(p.cfg.BaseURL, "/")+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if p.cfg.APIKeyEnv != "" {
		if key := os.Getenv(p.cfg.APIKeyEnv); key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
	}
	res, err := p.client.Do(req)
	if err != nil {
		return &ProviderError{Transient: true, ProviderWide: true, Err: fmt.Errorf("%s unreachable: %w", p.name, err)}
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if res.StatusCode >= 300 {
		return &ProviderError{
			Transient:    res.StatusCode == 429 || res.StatusCode >= 500,
			ProviderWide: res.StatusCode == 401 || res.StatusCode == 403,
			Status:       res.StatusCode,
			Err:          fmt.Errorf("%s: %s", p.name, truncate(strings.TrimSpace(string(data)), 500)),
		}
	}
	if err := json.Unmarshal(data, out); err != nil {
		return &ProviderError{Err: fmt.Errorf("%s: decode response: %w", p.name, err)}
	}
	return nil
}

func openAIRole(role string) string {
	if role == "assistant" {
		return "assistant"
	}
	return "user"
}

// renderContent turns tool results into plain user messages for the envelope protocol.
func renderContent(m Message) string {
	if m.Role == "tool" {
		return fmt.Sprintf("[tool result: %s]\n%s", m.Name, m.Content)
	}
	return m.Content
}

func price(cfg ProviderConfig, u Usage) float64 {
	return (float64(u.InputTokens)*cfg.InputPerMTok + float64(u.OutputTokens)*cfg.OutputPerMTok) / 1_000_000
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
