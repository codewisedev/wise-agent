// Package codeindex stores and searches the code index: files, AST symbols,
// text chunks and dependencies per repository, each tied to a commit SHA. The
// indexing itself runs in a separate worker (apps/agent-runtime, "indexer")
// that has no database access; it claims jobs and uploads results here.
package codeindex

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"sigs.k8s.io/yaml"

	"wise/apps/api/internal/apperr"
	"wise/apps/api/internal/audit"
	"wise/apps/api/internal/db"
	"wise/apps/api/internal/db/dbgen"
	"wise/apps/api/internal/repositories"
)

// Settings is the panel-managed "indexing" setting.
type Settings struct {
	Exclude              []string `json:"exclude"`
	Include              []string `json:"include"`
	Docs                 []string `json:"docs"`
	MaxFileKB            int      `json:"max_file_kb"`
	MaxFiles             int      `json:"max_files"`
	ChunkLines           int      `json:"chunk_lines"`
	AutoIndex            bool     `json:"auto_index"`
	CheckIntervalMinutes int      `json:"check_interval_minutes"`
	SecretPatterns       []string `json:"secret_patterns"`
}

// ParseSettings decodes and validates the indexing setting (YAML or JSON).
func ParseSettings(raw []byte) (Settings, error) {
	var s Settings
	if err := yaml.UnmarshalStrict(raw, &s); err != nil {
		return s, fmt.Errorf("parse: %w", err)
	}
	switch {
	case s.MaxFileKB < 1 || s.MaxFileKB > 4096:
		return s, fmt.Errorf("max_file_kb must be 1-4096")
	case s.MaxFiles < 1 || s.MaxFiles > 200000:
		return s, fmt.Errorf("max_files must be 1-200000")
	case s.ChunkLines < 10 || s.ChunkLines > 400:
		return s, fmt.Errorf("chunk_lines must be 10-400")
	case s.CheckIntervalMinutes < 1 || s.CheckIntervalMinutes > 1440:
		return s, fmt.Errorf("check_interval_minutes must be 1-1440")
	}
	for _, p := range s.SecretPatterns {
		if _, err := regexp.Compile(p); err != nil {
			return s, fmt.Errorf("secret pattern %q: %w", p, err)
		}
	}
	for _, list := range [][]string{s.Exclude, s.Include, s.Docs} {
		for _, g := range list {
			if strings.TrimSpace(g) == "" || len(g) > 300 {
				return s, fmt.Errorf("glob patterns must be 1-300 characters")
			}
		}
	}
	if s.Exclude == nil {
		s.Exclude = []string{}
	}
	if s.Include == nil {
		s.Include = []string{}
	}
	if s.Docs == nil {
		s.Docs = []string{}
	}
	if s.SecretPatterns == nil {
		s.SecretPatterns = []string{}
	}
	return s, nil
}

type Service struct {
	db    *db.DB
	repos *repositories.Service
	log   *slog.Logger
	lease time.Duration

	mu       sync.RWMutex
	settings Settings

	emb        Embedder
	embedState embedState
}

func NewService(d *db.DB, repos *repositories.Service, log *slog.Logger) *Service {
	return &Service{db: d, repos: repos, log: log, lease: 2 * time.Minute}
}

// SetSettings applies the panel-managed indexing settings.
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

func (s *Service) LeaseSeconds() int { return int(s.lease.Seconds()) }

const jobTokenPrefix = "wise_ij_"

func newJobToken() (string, []byte, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", nil, err
	}
	tok := jobTokenPrefix + base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(tok))
	return tok, sum[:], nil
}

// Claim is everything the indexer needs for one job.
type Claim struct {
	Job      dbgen.IndexJob
	Token    string
	Repo     dbgen.Repository
	Settings Settings
	Manifest map[string]string // path -> content hash of what is stored
}

