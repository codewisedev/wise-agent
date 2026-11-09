package memory

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"wise/apps/api/internal/agents"
	"wise/apps/api/internal/apperr"
	"wise/apps/api/internal/db"
	"wise/apps/api/internal/db/dbgen"
)

// ProfileAgent proposes engineering-profile preferences from indexed repositories.
const ProfileAgent = "profile-agent"

// inferredKinds are what inference may propose: it never creates rules.
var inferredKinds = map[string]bool{"preference": true, "convention": true, "example": true}

// extractorAgents never get memory context (they produce it), whatever the setting says.
var extractorAgents = map[string]bool{ExtractorAgent: true, ProfileAgent: true}

// InferProfile asks the profile agent to propose preferences from what the
// project's indexed repositories show (dependencies, tooling and lint
// configuration, test layout, naming). Proposals arrive in the memory inbox.
// One run per set of indexed commits: repeating it is free.
func (s *Service) InferProfile(ctx context.Context, projectID uuid.UUID, userID *uuid.UUID) (dbgen.AgentRun, error) {
	p, err := s.db.GetProject(ctx, projectID)
	if db.IsNotFound(err) {
		return dbgen.AgentRun{}, apperr.NotFound("project not found")
	}
	if err != nil {
		return dbgen.AgentRun{}, err
	}
	shas, err := s.db.ListProjectIndexedShas(ctx, projectID)
	if err != nil {
		return dbgen.AgentRun{}, err
	}
	if len(shas) == 0 {
		return dbgen.AgentRun{}, apperr.Conflict("no repository of this project is indexed yet; index one first")
	}
	key := "profile:" + projectID.String()
	for _, r := range shas {
		key += ":" + (*r.IndexedSha)[:min(12, len(*r.IndexedSha))]
	}

	var cfg strings.Builder
	chunks, err := s.db.ProfileConfigChunks(ctx, projectID)
	if err != nil {
		return dbgen.AgentRun{}, err
	}
	files, last := 0, ""
	for _, c := range chunks {
		name := c.RepositoryName + "/" + c.Path
		if name != last {
			if files == 10 || cfg.Len() > 24000 {
				break
			}
			files++
			last = name
			cfg.WriteString("\n### " + name + "\n")
		}
		cfg.WriteString(clip(c.Content, 3000) + "\n")
	}

	var layout strings.Builder
	if rows, err := s.db.ProfileTestLayout(ctx, projectID); err == nil {
		for _, r := range rows {
			layout.WriteString(fmt.Sprintf("- %s: %d files, %d look like tests\n", r.Language, r.Files, r.Tests))
		}
	}
	var naming strings.Builder
	if rows, err := s.db.ProfileNamingSamples(ctx, projectID); err == nil {
		for _, r := range rows {
			naming.WriteString(fmt.Sprintf("%s %s (%s); ", r.Kind, r.Name, r.Language))
		}
	}
	var deps []string
	if rows, err := s.db.ListProjectDependencies(ctx, dbgen.ListProjectDependenciesParams{ProjectID: projectID}); err == nil {
		for _, d := range rows {
			if len(deps) < 120 {
				tag := ""
				if d.Dev {
					tag = " (dev)"
				}
				deps = append(deps, fmt.Sprintf("%s %s %s%s", d.Ecosystem, d.Name, d.Version, tag))
			}
		}
	}
	existing := []map[string]string{}
	if rows, err := s.db.DeveloperProfile(ctx); err == nil {
		for _, m := range rows {
			existing = append(existing, map[string]string{"kind": m.Kind, "category": m.Category, "title": m.Title, "body": clip(m.Body, 300)})
		}
	}
	input := map[string]any{
		"project":           map[string]any{"name": p.Project.Name, "description": clip(p.Project.Description, 2000)},
		"dependencies":      deps,
		"configuration":     clip(cfg.String(), 30000),
		"test_layout":       layout.String(),
		"naming_samples":    clip(naming.String(), 6000),
		"existing_memories": existing,
	}
	run, err := s.agents.Enqueue(ctx, ProfileAgent, input, agentsRunOptions(&projectID, userID, key))
	if err != nil {
		return dbgen.AgentRun{}, err
	}
	if err := s.db.InsertMemoryExtraction(ctx, dbgen.InsertMemoryExtractionParams{
		RunID: run.ID, Source: "profile_inference", SourceRefs: []byte(fmt.Sprintf(`{"projectId":%q}`, projectID)), ProjectID: &projectID,
	}); err != nil && !db.IsUniqueViolation(err) {
		return dbgen.AgentRun{}, err
	}
	return run, nil
}

