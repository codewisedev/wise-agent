package codeindex

import (
	"context"
	"regexp"
	"strings"

	"github.com/google/uuid"

	"wise/apps/api/internal/apperr"
	"wise/apps/api/internal/db"
	"wise/apps/api/internal/db/dbgen"
)

// Hit is one search result: a location to read, never a substitute for reading it.
type Hit struct {
	Kind         string  `json:"kind"` // code | doc | config | symbol
	Repository   string  `json:"repository"`
	RepositoryID string  `json:"repositoryId"`
	Path         string  `json:"path"`
	Language     string  `json:"language"`
	StartLine    int     `json:"startLine"`
	EndLine      int     `json:"endLine"`
	Title        string  `json:"title"` // symbol / heading
	Snippet      string  `json:"snippet"`
	Score        float64 `json:"score"`
	Why          string  `json:"why"`
	IndexedSHA   string  `json:"indexedSha"`
	Stale        bool    `json:"stale"` // HEAD moved since this was indexed

	id string // chunk id, for merging keyword and semantic results
}

// SearchQuery narrows a search.
type SearchQuery struct {
	ProjectID    uuid.UUID
	Text         string
	Kind         string // "", code, doc, config, symbol
	RepositoryID *uuid.UUID
	Limit        int
}

// queryText splits identifiers in the query the same way chunks are indexed.
func queryText(q string) string {
	var words []string
	for _, w := range strings.Fields(q) {
		if strings.ContainsAny(w, `"`) || strings.HasPrefix(w, "-") || strings.EqualFold(w, "or") {
			words = append(words, w) // websearch syntax
			continue
		}
		split := camel.ReplaceAllString(w, "${1}${3} ${2}${4}")
		split = strings.NewReplacer("_", " ", ".", " ", "/", " ").Replace(split)
		words = append(words, strings.ToLower(split))
	}
	return strings.Join(words, " ")
}