// Claim takes the oldest queued (or lease-expired) job; nil when there is none.
func (s *Service) Claim(ctx context.Context, runtimeID string) (*Claim, error) {
	token, hash, err := newJobToken()
	if err != nil {
		return nil, err
	}
	var c *Claim
	err = s.db.InTx(ctx, func(q *dbgen.Queries) error {
		job, err := q.ClaimIndexJob(ctx, dbgen.ClaimIndexJobParams{
			RuntimeID: &runtimeID, TokenHash: hash, LeaseExpiresAt: ptr(time.Now().Add(s.lease)),
		})
		if db.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		repo, err := q.GetRepository(ctx, job.RepositoryID)
		if err != nil {
			return err
		}
		if err := q.SetRepositoryIndexing(ctx, dbgen.SetRepositoryIndexingParams{ID: repo.ID, Status: dbgen.RepositoryIndexStatusIndexing}); err != nil {
			return err
		}
		rows, err := q.ListCodeFileManifest(ctx, repo.ID)
		if err != nil {
			return err
		}
		manifest := make(map[string]string, len(rows))
		for _, r := range rows {
			manifest[r.Path] = r.ContentHash
		}
		c = &Claim{Job: job, Token: token, Repo: repo, Settings: s.Settings(), Manifest: manifest}
		return nil
	})
	return c, err
}

// Authenticate resolves a job token to its running job.
func (s *Service) Authenticate(ctx context.Context, token string) (dbgen.IndexJob, error) {
	if !strings.HasPrefix(token, jobTokenPrefix) {
		return dbgen.IndexJob{}, apperr.Unauthorized("invalid index job token")
	}
	sum := sha256.Sum256([]byte(token))
	job, err := s.db.GetRunningIndexJobByToken(ctx, sum[:])
	if db.IsNotFound(err) {
		return job, apperr.Unauthorized("index job token invalid or job no longer running")
	}
	return job, err
}

// Heartbeat extends the job's lease.
func (s *Service) Heartbeat(ctx context.Context, job dbgen.IndexJob) error {
	return s.db.ExtendIndexJobLease(ctx, dbgen.ExtendIndexJobLeaseParams{ID: job.ID, LeaseExpiresAt: ptr(time.Now().Add(s.lease))})
}

// Symbol, Chunk and File are what the indexer uploads.
type Symbol struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Signature string `json:"signature"`
	Parent    string `json:"parent"`
	Exported  bool   `json:"exported"`
	StartLine int    `json:"startLine"`
	EndLine   int    `json:"endLine"`
	Doc       string `json:"doc"`
}

type Chunk struct {
	Kind      string `json:"kind"`
	StartLine int    `json:"startLine"`
	EndLine   int    `json:"endLine"`
	Heading   string `json:"heading"`
	Content   string `json:"content"`
}

type File struct {
	Path          string   `json:"path"`
	Language      string   `json:"language"`
	Size          int      `json:"size"`
	ContentHash   string   `json:"contentHash"`
	Lines         int      `json:"lines"`
	Imports       []string `json:"imports"`
	SkippedReason string   `json:"skippedReason"`
	Symbols       []Symbol `json:"symbols"`
	Chunks        []Chunk  `json:"chunks"`
}

type Dependency struct {
	Ecosystem string `json:"ecosystem"`
	Name      string `json:"name"`
	Version   string `json:"version"`
	Dev       bool   `json:"dev"`
	Manifest  string `json:"manifest"`
}

// Upload limits (the indexer batches below them).
const (
	MaxFilesPerBatch  = 200
	MaxChunksPerBatch = 5000
	MaxChunkBytes     = 6000
	MaxSymbolsPerFile = 5000
)

var chunkKinds = map[string]bool{"code": true, "doc": true, "config": true}
var skipReasons = map[string]bool{"": true, "secret": true, "too_large": true, "binary": true, "unreadable": true}

func clean(s string, max int) string {
	s = strings.ToValidUTF8(strings.ReplaceAll(s, "\x00", ""), "")
	if len(s) > max {
		s = s[:max]
		for !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
	}
	return s
}

