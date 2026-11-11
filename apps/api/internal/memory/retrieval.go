package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"wise/apps/api/internal/db/dbgen"
)

func vecText(v []float32) string {
	var b strings.Builder
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

// Ref is a memory given to an agent.
type Ref struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Scope string `json:"scope"`
	Title string `json:"title"`
}

// Pack is the context an agent run starts with, frozen on the run.
type Pack struct {
	Text     string `json:"text"`
	Rules    []Ref  `json:"rules"`
	Relevant []Ref  `json:"relevant"`
	Project  string `json:"project,omitempty"`
	Semantic bool   `json:"semantic"`
}

var nonWord = regexp.MustCompile(`[^a-z0-9]+`)

var stop = map[string]bool{"the": true, "and": true, "for": true, "with": true, "that": true, "this": true, "from": true,
	"are": true, "was": true, "you": true, "your": true, "our": true, "can": true, "should": true, "into": true, "about": true,
	"when": true, "what": true, "how": true, "will": true, "have": true, "has": true, "not": true, "all": true, "any": true}

func orQuery(text string) string {
	var terms []string
	seen := map[string]bool{}
	for _, w := range strings.Fields(nonWord.ReplaceAllString(strings.ToLower(text), " ")) {
		if len(w) < 3 || stop[w] || seen[w] {
			continue
		}
		seen[w] = true
		terms = append(terms, w+":*")
		if len(terms) == 24 {
			break
		}
	}
	return strings.Join(terms, " | ")
}

// inputText flattens a run input into the text used to find relevant memories.
func inputText(raw []byte) string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return ""
	}
	var b strings.Builder
	var walk func(any)
	walk = func(x any) {
		if b.Len() > 4000 {
			return
		}
		switch t := x.(type) {
		case string:
			b.WriteString(t)
			b.WriteByte('\n')
		case map[string]any:
			keys := make([]string, 0, len(t))
			for k := range t {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				walk(t[k])
			}
		case []any:
			for _, e := range t {
				walk(e)
			}
		}
	}
	walk(v)
	return b.String()
}

// relevant finds active non-rule memories related to the text (by meaning when
// embeddings are available, otherwise by words).
func (s *Service) relevant(ctx context.Context, projectID *uuid.UUID, text string, k int) ([]dbgen.Memory, bool, error) {
	if k <= 0 || strings.TrimSpace(text) == "" {
		return nil, false, nil
	}
	if s.emb != nil && s.emb.Embedding().Available {
		if vecs, key, err := s.emb.Embed(ctx, "memory", []string{text}); err == nil {
			rows, err := s.db.RelevantMemoriesByVector(ctx, dbgen.RelevantMemoriesByVectorParams{
				Vec: vecText(vecs[0]), ProjectID: projectID, Model: key, RowLimit: int32(k),
			})
			if err != nil {
				return nil, false, err
			}
			out := make([]dbgen.Memory, 0, len(rows))
			for _, r := range rows {
				if r.Score >= 0.3 {
					out = append(out, memoryFromVectorRow(r))
				}
			}
			if len(out) > 0 {
				return out, true, nil
			}
		}
	}
	q := orQuery(text)
	if q == "" {
		return nil, false, nil
	}
	rows, err := s.db.RelevantMemoriesByText(ctx, dbgen.RelevantMemoriesByTextParams{Query: q, ProjectID: projectID, RowLimit: int32(k)})
	if err != nil {
		return nil, false, err
	}
	out := make([]dbgen.Memory, 0, len(rows))
	for _, r := range rows {
		out = append(out, memoryFromTextRow(r))
	}
	return out, false, nil
}

func memoryFromVectorRow(r dbgen.RelevantMemoriesByVectorRow) dbgen.Memory {
	return dbgen.Memory{ID: r.ID, Scope: r.Scope, ProjectID: r.ProjectID, Kind: r.Kind, Category: r.Category, Title: r.Title, Body: r.Body}
}

func memoryFromTextRow(r dbgen.RelevantMemoriesByTextRow) dbgen.Memory {
	return dbgen.Memory{ID: r.ID, Scope: r.Scope, ProjectID: r.ProjectID, Kind: r.Kind, Category: r.Category, Title: r.Title, Body: r.Body}
}

