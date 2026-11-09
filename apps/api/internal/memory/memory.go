// Package memory keeps the developer's and each project's engineering
// knowledge: rules, preferences, decisions, facts, conventions and approved
// examples. Memories extracted by agents start as candidates and influence
// agents only after the user approves them; every change is versioned.
package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"sigs.k8s.io/yaml"

	"wise/apps/api/internal/agents"
	"wise/apps/api/internal/apperr"
	"wise/apps/api/internal/audit"
	"wise/apps/api/internal/db"
	"wise/apps/api/internal/db/dbgen"
	"wise/apps/api/internal/modelgateway"
)

var (
	Scopes     = []string{"developer", "project"}
	Kinds      = []string{"rule", "preference", "decision", "fact", "convention", "example"}
	Categories = []string{"coding", "architecture", "testing", "api", "database", "security", "tooling", "product", "other"}
)

// Settings is the panel-managed "memory" setting.
type Settings struct {
	RetrievalK                  int      `json:"retrieval_k"`
	MaxContextChars             int      `json:"max_context_chars"`
	ExtractOnReviewComments     bool     `json:"extract_on_review_comments"`
	ExtractOnCompletedWorkflows bool     `json:"extract_on_completed_workflows"`
	DuplicateSimilarity         float64  `json:"duplicate_similarity"`
	SkipAgents                  []string `json:"skip_agents"`
}

// ParseSettings decodes and validates the memory setting.
func ParseSettings(raw []byte) (Settings, error) {
	var s Settings
	if err := yaml.UnmarshalStrict(raw, &s); err != nil {
		return s, fmt.Errorf("parse: %w", err)
	}
	switch {
	case s.RetrievalK < 0 || s.RetrievalK > 30:
		return s, fmt.Errorf("retrieval_k must be 0-30")
	case s.MaxContextChars < 500 || s.MaxContextChars > 40000:
		return s, fmt.Errorf("max_context_chars must be 500-40000")
	case s.DuplicateSimilarity < 0.5 || s.DuplicateSimilarity > 1:
		return s, fmt.Errorf("duplicate_similarity must be 0.5-1")
	}
	if s.SkipAgents == nil {
		s.SkipAgents = []string{}
	}
	return s, nil
}

// Embedder is the slice of the model gateway memory uses.
type Embedder interface {
	Embed(ctx context.Context, caller string, texts []string) ([][]float32, string, error)
	Embedding() modelgateway.EmbeddingInfo
}

type Service struct {
	db     *db.DB
	agents *agents.Service
	emb    Embedder
	log    *slog.Logger

	mu       sync.RWMutex
	settings Settings
	since    time.Time // when memory went live (see liveSince)
}

func NewService(d *db.DB, a *agents.Service, emb Embedder, log *slog.Logger) *Service {
	return &Service{db: d, agents: a, emb: emb, log: log, settings: Settings{
		RetrievalK: 6, MaxContextChars: 6000, ExtractOnReviewComments: true, ExtractOnCompletedWorkflows: true, DuplicateSimilarity: 0.9,
	}}
}

func (s *Service) SetSettings(v Settings) {
	s.mu.Lock()
	s.settings = v
	s.mu.Unlock()
}

func (s *Service) Settings() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.settings
}

// Input is a memory's editable content.
type Input struct {
	Scope     string
	ProjectID *uuid.UUID
	Kind      string
	Category  string
	Title     string
	Body      string
}