func validPath(p string) bool {
	return p != "" && len(p) <= 1024 && !strings.HasPrefix(p, "/") && !strings.Contains(p, "\x00") &&
		!strings.HasPrefix(p, "../") && !strings.Contains(p, "/../") && p != ".."
}

// Upload stores a batch of (re)indexed files, replacing their symbols and
// chunks, and removes deleted paths.
func (s *Service) Upload(ctx context.Context, job dbgen.IndexJob, files []File, deleted []string) error {
	if len(files) > MaxFilesPerBatch {
		return apperr.Validation(apperr.FieldError{Field: "files", Message: fmt.Sprintf("at most %d files per batch", MaxFilesPerBatch)})
	}
	total := 0
	for i, f := range files {
		if !validPath(f.Path) {
			return apperr.Validation(apperr.FieldError{Field: fmt.Sprintf("files[%d].path", i), Message: "must be a relative path inside the repository"})
		}
		if !skipReasons[f.SkippedReason] {
			return apperr.Validation(apperr.FieldError{Field: fmt.Sprintf("files[%d].skippedReason", i), Message: "unknown reason"})
		}
		if len(f.Symbols) > MaxSymbolsPerFile {
			return apperr.Validation(apperr.FieldError{Field: fmt.Sprintf("files[%d].symbols", i), Message: "too many symbols"})
		}
		for _, c := range f.Chunks {
			if !chunkKinds[c.Kind] {
				return apperr.Validation(apperr.FieldError{Field: fmt.Sprintf("files[%d].chunks", i), Message: "kind must be code, doc or config"})
			}
		}
		if f.SkippedReason != "" && len(f.Chunks)+len(f.Symbols) > 0 {
			return apperr.Validation(apperr.FieldError{Field: fmt.Sprintf("files[%d]", i), Message: "a skipped file carries no symbols or chunks"})
		}
		total += len(f.Chunks)
	}
	if total > MaxChunksPerBatch {
		return apperr.Validation(apperr.FieldError{Field: "files", Message: fmt.Sprintf("at most %d chunks per batch", MaxChunksPerBatch)})
	}
	for _, p := range deleted {
		if !validPath(p) {
			return apperr.Validation(apperr.FieldError{Field: "deleted", Message: "must be relative paths"})
		}
	}
	return s.db.InTx(ctx, func(q *dbgen.Queries) error {
		repo := job.RepositoryID
		for _, f := range files {
			fid, err := uuid.NewV7()
			if err != nil {
				return err
			}
			imports := f.Imports
			if imports == nil {
				imports = []string{}
			}
			if len(imports) > 500 {
				imports = imports[:500]
			}
			for i := range imports {
				imports[i] = clean(imports[i], 300)
			}
			var skipped *string
			if f.SkippedReason != "" {
				skipped = &f.SkippedReason
			}
			id, err := q.UpsertCodeFile(ctx, dbgen.UpsertCodeFileParams{
				ID: fid, RepositoryID: repo, Path: f.Path, Language: clean(f.Language, 40), SizeBytes: int32(f.Size),
				ContentHash: clean(f.ContentHash, 128), LineCount: int32(f.Lines), Imports: imports, SkippedReason: skipped,
			})
			if err != nil {
				return err
			}
			if err := q.DeleteFileSymbols(ctx, id); err != nil {
				return err
			}
			if err := q.DeleteFileChunks(ctx, id); err != nil {
				return err
			}
			syms := make([]dbgen.InsertCodeSymbolsParams, 0, len(f.Symbols))
			for _, sym := range f.Symbols {
				sid, err := uuid.NewV7()
				if err != nil {
					return err
				}
				var parent *string
				if sym.Parent != "" {
					p := clean(sym.Parent, 200)
					parent = &p
				}
				syms = append(syms, dbgen.InsertCodeSymbolsParams{
					ID: sid, FileID: id, RepositoryID: repo, Name: clean(sym.Name, 200), Kind: clean(sym.Kind, 30),
					Signature: clean(sym.Signature, 500), Parent: parent, Exported: sym.Exported,
					StartLine: int32(sym.StartLine), EndLine: int32(max(sym.EndLine, sym.StartLine)), Doc: clean(sym.Doc, 1000),
				})
			}
			if len(syms) > 0 {
				if _, err := q.InsertCodeSymbols(ctx, syms); err != nil {
					return err
				}
			}
			chunks := make([]dbgen.InsertCodeChunksParams, 0, len(f.Chunks))
			for _, c := range f.Chunks {
				cid, err := uuid.NewV7()
				if err != nil {
					return err
				}
				content := clean(c.Content, MaxChunkBytes)
				heading := clean(c.Heading, 300)
				sum := sha256.Sum256([]byte(heading + "\x00" + content))
				chunks = append(chunks, dbgen.InsertCodeChunksParams{
					ID: cid, FileID: id, RepositoryID: repo, Kind: c.Kind, StartLine: int32(c.StartLine),
					EndLine: int32(max(c.EndLine, c.StartLine)), Heading: heading, Content: content,
					SearchHeading: SearchText(heading), SearchText: SearchText(f.Path + " " + content),
					ContentHash: base64.RawURLEncoding.EncodeToString(sum[:]),
				})
			}
			if len(chunks) > 0 {
				if _, err := q.InsertCodeChunks(ctx, chunks); err != nil {
					return err
				}
			}
		}
		if len(deleted) > 0 {
			if _, err := q.DeleteCodeFiles(ctx, dbgen.DeleteCodeFilesParams{RepositoryID: repo, Paths: deleted}); err != nil {
				return err
			}
		}
		return q.ExtendIndexJobLease(ctx, dbgen.ExtendIndexJobLeaseParams{ID: job.ID, LeaseExpiresAt: ptr(time.Now().Add(s.lease))})
	})
}