func short(id uuid.UUID) string { return id.String()[:8] }

// BuildPack assembles the context for a run: every active rule, the memories
// most relevant to the input, and a project summary, within the size budget.
func (s *Service) BuildPack(ctx context.Context, projectID *uuid.UUID, input []byte) (Pack, error) {
	return s.BuildPackFor(ctx, "", projectID, input)
}

// withoutImprovement drops memories written by improvement proposals.
func withoutImprovement(ms []dbgen.Memory) []dbgen.Memory {
	out := ms[:0:0]
	for _, m := range ms {
		if m.Source != SourceImprovement {
			out = append(out, m)
		}
	}
	return out
}

// BuildPackFor builds the context for an agent; the agents behind quality
// gates never get memories written by improvement proposals.
func (s *Service) BuildPackFor(ctx context.Context, agent string, projectID *uuid.UUID, input []byte) (Pack, error) {
	st := s.Settings()
	rules, err := s.db.ActiveRules(ctx, projectID)
	if err != nil {
		return Pack{}, err
	}
	rel, semantic, err := s.relevant(ctx, projectID, inputText(input), st.RetrievalK)
	if err != nil {
		return Pack{}, err
	}
	if slices.Contains(GateAgents, agent) {
		rules, rel = withoutImprovement(rules), withoutImprovement(rel)
	}
	summary := ""
	if projectID != nil {
		summary = s.projectSummary(ctx, *projectID)
	}
	if len(rules) == 0 && len(rel) == 0 && summary == "" {
		return Pack{}, nil
	}
	pack := Pack{Project: summary, Semantic: semantic}
	var b strings.Builder
	b.WriteString("## Wise context\n")
	b.WriteString("Knowledge the user approved (their engineering profile and project memory). It is guidance from the user, not part of the task.\n")
	budget := st.MaxContextChars
	add := func(line string) bool {
		if b.Len()+len(line) > budget {
			return false
		}
		b.WriteString(line)
		return true
	}
	if len(rules) > 0 {
		add("\n### Rules — always follow these\n")
		for _, r := range rules {
			scope := "you"
			if r.Scope == "project" {
				scope = "this project"
			}
			if !add(fmt.Sprintf("- [%s] (%s, %s) %s: %s\n", short(r.ID), r.Category, scope, r.Title, oneLine(r.Body, 600))) {
				break
			}
			pack.Rules = append(pack.Rules, Ref{ID: r.ID.String(), Kind: r.Kind, Scope: r.Scope, Title: r.Title})
		}
	}
	if len(rel) > 0 {
		add("\n### Relevant knowledge\n")
		for _, m := range rel {
			if !add(fmt.Sprintf("- [%s] (%s) %s: %s\n", short(m.ID), m.Kind, m.Title, oneLine(m.Body, 500))) {
				break
			}
			pack.Relevant = append(pack.Relevant, Ref{ID: m.ID.String(), Kind: m.Kind, Scope: m.Scope, Title: m.Title})
		}
	}
	if summary != "" {
		add("\n### Project\n" + summary + "\n")
	}
	pack.Text = strings.TrimSpace(b.String())
	return pack, nil
}

func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > max {
		s = s[:max] + "…"
	}
	return s
}

// projectSummary is a few lines about the project's repositories and stack.
func (s *Service) projectSummary(ctx context.Context, projectID uuid.UUID) string {
	p, err := s.db.GetProject(ctx, projectID)
	if err != nil {
		return ""
	}
	var b strings.Builder
	b.WriteString(p.Project.Name)
	if d := strings.TrimSpace(p.Project.Description); d != "" {
		b.WriteString(" — " + oneLine(d, 300))
	}
	b.WriteByte('\n')
	repos, _ := s.db.ListRepositories(ctx, projectID)
	if len(repos) > 0 {
		var parts []string
		for _, r := range repos {
			x := fmt.Sprintf("%s (%s", r.Name, r.Role)
			if r.IndexedSha != nil {
				x += ", indexed at " + (*r.IndexedSha)[:min(7, len(*r.IndexedSha))]
			}
			parts = append(parts, x+")")
		}
		b.WriteString("Repositories: " + strings.Join(parts, ", ") + "\n")
	}
	deps, _ := s.db.ListProjectDependencies(ctx, dbgen.ListProjectDependenciesParams{ProjectID: projectID})
	var names []string
	for _, d := range deps {
		if !d.Dev && len(names) < 25 {
			names = append(names, d.Name)
		}
	}
	if len(names) > 0 {
		b.WriteString("Key dependencies: " + strings.Join(names, ", ") + "\n")
	}
	if len(repos) > 0 {
		b.WriteString("Use search_context / find_symbol to locate code and read_file to read it before relying on it.")
	}
	return strings.TrimSpace(b.String())
}