// downgradeInferred keeps inferred proposals from becoming rules (and other
// kinds inference does not make): the user decides what is mandatory.
func downgradeInferred(cands []Candidate) []Candidate {
	out := make([]Candidate, 0, len(cands))
	for _, c := range cands {
		if !inferredKinds[c.Kind] {
			c.Kind = "preference"
		}
		c.Scope = "developer" // an engineering profile describes the developer
		out = append(out, c)
	}
	return out
}

// ExampleInput marks part of a repository file as an approved example.
type ExampleInput struct {
	RepositoryID uuid.UUID
	Path         string
	StartLine    int
	EndLine      int
	Title        string
	Note         string
	Category     string
}

// FileReader reads repository files with the secret guard applied.
type FileReader interface {
	ReadFile(ctx context.Context, id uuid.UUID, rel string) ([]byte, int, bool, dbgen.Repository, error)
}

// CreateExample stores an approved example for the repository's project: the
// snippet (bounded) with its location and commit, so the context given to
// agents is useful even when they cannot open that repository.
func (s *Service) CreateExample(ctx context.Context, files FileReader, in ExampleInput, userID *uuid.UUID) (dbgen.Memory, error) {
	if in.StartLine < 1 || in.EndLine < in.StartLine {
		return dbgen.Memory{}, apperr.Validation(apperr.FieldError{Field: "lines", Message: "startLine must be ≥ 1 and endLine ≥ startLine"})
	}
	if in.EndLine-in.StartLine > 199 {
		return dbgen.Memory{}, apperr.Validation(apperr.FieldError{Field: "lines", Message: "at most 200 lines"})
	}
	content, _, _, repo, err := files.ReadFile(ctx, in.RepositoryID, in.Path)
	if err != nil {
		return dbgen.Memory{}, err // includes the secret guard
	}
	lines := strings.Split(string(content), "\n")
	if in.StartLine > len(lines) {
		return dbgen.Memory{}, apperr.Validation(apperr.FieldError{Field: "lines", Message: "past the end of the file"})
	}
	end := min(in.EndLine, len(lines))
	snippet := clip(strings.Join(lines[in.StartLine-1:end], "\n"), 4000)
	sha := ""
	if repo.HeadSha != nil {
		sha = (*repo.HeadSha)[:min(12, len(*repo.HeadSha))]
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		title = fmt.Sprintf("Example: %s", in.Path)
	}
	body := fmt.Sprintf("%s\n\n%s/%s lines %d-%d (commit %s):\n```\n%s\n```", strings.TrimSpace(in.Note), repo.Name, in.Path, in.StartLine, end, sha, snippet)
	cat := in.Category
	if cat == "" {
		cat = "coding"
	}
	mi := Input{Scope: "project", ProjectID: &repo.ProjectID, Kind: "example", Category: cat, Title: title, Body: strings.TrimSpace(body)}
	if err := mi.validate(); err != nil {
		return dbgen.Memory{}, err
	}
	refs := map[string]any{"repositoryId": repo.ID, "path": in.Path, "startLine": in.StartLine, "endLine": end, "commit": sha}
	return s.insert(ctx, mi, "active", "manual", refs, "", nil, userID)
}

func agentsRunOptions(projectID, userID *uuid.UUID, key string) agents.RunOptions {
	return agents.RunOptions{ProjectID: projectID, UserID: userID, IdempotencyKey: key}
}
