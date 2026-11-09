package modelgateway

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Embedder is implemented by providers that can embed text.
type Embedder interface {
	Embed(ctx context.Context, model string, dims int, texts []string) ([][]float32, Usage, float64, error)
}

var (
	// ErrNoEmbedding means no embedding model is configured (semantic search is off).
	ErrNoEmbedding = errors.New("no embedding model is configured (Settings → Models → Embeddings)")
	// ErrEmbeddingCloud means the embedding provider is cloud and consent was not given.
	ErrEmbeddingCloud = errors.New("the embedding provider is a cloud service and sending code to it was not allowed (Settings → Models → Embeddings)")
)

// EmbeddingKey identifies the configured embedding model ("" when none).
func (g *Gateway) EmbeddingKey() string { return g.curCfg().Embedding.Key() }

// EmbeddingInfo describes the configured embedding model for the UI.
type EmbeddingInfo struct {
	Configured bool
	Key        string
	Provider   string
	Model      string
	Cloud      bool
	AllowCloud bool
	BatchSize  int
	Available  bool   // provider exists and is not cooling down
	Problem    string // why embeddings cannot run right now
}

// Embedding reports the embedding configuration and whether it can run.
func (g *Gateway) Embedding() EmbeddingInfo {
	e := g.curCfg().Embedding
	if e == nil {
		return EmbeddingInfo{Problem: ErrNoEmbedding.Error()}
	}
	info := EmbeddingInfo{Configured: true, Key: e.Key(), Provider: e.Provider, Model: e.Model, AllowCloud: e.AllowCloud, BatchSize: e.BatchSize}
	g.mu.RLock()
	fault := g.embedFault
	g.mu.RUnlock()
	p, ok := g.provider(e.Provider)
	switch {
	case fault != "":
		info.Problem = fault
	case !ok:
		info.Problem = fmt.Sprintf("provider or account %q not found", e.Provider)
	case p.Cloud() && !e.AllowCloud:
		info.Cloud, info.Problem = true, ErrEmbeddingCloud.Error()
	default:
		info.Cloud = p.Cloud()
		if _, isEmb := p.(Embedder); !isEmb {
			info.Problem = fmt.Sprintf("%q cannot embed text (use an OpenAI-compatible account)", e.Provider)
		} else if reason := g.recentFailure(e.Provider); reason != "" {
			info.Problem = "provider cooling down after an error: " + reason
		} else {
			info.Available = true
		}
	}
	return info
}

// Embed embeds texts with the configured model on behalf of caller (used for
// budgets and the usage log, e.g. "context-indexer"). Cloud providers need the
// consent flag; the caller's daily budget applies.
func (g *Gateway) Embed(ctx context.Context, caller string, texts []string) ([][]float32, string, error) {
	e := g.curCfg().Embedding
	if e == nil {
		return nil, "", ErrNoEmbedding
	}
	p, ok := g.provider(e.Provider)
	if !ok {
		return nil, "", fmt.Errorf("embedding provider or account %q not found", e.Provider)
	}
	emb, ok := p.(Embedder)
	if !ok {
		return nil, "", fmt.Errorf("%q cannot embed text (use an OpenAI-compatible account)", e.Provider)
	}
	req := TurnRequest{AgentName: caller}
	if p.Cloud() {
		if !e.AllowCloud {
			return nil, "", ErrEmbeddingCloud
		}
		blocked, _, err := g.budgetState(ctx, req)
		if err != nil {
			return nil, "", err
		}
		if blocked {
			return nil, "", fmt.Errorf("%w: daily budget for %s reached", ErrBudgetExceeded, caller)
		}
	}
	if until, cooling := g.coolingDown(e.Provider); cooling {
		return nil, "", fmt.Errorf("%s is cooling down until %s after: %s", e.Provider, until.Format(time.TimeOnly), g.recentFailure(e.Provider))
	}
	start := g.now()
	vecs, u, cost, err := emb.Embed(ctx, e.Model, e.Dimensions, texts)
	latency := g.now().Sub(start).Milliseconds()
	if err != nil {
		var pe *ProviderError
		if errors.As(err, &pe) && pe.ProviderWide {
			g.coolDown(e.Provider, err)
		}
		g.record(ctx, req, "embedding", e.Provider, e.Model, "error", truncate(err.Error(), 500), Usage{}, 0, latency, false)
		return nil, "", err
	}
	// The call happened (and may have cost money): record it before judging the result.
	for _, v := range vecs {
		if e.Dimensions > 0 && len(v) != e.Dimensions {
			msg := fmt.Sprintf("the model returned %d dimensions but %d are configured; change Dimensions in Settings → Models → Embeddings", len(v), e.Dimensions)
			g.record(ctx, req, "embedding", e.Provider, e.Model, "error", msg, u, cost, latency, false)
			g.mu.Lock()
			g.embedFault = msg
			g.mu.Unlock()
			return nil, "", errors.New(msg)
		}
	}
	g.record(ctx, req, "embedding", e.Provider, e.Model, "ok", "", u, cost, latency, false)
	return vecs, e.Key(), nil
}