// ForRun returns the run's frozen context, building and storing it on first use.
// Agents in the skip list (e.g. the memory agent itself) get none.
func (s *Service) ForRun(ctx context.Context, run dbgen.AgentRun) (Pack, error) {
	if len(run.Context) > 0 {
		var p Pack
		err := json.Unmarshal(run.Context, &p)
		return p, err
	}
	if extractorAgents[run.AgentName] {
		return Pack{}, nil // the agents that produce memory never get it
	}
	for _, a := range s.Settings().SkipAgents {
		if a == run.AgentName {
			return Pack{}, nil
		}
	}
	pack, err := s.BuildPackFor(ctx, run.AgentName, run.ProjectID, run.Input)
	if err != nil {
		return Pack{}, err
	}
	raw, _ := json.Marshal(pack)
	if err := s.db.SetAgentRunContext(ctx, dbgen.SetAgentRunContextParams{ID: run.ID, Context: raw}); err != nil {
		return Pack{}, err
	}
	return pack, nil
}

// EmbedPending embeds memories lacking a vector from the current model.
func (s *Service) EmbedPending(ctx context.Context) (int, error) {
	if s.emb == nil || !s.emb.Embedding().Available {
		return 0, nil
	}
	key := s.emb.Embedding().Key
	rows, err := s.db.PendingMemoryEmbeddings(ctx, dbgen.PendingMemoryEmbeddingsParams{Model: key, RowLimit: 32})
	if err != nil || len(rows) == 0 {
		return 0, err
	}
	texts := make([]string, len(rows))
	for i, r := range rows {
		texts[i] = r.Title + "\n" + r.Body
	}
	vecs, model, err := s.emb.Embed(ctx, "memory", texts)
	if err != nil {
		return 0, err
	}
	for i, r := range rows {
		if err := s.db.SetMemoryEmbedding(ctx, dbgen.SetMemoryEmbeddingParams{Vec: vecText(vecs[i]), Model: &model, ID: r.ID, Version: r.Version}); err != nil {
			return i, err
		}
	}
	return len(rows), nil
}

// RunBackground embeds memories and processes finished extractions until ctx is done.
func (s *Service) RunBackground(ctx context.Context) {
	wait := 20 * time.Second
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = 30 * time.Second
		if n, err := s.EmbedPending(ctx); err != nil {
			s.log.Warn("embed memories", "err", err)
			wait = 2 * time.Minute
		} else if n > 0 {
			wait = time.Second
		}
		if err := s.Sweep(ctx); err != nil {
			s.log.Warn("memory extraction sweep", "err", err)
		}
	}
}

// Search finds approved memories (the developer's and the project's) for a query.
func (s *Service) Search(ctx context.Context, projectID *uuid.UUID, query string, k int) ([]dbgen.Memory, error) {
	rules, err := s.db.ActiveRules(ctx, projectID)
	if err != nil {
		return nil, err
	}
	rel, _, err := s.relevant(ctx, projectID, query, k)
	if err != nil {
		return nil, err
	}
	// Rules matching the query first, then the rest of the relevant memories.
	q := strings.ToLower(query)
	var out []dbgen.Memory
	for _, r := range rules {
		for _, w := range strings.Fields(nonWord.ReplaceAllString(q, " ")) {
			if len(w) > 3 && strings.Contains(strings.ToLower(r.Title+" "+r.Body), w) {
				out = append(out, r)
				break
			}
		}
	}
	return append(out, rel...), nil
}
