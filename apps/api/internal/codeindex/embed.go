package codeindex

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"wise/apps/api/internal/db/dbgen"
	"wise/apps/api/internal/modelgateway"
)

// Embedder is the slice of the model gateway the index uses.
type Embedder interface {
	Embed(ctx context.Context, caller string, texts []string) ([][]float32, string, error)
	EmbeddingKey() string
	Embedding() modelgateway.EmbeddingInfo
}

// EmbedStatus is the embedding worker's progress for the UI.
type EmbedStatus struct {
	Info      modelgateway.EmbeddingInfo
	Embedded  int64
	Pending   int64
	LastError string
	LastRun   time.Time
}

type embedState struct {
	mu      sync.Mutex
	lastErr string
	lastRun time.Time
}

// SetEmbedder enables semantic search and the background embedding worker.
func (s *Service) SetEmbedder(e Embedder) { s.emb = e }

// vecText renders a vector as a pgvector literal.
func vecText(v []float32) string {
	var b strings.Builder
	b.Grow(len(v) * 10)
	b.WriteByte('[')
	for i, x := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(x), 'g', 7, 32))
	}
	b.WriteByte(']')
	return b.String()
}

// embedText is what represents a chunk: where it is, what it is, then the content.
func embedText(path, heading, content string) string {
	t := path + "\n" + heading + "\n" + content
	if len(t) > 8000 {
		t = clean(t, 8000)
	}
	return t
}

// EmbedPending embeds one batch of chunks lacking a vector from the current
// model. It returns how many were embedded (0 = nothing to do).
func (s *Service) EmbedPending(ctx context.Context) (int, error) {
	if s.emb == nil {
		return 0, nil
	}
	info := s.emb.Embedding()
	if !info.Configured {
		return 0, nil
	}
	if !info.Available {
		return 0, errors.New(info.Problem)
	}
	batch := info.BatchSize
	if batch <= 0 {
		batch = 32
	}
	rows, err := s.db.PendingEmbeddingChunks(ctx, dbgen.PendingEmbeddingChunksParams{Model: info.Key, RowLimit: int32(batch)})
	if err != nil || len(rows) == 0 {
		return 0, err
	}
	texts := make([]string, len(rows))
	for i, r := range rows {
		texts[i] = embedText(r.Path, r.Heading, r.Content)
	}
	vecs, key, err := s.emb.Embed(ctx, "context-indexer", texts)
	if err != nil {
		return 0, err
	}
	err = s.db.InTx(ctx, func(q *dbgen.Queries) error {
		for i, r := range rows {
			if err := q.SetChunkEmbedding(ctx, dbgen.SetChunkEmbeddingParams{
				ID: r.ID, Vec: vecText(vecs[i]), Model: &key, ContentHash: r.ContentHash,
			}); err != nil {
				return err
			}
		}
		return nil
	})
	return len(rows), err
}

// RunEmbedder embeds pending chunks in the background until ctx is done,
// backing off after errors (e.g. provider down, budget reached).
func (s *Service) RunEmbedder(ctx context.Context) {
	wait := 15 * time.Second
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		n, err := s.EmbedPending(ctx)
		s.embedState.mu.Lock()
		s.embedState.lastRun = time.Now()
		if err != nil {
			s.embedState.lastErr = err.Error()
		} else {
			s.embedState.lastErr = ""
		}
		s.embedState.mu.Unlock()
		switch {
		case err != nil:
			s.log.Warn("embedding chunks", "err", err)
			wait = 2 * time.Minute
		case n > 0:
			wait = 500 * time.Millisecond // keep going while there is work
		default:
			wait = 20 * time.Second
		}
	}
}

// EmbeddingStatus reports semantic search readiness and progress.
func (s *Service) EmbeddingStatus(ctx context.Context) (EmbedStatus, error) {
	st := EmbedStatus{}
	if s.emb == nil {
		return st, nil
	}
	st.Info = s.emb.Embedding()
	if st.Info.Configured {
		c, err := s.db.CountEmbeddings(ctx, st.Info.Key)
		if err != nil {
			return st, err
		}
		st.Embedded, st.Pending = c.Embedded, c.Pending
	}
	s.embedState.mu.Lock()
	st.LastError, st.LastRun = s.embedState.lastErr, s.embedState.lastRun
	s.embedState.mu.Unlock()
	return st, nil
}

// semantic returns chunk hits by meaning (nil when semantic search is off or failed).
func (s *Service) semantic(ctx context.Context, q SearchQuery, kind *string) ([]Hit, error) {
	if s.emb == nil || !s.emb.Embedding().Available {
		return nil, nil
	}
	vecs, key, err := s.emb.Embed(ctx, "context-search", []string{q.Text})
	if err != nil {
		return nil, err
	}
	rows, err := s.db.SemanticChunks(ctx, dbgen.SemanticChunksParams{
		Vec: vecText(vecs[0]), ProjectID: q.ProjectID, Model: key, RepositoryID: q.RepositoryID, Kind: kind, RowLimit: int32(q.Limit),
	})
	if err != nil {
		return nil, err
	}
	hits := make([]Hit, 0, len(rows))
	for _, r := range rows {
		hits = append(hits, Hit{
			Kind: r.Kind, Repository: r.RepositoryName, RepositoryID: r.RepositoryID.String(), Path: r.Path, Language: r.Language,
			StartLine: int(r.StartLine), EndLine: int(r.EndLine), Title: r.Heading, Snippet: snippet(r.Content, 600),
			Score: 1 - r.Distance, Why: "similar meaning", IndexedSHA: deref(r.IndexedSha), Stale: stale(r.IndexedSha, r.HeadSha),
			id: r.ID.String(),
		})
	}
	return hits, nil
}

// fuse merges ranked lists with reciprocal-rank fusion; a hit found by both
// keyword and meaning says so.
func fuse(limit int, lists ...[]Hit) []Hit {
	const k = 60.0
	type acc struct {
		hit   Hit
		score float64
		whys  []string
	}
	by := map[string]*acc{}
	var order []string
	for _, list := range lists {
		for rank, h := range list {
			key := h.id
			if key == "" {
				key = h.Repository + "\x00" + h.Path + "\x00" + strconv.Itoa(h.StartLine)
			}
			a, ok := by[key]
			if !ok {
				a = &acc{hit: h}
				by[key] = a
				order = append(order, key)
			}
			a.score += 1 / (k + float64(rank+1))
			a.whys = append(a.whys, h.Why)
		}
	}
	out := make([]Hit, 0, len(order))
	for _, key := range order {
		a := by[key]
		h := a.hit
		h.Score = a.score
		if len(a.whys) > 1 {
			h.Why = strings.Join(dedupe(a.whys), " + ")
		}
		out = append(out, h)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