// Complete records the dependencies and marks the repository indexed at commitSHA.
func (s *Service) Complete(ctx context.Context, job dbgen.IndexJob, commitSHA string, deps []Dependency, stats map[string]any) error {
	if len(deps) > 5000 {
		return apperr.Validation(apperr.FieldError{Field: "dependencies", Message: "at most 5000"})
	}
	return s.db.InTx(ctx, func(q *dbgen.Queries) error {
		if err := q.DeleteRepoDependencies(ctx, job.RepositoryID); err != nil {
			return err
		}
		seen := map[string]bool{}
		var rows []dbgen.InsertRepoDependenciesParams
		for _, d := range deps {
			key := d.Ecosystem + "\x00" + d.Name + "\x00" + d.Manifest
			if d.Name == "" || seen[key] {
				continue
			}
			seen[key] = true
			id, err := uuid.NewV7()
			if err != nil {
				return err
			}
			rows = append(rows, dbgen.InsertRepoDependenciesParams{ID: id, RepositoryID: job.RepositoryID,
				Ecosystem: clean(d.Ecosystem, 20), Name: clean(d.Name, 200), Version: clean(d.Version, 100), Dev: d.Dev, Manifest: clean(d.Manifest, 300)})
		}
		if len(rows) > 0 {
			if _, err := q.InsertRepoDependencies(ctx, rows); err != nil {
				return err
			}
		}
		counts, err := q.CountIndex(ctx, job.RepositoryID)
		if err != nil {
			return err
		}
		if stats == nil {
			stats = map[string]any{}
		}
		summary := map[string]any{"files": counts.Files, "skipped": counts.Skipped, "symbols": counts.Symbols,
			"chunks": counts.Chunks, "dependencies": counts.Dependencies}
		for k, v := range summary {
			stats[k] = v
		}
		raw, _ := json.Marshal(stats)
		sha := clean(commitSHA, 64)
		var shaPtr *string
		if sha != "" {
			shaPtr = &sha
		}
		if _, err := q.FinishIndexJob(ctx, dbgen.FinishIndexJobParams{ID: job.ID, Status: "done", CommitSha: shaPtr, Stats: raw}); err != nil {
			return err
		}
		sraw, _ := json.Marshal(summary)
		if err := q.SetRepositoryIndexed(ctx, dbgen.SetRepositoryIndexedParams{ID: job.RepositoryID, IndexedSha: shaPtr, IndexStats: sraw}); err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{Action: "repository.indexed", EntityType: "repository", EntityID: audit.Ptr(job.RepositoryID),
			Payload: map[string]any{"jobId": job.ID, "commit": sha, "stats": summary}})
	})
}