func stale(indexed, head *string) bool {
	return indexed != nil && head != nil && *indexed != *head
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func snippet(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return clean(s, max) + "…"
}

// Search returns symbols (name matches) and chunks (full text, and by meaning
// when an embedding model is configured) for a project. The note explains a
// degraded search (e.g. semantic search unavailable).
func (s *Service) Search(ctx context.Context, q SearchQuery) ([]Hit, string, error) {
	text := strings.TrimSpace(q.Text)
	if text == "" || len(text) > 500 {
		return nil, "", apperr.Validation(apperr.FieldError{Field: "q", Message: "must be 1-500 characters"})
	}
	if q.Limit <= 0 || q.Limit > 100 {
		q.Limit = 20
	}
	if _, err := s.db.GetProject(ctx, q.ProjectID); db.IsNotFound(err) {
		return nil, "", apperr.NotFound("project not found")
	} else if err != nil {
		return nil, "", err
	}
	var hits []Hit
	note := ""
	if q.Kind == "" || q.Kind == "symbol" {
		// A single identifier-like query is also a symbol lookup.
		if !strings.ContainsAny(text, " \t") {
			syms, err := s.Symbols(ctx, q.ProjectID, text, "", q.RepositoryID, min(q.Limit, 10))
			if err != nil {
				return nil, "", err
			}
			hits = append(hits, syms...)
		}
	}
	if q.Kind != "symbol" {
		var kind *string
		if q.Kind != "" {
			kind = &q.Kind
		}
		rows, err := s.db.SearchChunks(ctx, dbgen.SearchChunksParams{
			ProjectID: q.ProjectID, Query: queryText(text), RepositoryID: q.RepositoryID, Kind: kind, RowLimit: int32(q.Limit),
		})
		if err != nil {
			return nil, "", err
		}
		var words []Hit
		seen := map[string]bool{}
		for _, r := range rows {
			seen[r.ID.String()] = true
			words = append(words, Hit{id: r.ID.String(),
				Kind: r.Kind, Repository: r.RepositoryName, RepositoryID: r.RepositoryID.String(), Path: r.Path, Language: r.Language,
				StartLine: int(r.StartLine), EndLine: int(r.EndLine), Title: r.Heading, Snippet: snippet(r.Content, 600),
				Score: r.Rank, Why: "all words match", IndexedSHA: deref(r.IndexedSha), Stale: stale(r.IndexedSha, r.HeadSha),
			})
		}
		// Natural-language questions rarely contain every word of the code: fill up with any-word matches.
		if anyQ := anyWords(text); len(rows) < q.Limit && anyQ != "" {
			more, err := s.db.SearchChunksAny(ctx, dbgen.SearchChunksAnyParams{
				ProjectID: q.ProjectID, Query: anyQ, RepositoryID: q.RepositoryID, Kind: kind, RowLimit: int32(q.Limit),
			})
			if err != nil {
				return nil, "", err
			}
			for _, r := range more {
				if seen[r.ID.String()] {
					continue
				}
				words = append(words, Hit{id: r.ID.String(),
					Kind: r.Kind, Repository: r.RepositoryName, RepositoryID: r.RepositoryID.String(), Path: r.Path, Language: r.Language,
					StartLine: int(r.StartLine), EndLine: int(r.EndLine), Title: r.Heading, Snippet: snippet(r.Content, 600),
					Score: r.Rank, Why: "some words match", IndexedSHA: deref(r.IndexedSha), Stale: stale(r.IndexedSha, r.HeadSha),
				})
			}
		}
		// Meaning: fused with the keyword ranking (reciprocal-rank fusion).
		sem, err := s.semantic(ctx, q, kind)
		switch {
		case err != nil:
			note = "Semantic search is unavailable (" + err.Error() + "); showing keyword matches only."
			hits = append(hits, words...)
		case sem != nil:
			hits = append(hits, fuse(q.Limit, words, sem)...)
		default:
			hits = append(hits, words...)
		}
	}
	if len(hits) > q.Limit {
		hits = hits[:q.Limit]
	}
	return hits, note, nil
}

// Symbols finds symbols by name (exact, prefix, then fuzzy).
func (s *Service) Symbols(ctx context.Context, projectID uuid.UUID, name, kind string, repo *uuid.UUID, limit int) ([]Hit, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 200 {
		return nil, apperr.Validation(apperr.FieldError{Field: "name", Message: "must be 1-200 characters"})
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var k *string
	if kind != "" {
		k = &kind
	}
	rows, err := s.db.SearchSymbols(ctx, dbgen.SearchSymbolsParams{ProjectID: projectID, Query: name, RepositoryID: repo, Kind: k, RowLimit: int32(limit)})
	if err != nil {
		return nil, err
	}
	hits := make([]Hit, 0, len(rows))
	for _, r := range rows {
		title := r.Name
		if r.Parent != nil && *r.Parent != "" {
			title = *r.Parent + "." + r.Name
		}
		why := "similar name"
		switch {
		case strings.EqualFold(r.Name, name):
			why = "exact name"
		case strings.HasPrefix(strings.ToLower(r.Name), strings.ToLower(name)):
			why = "name prefix"
		}
		sn := r.Signature
		if r.Doc != "" {
			sn = r.Doc + "\n" + sn
		}
		hits = append(hits, Hit{
			Kind: "symbol", Repository: r.RepositoryName, RepositoryID: r.RepositoryID.String(), Path: r.Path, Language: r.Language,
			StartLine: int(r.StartLine), EndLine: int(r.EndLine), Title: r.Kind + " " + title, Snippet: snippet(sn, 400),
			Score: r.Score, Why: why, IndexedSHA: deref(r.IndexedSha), Stale: stale(r.IndexedSha, r.HeadSha),
		})
	}
	return hits, nil
}

// Dependencies lists a project's (or one repository's) declared dependencies.
func (s *Service) Dependencies(ctx context.Context, projectID uuid.UUID, repo *uuid.UUID) ([]dbgen.ListProjectDependenciesRow, error) {
	return s.db.ListProjectDependencies(ctx, dbgen.ListProjectDependenciesParams{ProjectID: projectID, RepositoryID: repo})
}

var stopwords = map[string]bool{
	"a": true, "an": true, "the": true, "and": true, "or": true, "of": true, "to": true, "in": true, "on": true, "for": true,
	"with": true, "is": true, "are": true, "be": true, "how": true, "what": true, "where": true, "when": true, "why": true,
	"which": true, "do": true, "does": true, "it": true, "this": true, "that": true, "by": true, "from": true, "as": true,
	"at": true, "we": true, "i": true, "you": true, "can": true, "should": true, "into": true, "about": true, "there": true,
}

var nonWord = regexp.MustCompile(`[^a-z0-9]+`)

// anyWords is a to_tsquery OR expression of the query's meaningful words (prefix-matched).
func anyWords(q string) string {
	var terms []string
	seen := map[string]bool{}
	for _, w := range strings.Fields(nonWord.ReplaceAllString(strings.ToLower(queryText(q)), " ")) {
		if len(w) < 2 || stopwords[w] || seen[w] {
			continue
		}
		seen[w] = true
		terms = append(terms, w+":*")
		if len(terms) == 12 {
			break
		}
	}
	return strings.Join(terms, " | ")
}
