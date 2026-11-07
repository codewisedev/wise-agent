// Package modelgateway is the single path for every model request in Wise.
// It routes model aliases to providers, enforces cloud policy and budgets,
// retries and falls back, caches deterministic requests, and records every
// call with tokens and cost. Agents never talk to providers directly.
package modelgateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// Message is one entry of a conversation transcript.
type Message struct {
	Role    string `json:"role"` // "user" | "assistant" | "tool"
	Content string `json:"content"`
	// Name is the tool name for role "tool".
	Name string `json:"name,omitempty"`
}

// ToolSpec describes a tool the model may call.
type ToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// ToolCall is a tool invocation requested by the model.
type ToolCall struct {
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments"`
}

type Usage struct {
	InputTokens  int64 `json:"inputTokens"`
	OutputTokens int64 `json:"outputTokens"`
}

// ProviderRequest is what a provider receives: a fully rendered request.
type ProviderRequest struct {
	Model           string
	System          string
	Messages        []Message
	Schema          json.RawMessage // JSON Schema the response must satisfy
	Temperature     *float64
	MaxOutputTokens int
	MaxCostUSD      float64 // remaining budget; providers that support it enforce it
}

// ProviderResponse is a raw provider result.
type ProviderResponse struct {
	Text    string
	JSON    json.RawMessage // structured output when the provider returns it separately
	Usage   Usage
	CostUSD float64
	Model   string
}

// Provider adapts one model backend.
type Provider interface {
	Name() string
	// Cloud reports whether requests leave the machine (subject to cloud policy).
	Cloud() bool
	Complete(ctx context.Context, req ProviderRequest) (ProviderResponse, error)
	Health(ctx context.Context) error
}

// ProviderError classifies failures for retry decisions.
type ProviderError struct {
	Transient bool
	// ProviderWide marks failures that affect every request (not logged in, CLI
	// missing, server unreachable); the gateway then skips the provider briefly.
	ProviderWide bool
	Status       int
	Err          error
}

func (e *ProviderError) Error() string {
	if e.Status != 0 {
		return fmt.Sprintf("provider status %d: %v", e.Status, e.Err)
	}
	return e.Err.Error()
}
func (e *ProviderError) Unwrap() error { return e.Err }

func providerWide(err error) bool {
	var pe *ProviderError
	return errors.As(err, &pe) && pe.ProviderWide
}

func transient(err error) bool {
	var pe *ProviderError
	if errors.As(err, &pe) {
		return pe.Transient
	}
	return errors.Is(err, context.DeadlineExceeded)
}

var (
	// ErrBudgetExceeded means no target could run within the run or agent budget.
	ErrBudgetExceeded = errors.New("budget exceeded")
	// ErrNoTarget means every target was disallowed or failed.
	ErrNoTarget = errors.New("no model target available")
	// ErrUnknownAlias means the alias is not configured.
	ErrUnknownAlias = errors.New("unknown model alias")
)