// Fail records a failed job; the repository shows the error.
func (s *Service) Fail(ctx context.Context, job dbgen.IndexJob, msg string) error {
	msg = clean(msg, 2000)
	return s.db.InTx(ctx, func(q *dbgen.Queries) error {
		if _, err := q.FinishIndexJob(ctx, dbgen.FinishIndexJobParams{ID: job.ID, Status: "failed", Stats: []byte("{}"), Error: &msg}); err != nil {
			return err
		}
		return q.SetRepositoryIndexing(ctx, dbgen.SetRepositoryIndexingParams{ID: job.RepositoryID, Status: dbgen.RepositoryIndexStatusFailed, Error: &msg})
	})
}

// Jobs lists a repository's recent index jobs.
func (s *Service) Jobs(ctx context.Context, repoID uuid.UUID) ([]dbgen.IndexJob, error) {
	return s.db.ListIndexJobs(ctx, dbgen.ListIndexJobsParams{RepositoryID: repoID, RowLimit: 10})
}

// RunScheduler refreshes local repositories' git state and queues an index job
// when HEAD moved since the last index (auto-index on).
func (s *Service) RunScheduler(ctx context.Context) {
	wait := 20 * time.Second // first pass soon after start: refresh git state, catch up on missed commits
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = time.Duration(max(s.Settings().CheckIntervalMinutes, 1)) * time.Minute
		if err := s.Tick(ctx); err != nil {
			s.log.Warn("index scheduler", "err", err)
		}
	}
}

// Tick runs one scheduler pass.
func (s *Service) Tick(ctx context.Context) error {
	if !s.Settings().AutoIndex {
		return nil
	}
	repos, err := s.db.ListLocalRepositories(ctx)
	if err != nil {
		return err
	}
	for _, r := range repos {
		if _, err := s.repos.Inspect(ctx, r.ID); err != nil {
			s.log.Debug("inspect repository", "repo", r.ID, "err", err)
		}
	}
	ids, err := s.db.ListReposNeedingIndex(ctx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := s.db.InTx(ctx, func(q *dbgen.Queries) error {
			return repositories.QueueIndexJob(ctx, q, id, "head_changed")
		}); err != nil {
			return err
		}
	}
	return nil
}

var camel = regexp.MustCompile(`([a-z0-9])([A-Z])|([A-Z]+)([A-Z][a-z])`)

// SearchText splits identifiers (camelCase, snake_case, kebab-case, paths) so
// full-text search finds "gateway reconfigure" in ReconfigureGateway.
func SearchText(s string) string {
	var b strings.Builder
	b.Grow(len(s) * 2)
	word := func(w string) {
		if w == "" {
			return
		}
		b.WriteString(strings.ToLower(w))
		b.WriteByte(' ')
		split := camel.ReplaceAllString(w, "${1}${3} ${2}${4}")
		split = strings.NewReplacer("_", " ", "-", " ", ".", " ", "/", " ").Replace(split)
		if lower := strings.ToLower(split); lower != strings.ToLower(w) {
			b.WriteString(lower)
			b.WriteByte(' ')
		}
	}
	start := -1
	for i, r := range s {
		ok := unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' || r == '.' || r == '/'
		if ok && start < 0 {
			start = i
		} else if !ok && start >= 0 {
			word(s[start:i])
			start = -1
		}
	}
	if start >= 0 {
		word(s[start:])
	}
	return b.String()
}

func ptr[T any](v T) *T { return &v }