func oneOf(v string, list []string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func (in *Input) validate() error {
	in.Title, in.Body = strings.TrimSpace(in.Title), strings.TrimSpace(in.Body)
	if in.Category == "" {
		in.Category = "other"
	}
	var errs []apperr.FieldError
	if !oneOf(in.Scope, Scopes) {
		errs = append(errs, apperr.FieldError{Field: "scope", Message: "developer or project"})
	}
	if in.Scope == "project" && in.ProjectID == nil {
		errs = append(errs, apperr.FieldError{Field: "projectId", Message: "required for project memories"})
	}
	if in.Scope == "developer" {
		in.ProjectID = nil
	}
	if !oneOf(in.Kind, Kinds) {
		errs = append(errs, apperr.FieldError{Field: "kind", Message: strings.Join(Kinds, ", ")})
	}
	if !oneOf(in.Category, Categories) {
		errs = append(errs, apperr.FieldError{Field: "category", Message: strings.Join(Categories, ", ")})
	}
	if in.Title == "" || utf8.RuneCountInString(in.Title) > 200 {
		errs = append(errs, apperr.FieldError{Field: "title", Message: "must be 1-200 characters"})
	}
	if in.Body == "" || utf8.RuneCountInString(in.Body) > 8000 {
		errs = append(errs, apperr.FieldError{Field: "body", Message: "must be 1-8000 characters"})
	}
	if len(errs) > 0 {
		return apperr.Validation(errs...)
	}
	return nil
}

func versionRow(m dbgen.Memory, by *uuid.UUID) dbgen.InsertMemoryVersionParams {
	return dbgen.InsertMemoryVersionParams{MemoryID: m.ID, Version: m.Version, Scope: m.Scope, ProjectID: m.ProjectID,
		Kind: m.Kind, Category: m.Category, Title: m.Title, Body: m.Body, Status: m.Status, ChangedBy: by}
}

// Create adds a memory written by the user: it applies immediately.
func (s *Service) Create(ctx context.Context, in Input, userID *uuid.UUID) (dbgen.Memory, error) {
	if err := in.validate(); err != nil {
		return dbgen.Memory{}, err
	}
	return s.insert(ctx, in, "active", "manual", map[string]any{}, "", nil, userID)
}

func (s *Service) insert(ctx context.Context, in Input, status, source string, refs map[string]any, rationale string, dup *uuid.UUID, userID *uuid.UUID) (dbgen.Memory, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return dbgen.Memory{}, err
	}
	var m dbgen.Memory
	err = s.db.InTx(ctx, func(q *dbgen.Queries) error {
		m, err = insertQ(ctx, q, id, in, status, source, refs, rationale, dup, userID)
		return err
	})
	return m, err
}

// insertQ stores a memory and its first version inside the caller's transaction.
func insertQ(ctx context.Context, q *dbgen.Queries, id uuid.UUID, in Input, status, source string, refs map[string]any, rationale string, dup *uuid.UUID, userID *uuid.UUID) (dbgen.Memory, error) {
	rawRefs, _ := json.Marshal(refs)
	var m dbgen.Memory
	err := func() error {
		var err error
		if in.ProjectID != nil {
			if _, err := q.GetProject(ctx, *in.ProjectID); db.IsNotFound(err) {
				return apperr.Validation(apperr.FieldError{Field: "projectId", Message: "no such project"})
			} else if err != nil {
				return err
			}
		}
		m, err = q.InsertMemory(ctx, dbgen.InsertMemoryParams{
			ID: id, Scope: in.Scope, ProjectID: in.ProjectID, Kind: in.Kind, Category: in.Category, Title: in.Title, Body: in.Body,
			Status: status, Source: source, SourceRefs: rawRefs, Rationale: rationale, DuplicateOf: dup, CreatedBy: userID,
		})
		if err != nil {
			return err
		}
		if err := q.InsertMemoryVersion(ctx, versionRow(m, userID)); err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{Action: "memory.create", EntityType: "memory", EntityID: audit.Ptr(id),
			Payload: map[string]any{"status": status, "source": source, "kind": in.Kind, "scope": in.Scope}})
	}()
	return m, err
}

// Get returns one memory.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (dbgen.Memory, error) {
	m, err := s.db.GetMemory(ctx, id)
	if db.IsNotFound(err) {
		return m, apperr.NotFound("memory not found")
	}
	return m, err
}

// Filter narrows List.
type Filter struct {
	Status, Scope, Kind, Category, Q string
	ProjectID                        *uuid.UUID
	Limit                            int
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (s *Service) List(ctx context.Context, f Filter) ([]dbgen.ListMemoriesRow, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 200
	}
	return s.db.ListMemories(ctx, dbgen.ListMemoriesParams{
		Status: nilIfEmpty(f.Status), Scope: nilIfEmpty(f.Scope), ProjectID: f.ProjectID, Kind: nilIfEmpty(f.Kind),
		Category: nilIfEmpty(f.Category), Q: nilIfEmpty(strings.TrimSpace(f.Q)), RowLimit: int32(f.Limit),
	})
}

// InboxCount is the number of candidates waiting for a decision.
func (s *Service) InboxCount(ctx context.Context) (int64, error) {
	return s.db.CountMemoryCandidates(ctx)
}

// change applies an edit and/or status change as a new version.
func (s *Service) change(ctx context.Context, id uuid.UUID, in *Input, status string, allowed []string, action string, userID *uuid.UUID) (dbgen.Memory, error) {
	if in != nil {
		if err := in.validate(); err != nil {
			return dbgen.Memory{}, err
		}
	}
	var m dbgen.Memory
	err := s.db.InTx(ctx, func(q *dbgen.Queries) error {
		cur, err := q.LockMemory(ctx, id)
		if db.IsNotFound(err) {
			return apperr.NotFound("memory not found")
		}
		if err != nil {
			return err
		}
		if len(allowed) > 0 && !oneOf(cur.Status, allowed) {
			return apperr.Conflict(fmt.Sprintf("the memory is %s", cur.Status))
		}
		next := Input{Scope: cur.Scope, ProjectID: cur.ProjectID, Kind: cur.Kind, Category: cur.Category, Title: cur.Title, Body: cur.Body}
		if in != nil {
			next = *in
		}
		if status == "" {
			status = cur.Status
		}
		p := dbgen.UpdateMemoryParams{ID: id, Scope: next.Scope, ProjectID: next.ProjectID, Kind: next.Kind, Category: next.Category,
			Title: next.Title, Body: next.Body, Status: status, DuplicateOf: cur.DuplicateOf}
		if status != cur.Status && (status == "active" || status == "rejected") {
			now := time.Now()
			p.DecidedBy, p.DecidedAt = userID, &now
		}
		if m, err = q.UpdateMemory(ctx, p); err != nil {
			return err
		}
		if err := q.InsertMemoryVersion(ctx, versionRow(m, userID)); err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{Action: action, EntityType: "memory", EntityID: audit.Ptr(id),
			Payload: map[string]any{"version": m.Version, "status": m.Status}})
	})
	return m, err
}

// Update edits a memory's content (a new version; status unchanged).
func (s *Service) Update(ctx context.Context, id uuid.UUID, in Input, userID *uuid.UUID) (dbgen.Memory, error) {
	return s.change(ctx, id, &in, "", []string{"candidate", "active", "archived"}, "memory.update", userID)
}

// Approve makes a candidate active, optionally with the user's edits.
func (s *Service) Approve(ctx context.Context, id uuid.UUID, in *Input, userID *uuid.UUID) (dbgen.Memory, error) {
	return s.change(ctx, id, in, "active", []string{"candidate"}, "memory.approve", userID)
}

// Reject discards a candidate (kept for the record).
func (s *Service) Reject(ctx context.Context, id uuid.UUID, userID *uuid.UUID) (dbgen.Memory, error) {
	return s.change(ctx, id, nil, "rejected", []string{"candidate"}, "memory.reject", userID)
}

// Archive stops a memory from influencing agents.
func (s *Service) Archive(ctx context.Context, id uuid.UUID, userID *uuid.UUID) (dbgen.Memory, error) {
	return s.change(ctx, id, nil, "archived", []string{"candidate", "active"}, "memory.archive", userID)
}

// Reactivate puts an archived memory back in use.
func (s *Service) Reactivate(ctx context.Context, id uuid.UUID, userID *uuid.UUID) (dbgen.Memory, error) {
	return s.change(ctx, id, nil, "active", []string{"archived"}, "memory.reactivate", userID)
}

// Versions lists a memory's history (newest first).
func (s *Service) Versions(ctx context.Context, id uuid.UUID) ([]dbgen.MemoryVersion, error) {
	if _, err := s.Get(ctx, id); err != nil {
		return nil, err
	}
	return s.db.ListMemoryVersions(ctx, id)
}

// Restore brings back an earlier version's content as a new version.
func (s *Service) Restore(ctx context.Context, id uuid.UUID, version int32, userID *uuid.UUID) (dbgen.Memory, error) {
	v, err := s.db.GetMemoryVersion(ctx, dbgen.GetMemoryVersionParams{MemoryID: id, Version: version})
	if db.IsNotFound(err) {
		return dbgen.Memory{}, apperr.NotFound("version not found")
	}
	if err != nil {
		return dbgen.Memory{}, err
	}
	in := Input{Scope: v.Scope, ProjectID: v.ProjectID, Kind: v.Kind, Category: v.Category, Title: v.Title, Body: v.Body}
	return s.change(ctx, id, &in, "", nil, "memory.restore", userID)
}

// Candidate is one memory proposed by an agent.
type Candidate struct {
	Scope     string `json:"scope"`
	Kind      string `json:"kind"`
	Category  string `json:"category"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	Rationale string `json:"rationale"`
}

// prepared is a validated candidate with its duplicate check done.
type prepared struct {
	in        Input
	rationale string
	dup       *uuid.UUID
}

// prepareCandidates validates proposals and finds likely duplicates (this may
// call the embedding model, so it runs outside any transaction).
func (s *Service) prepareCandidates(ctx context.Context, cands []Candidate, projectID *uuid.UUID) []prepared {
	var out []prepared
	for _, c := range cands {
		in := Input{Scope: c.Scope, ProjectID: projectID, Kind: c.Kind, Category: c.Category, Title: c.Title, Body: c.Body}
		if in.Scope == "project" && projectID == nil {
			in.Scope = "developer"
		}
		if err := in.validate(); err != nil {
			s.log.Warn("skipping invalid memory candidate", "title", c.Title, "err", err)
			continue
		}
		out = append(out, prepared{in: in, rationale: strings.TrimSpace(c.Rationale), dup: s.duplicateOf(ctx, in)})
	}
	return out
}

// AddCandidates stores proposed memories for approval (all or none), marking
// likely duplicates of existing ones.
func (s *Service) AddCandidates(ctx context.Context, cands []Candidate, projectID *uuid.UUID, source string, refs map[string]any) (int, error) {
	prep := s.prepareCandidates(ctx, cands, projectID)
	err := s.db.InTx(ctx, func(q *dbgen.Queries) error {
		return insertCandidates(ctx, q, prep, source, refs)
	})
	if err != nil {
		return 0, err
	}
	return len(prep), nil
}

func insertCandidates(ctx context.Context, q *dbgen.Queries, prep []prepared, source string, refs map[string]any) error {
	for _, p := range prep {
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		if _, err := insertQ(ctx, q, id, p.in, "candidate", source, refs, p.rationale, p.dup, nil); err != nil {
			return err
		}
	}
	return nil
}

// duplicateOf finds an existing candidate/active memory saying the same thing.
func (s *Service) duplicateOf(ctx context.Context, in Input) *uuid.UUID {
	text := in.Title + " " + in.Body
	if s.emb != nil && s.emb.Embedding().Available {
		if vecs, key, err := s.emb.Embed(ctx, "memory", []string{text}); err == nil {
			rows, err := s.db.SimilarMemoriesByVector(ctx, dbgen.SimilarMemoriesByVectorParams{
				Vec: vecText(vecs[0]), Scope: in.Scope, ProjectID: in.ProjectID, Model: key,
			})
			if err == nil && len(rows) > 0 && rows[0].Score >= s.Settings().DuplicateSimilarity {
				return &rows[0].ID
			}
		}
	}
	rows, err := s.db.SimilarMemoriesByText(ctx, dbgen.SimilarMemoriesByTextParams{Text: text, Scope: in.Scope, ProjectID: in.ProjectID})
	if err == nil && len(rows) > 0 && rows[0].Score >= 0.6 {
		return &rows[0].ID
	}
	return nil
}
