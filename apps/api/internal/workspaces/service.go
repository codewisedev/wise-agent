// Package workspaces manages task workspaces from the API side: which
// repository a task changes, its execution settings, preparing the
// workspace through the executor, and cleaning up.
package workspaces

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"wise/apps/api/internal/apperr"
	"wise/apps/api/internal/audit"
	"wise/apps/api/internal/db"
	"wise/apps/api/internal/db/dbgen"
	"wise/apps/api/internal/executor"
	"wise/apps/api/internal/secretguard"
)

// Executor is the part of the executor client the service uses.
type Executor interface {
	Prepare(ctx context.Context, req executor.PrepareRequest) (string, error)
	Wait(ctx context.Context, jobID string, onOutput func(string), beat func()) (executor.JobStatus, error)
	Remove(ctx context.Context, id string) error
	Stop(ctx context.Context, id string) error
	SetEgress(ctx context.Context, hosts []string) error
	Ops
}

// Service is the workspace service.
type Service struct {
	db   *db.DB
	exec Executor
	log  *slog.Logger

	mu       sync.RWMutex
	settings Settings

	trackMu  sync.Mutex
	tracking map[uuid.UUID]bool // preparations tracked by this process
}

// New returns a Service. exec may be nil (executor not configured).
func New(d *db.DB, exec Executor, log *slog.Logger) *Service {
	return &Service{db: d, exec: exec, log: log, tracking: map[uuid.UUID]bool{}}
}

// Settings returns the current execution settings.
func (s *Service) Settings() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.settings
}

// SetSettings applies new execution settings (and pushes the egress
// allowlist to the executor).
func (s *Service) SetSettings(ctx context.Context, st Settings) error {
	s.mu.Lock()
	s.settings = st
	s.mu.Unlock()
	if s.exec == nil {
		return nil
	}
	if err := s.exec.SetEgress(ctx, st.EgressAllowlist); err != nil {
		// The executor may still be starting; the sweep pushes it again.
		s.log.Warn("push egress allowlist", "err", err)
	}
	return nil
}

// Available reports whether the executor is configured.
func (s *Service) Available() bool { return s.exec != nil }

func (s *Service) available() error {
	if s.exec == nil {
		return apperr.Unavailable("the executor is not configured (set WISE_EXECUTOR_URL and WISE_EXECUTOR_TOKEN)")
	}
	return nil
}

// ---- repository execution settings ----

// RepoSettings returns a repository's execution settings.
func (s *Service) RepoSettings(ctx context.Context, repoID uuid.UUID) (RepoSettings, dbgen.Repository, error) {
	r, err := s.db.GetRepository(ctx, repoID)
	if db.IsNotFound(err) {
		return RepoSettings{}, r, apperr.NotFound("repository not found")
	}
	if err != nil {
		return RepoSettings{}, r, err
	}
	return RepoSettingsFrom(r.Settings), r, nil
}

// SetRepoSettings validates and stores a repository's execution settings.
func (s *Service) SetRepoSettings(ctx context.Context, repoID uuid.UUID, in RepoSettings) (RepoSettings, error) {
	if err := in.Validate(); err != nil {
		return RepoSettings{}, err
	}
	if in.Setup == nil {
		in.Setup = []string{}
	}
	if in.Tools == nil {
		in.Tools = []string{}
	}
	if in.Env == nil {
		in.Env = map[string]string{}
	}
	err := s.db.InTx(ctx, func(q *dbgen.Queries) error {
		r, err := q.GetRepository(ctx, repoID)
		if db.IsNotFound(err) {
			return apperr.NotFound("repository not found")
		}
		if err != nil {
			return err
		}
		all := map[string]any{}
		_ = json.Unmarshal(r.Settings, &all)
		all["execution"] = in
		raw, err := json.Marshal(all)
		if err != nil {
			return err
		}
		if _, err := q.SetRepositorySettings(ctx, dbgen.SetRepositorySettingsParams{ID: repoID, Settings: raw}); err != nil {
			return err
		}
		envNames := make([]string, 0, len(in.Env))
		for k := range in.Env {
			envNames = append(envNames, k)
		}
		return audit.Record(ctx, q, audit.Entry{Action: "repository.execution_updated", EntityType: "repository", EntityID: &repoID,
			Payload: map[string]any{"image": in.Image, "setup": in.Setup, "commands": in.Commands, "tools": in.Tools,
				"baseBranch": in.BaseBranch, "env": envNames}})
	})
	return in, err
}

// ---- target repository ----

// Task returns a task.
func (s *Service) Task(ctx context.Context, id uuid.UUID) (dbgen.Task, error) {
	t, err := s.db.GetTask(ctx, id)
	if db.IsNotFound(err) {
		return t, apperr.NotFound("task not found")
	}
	return t, err
}

// TargetRepository is the repository a task changes: the one chosen on the
// task, else the project's only local repository.
func (s *Service) TargetRepository(ctx context.Context, t dbgen.Task) (dbgen.Repository, error) {
	if t.TargetRepositoryID != nil {
		r, err := s.db.GetRepository(ctx, *t.TargetRepositoryID)
		if err == nil {
			return r, nil
		}
		if !db.IsNotFound(err) {
			return r, err
		}
	}
	repos, err := s.db.ListRepositories(ctx, t.ProjectID)
	if err != nil {
		return dbgen.Repository{}, err
	}
	var local []dbgen.Repository
	for _, r := range repos {
		if r.SourceType == dbgen.RepositorySourceTypeLocal {
			local = append(local, r)
		}
	}
	switch len(local) {
	case 0:
		return dbgen.Repository{}, apperr.Conflict("the project has no local repository to work in")
	case 1:
		return local[0], nil
	}
	return dbgen.Repository{}, apperr.Conflict("the project has several local repositories; choose the task's repository first")
}

// SetTarget chooses the repository a task changes (nil = automatic).
func (s *Service) SetTarget(ctx context.Context, taskID uuid.UUID, repoID *uuid.UUID) error {
	return s.db.InTx(ctx, func(q *dbgen.Queries) error {
		t, err := q.GetTask(ctx, taskID)
		if db.IsNotFound(err) {
			return apperr.NotFound("task not found")
		}
		if err != nil {
			return err
		}
		if repoID != nil {
			r, err := q.GetRepository(ctx, *repoID)
			if err != nil || r.ProjectID != t.ProjectID {
				return apperr.Validation(apperr.FieldError{Field: "repository", Message: "a repository of the task's project"})
			}
			if r.SourceType != dbgen.RepositorySourceTypeLocal {
				return apperr.Validation(apperr.FieldError{Field: "repository", Message: "Wise can only work in local repositories"})
			}
		}
		if err := q.SetTaskTargetRepository(ctx, dbgen.SetTaskTargetRepositoryParams{ID: taskID, TargetRepositoryID: repoID}); err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{Action: "task.repository_set", EntityType: "task", EntityID: &taskID,
			Payload: map[string]any{"repositoryId": repoID}})
	})
}

// ---- prepare ----

// ForTask returns the task's live workspace, or its most recent one.
func (s *Service) ForTask(ctx context.Context, taskID uuid.UUID) (*dbgen.Workspace, error) {
	w, err := s.db.LiveWorkspaceForTask(ctx, taskID)
	if err == nil {
		return &w, nil
	}
	if !db.IsNotFound(err) {
		return nil, err
	}
	all, err := s.db.ListTaskWorkspaces(ctx, taskID)
	if err != nil || len(all) == 0 {
		return nil, err
	}
	return &all[0], nil
}

// Get returns a workspace.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (dbgen.Workspace, error) {
	w, err := s.db.GetWorkspace(ctx, id)
	if db.IsNotFound(err) {
		return w, apperr.NotFound("workspace not found")
	}
	return w, err
}

// Start creates the task's workspace (or reuses its live one) and asks the
// executor to prepare it. Track waits for the preparation to finish.
func (s *Service) Start(ctx context.Context, taskID uuid.UUID, workflowRunID, userID *uuid.UUID) (dbgen.Workspace, error) {
	if err := s.available(); err != nil {
		return dbgen.Workspace{}, err
	}
	t, err := s.db.GetTask(ctx, taskID)
	if db.IsNotFound(err) {
		return dbgen.Workspace{}, apperr.NotFound("task not found")
	}
	if err != nil {
		return dbgen.Workspace{}, err
	}
	repo, err := s.TargetRepository(ctx, t)
	if err != nil {
		return dbgen.Workspace{}, err
	}
	rs := RepoSettingsFrom(repo.Settings)
	if rs.Image == "" {
		return dbgen.Workspace{}, apperr.Conflict(fmt.Sprintf("repository %s has no execution settings yet: open the project, choose the repository's Execution settings (Detect can fill them in) and save", repo.Name))
	}
	if repo.LocalPath == nil {
		return dbgen.Workspace{}, apperr.Conflict("Wise can only work in local repositories")
	}
	st := s.Settings()

	var w dbgen.Workspace
	live, err := s.db.LiveWorkspaceForTask(ctx, taskID)
	if err != nil && !db.IsNotFound(err) {
		return dbgen.Workspace{}, err
	}
	if err == nil {
		switch {
		case live.RepositoryID != repo.ID:
			// The task's repository changed: retire the old workspace.
			if err := s.remove(ctx, live); err != nil {
				return dbgen.Workspace{}, err
			}
		case live.Status == "ready" || live.Status == "preparing":
			return live, nil
		default: // stopped: preparing again just restarts its sandbox
			w = live
		}
	}
	if w.ID == uuid.Nil {
		id, _ := uuid.NewV7()
		ext := ""
		if t.ExternalID != nil {
			ext = *t.ExternalID
		}
		branch := BranchName(st.BranchTemplate, t.Title, strings.ReplaceAll(t.ID.String(), "-", "")[:8], ext)
		if _, ok := checkBranch(branch); !ok {
			branch = "wise/task-" + t.ID.String()[:8]
		}
		var base *string
		if rs.BaseBranch != "" {
			base = &rs.BaseBranch
		}
		w, err = s.db.InsertWorkspace(ctx, dbgen.InsertWorkspaceParams{ID: id, TaskID: taskID, WorkflowRunID: workflowRunID,
			RepositoryID: repo.ID, Branch: branch, BaseBranch: base, Image: rs.Image, CreatedBy: userID})
		if db.IsUniqueViolation(err) {
			return s.db.LiveWorkspaceForTask(ctx, taskID)
		}
		if err != nil {
			return dbgen.Workspace{}, err
		}
	}
	if err := s.exec.SetEgress(ctx, st.EgressAllowlist); err != nil {
		s.log.Warn("push egress allowlist", "err", err)
	}
	job, err := s.exec.Prepare(ctx, executor.PrepareRequest{
		ID: w.ID.String(), RepoPath: *repo.LocalPath, Base: rs.BaseBranch, Branch: w.Branch, Image: rs.Image, Env: rs.Env,
		Limits: executor.Limits{CPUs: st.Limits.CPUs, MemoryMB: st.Limits.MemoryMB, Pids: st.Limits.Pids},
		Tools:  rs.Tools, Setup: rs.Setup, SetupTimeout: st.SetupTimeoutS,
	})
	if err != nil {
		w2, _ := s.db.SetWorkspaceFailed(ctx, dbgen.SetWorkspaceFailedParams{ID: w.ID, Error: ptr(err.Error())})
		var ee *executor.Error
		if errors.As(err, &ee) && ee.Status == 400 {
			return w2, apperr.Validation(apperr.FieldError{Field: "execution", Message: ee.Msg})
		}
		return w2, err
	}
	if err := s.db.SetWorkspaceJob(ctx, dbgen.SetWorkspaceJobParams{ID: w.ID, JobID: &job}); err != nil {
		return w, err
	}
	w.JobID, w.Status = &job, "preparing"
	return w, nil
}

func ptr[T any](v T) *T { return &v }

// finish records a preparation's outcome unless the workspace moved on
// meanwhile (e.g. it was removed): then the current row is returned.
func (s *Service) finish(ctx context.Context, id uuid.UUID, w dbgen.Workspace, err error) (dbgen.Workspace, error) {
	if db.IsNotFound(err) {
		return s.db.GetWorkspace(ctx, id)
	}
	return w, err
}

// Track waits for a workspace's preparation and records the outcome.
func (s *Service) Track(ctx context.Context, w dbgen.Workspace, beat func()) (dbgen.Workspace, error) {
	if w.Status != "preparing" || w.JobID == nil {
		return w, nil
	}
	s.trackMu.Lock()
	s.tracking[w.ID] = true
	s.trackMu.Unlock()
	defer func() {
		s.trackMu.Lock()
		delete(s.tracking, w.ID)
		s.trackMu.Unlock()
	}()
	st, err := s.exec.Wait(ctx, *w.JobID, func(out string) {
		_ = s.db.AppendWorkspaceLog(ctx, dbgen.AppendWorkspaceLogParams{ID: w.ID, Chunk: secretguard.Redact(out)})
	}, beat)
	if err != nil {
		var ee *executor.Error
		if errors.As(err, &ee) && ee.Status == 404 {
			// The executor restarted and lost the job.
			r, err := s.db.SetWorkspaceFailed(ctx, dbgen.SetWorkspaceFailedParams{ID: w.ID, Error: ptr("the executor restarted during preparation; retry")})
			return s.finish(ctx, w.ID, r, err)
		}
		return w, err
	}
	if st.Status != executor.JobSucceeded {
		msg := st.Error
		if msg == "" {
			msg = "preparation " + st.Status
		}
		r, err := s.db.SetWorkspaceFailed(ctx, dbgen.SetWorkspaceFailedParams{ID: w.ID, Error: &msg})
		return s.finish(ctx, w.ID, r, err)
	}
	var res executor.State
	raw, _ := json.Marshal(st.Result)
	_ = json.Unmarshal(raw, &res)
	scrubbed := res.Scrubbed
	if scrubbed == nil {
		scrubbed = []string{}
	}
	tools := res.Tools
	if tools == nil {
		tools = []string{}
	}
	r, err := s.db.SetWorkspaceReady(ctx, dbgen.SetWorkspaceReadyParams{ID: w.ID, BaseBranch: &res.Base, BaseSha: &res.BaseSHA,
		ToolVersions: tools, Scrubbed: scrubbed})
	return s.finish(ctx, w.ID, r, err)
}

// trackInBackground tracks a preparation unless this process already does.
func (s *Service) trackInBackground(w dbgen.Workspace) {
	s.trackMu.Lock()
	busy := s.tracking[w.ID]
	s.trackMu.Unlock()
	if busy {
		return
	}
	go func() {
		bctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
		defer cancel()
		if _, err := s.Track(bctx, w, nil); err != nil {
			s.log.Warn("track workspace preparation", "workspace", w.ID, "err", err)
		}
	}()
}

// StartAsync starts preparing and tracks it in the background (for the
// dashboard; workflows track it in an activity).
func (s *Service) StartAsync(ctx context.Context, taskID uuid.UUID, userID *uuid.UUID) (dbgen.Workspace, error) {
	w, err := s.Start(ctx, taskID, nil, userID)
	if err != nil {
		return w, err
	}
	if w.Status == "preparing" {
		s.trackInBackground(w)
	}
	tid := taskID
	_ = s.db.InTx(ctx, func(q *dbgen.Queries) error {
		return audit.Record(ctx, q, audit.Entry{Action: "workspace.prepare", EntityType: "task", EntityID: &tid,
			Payload: map[string]any{"workspaceId": w.ID, "branch": w.Branch}})
	})
	return w, nil
}

// ---- remove and sweep ----

func (s *Service) remove(ctx context.Context, w dbgen.Workspace) error {
	if err := s.exec.Remove(ctx, w.ID.String()); err != nil {
		var ee *executor.Error
		if !errors.As(err, &ee) || ee.Status != 404 {
			return err
		}
	}
	_, err := s.db.SetWorkspaceStatus(ctx, dbgen.SetWorkspaceStatusParams{ID: w.ID, Status: "removed"})
	return err
}

// Remove deletes a task's workspace (its sandbox and files). A delivered
// branch stays in the repository.
func (s *Service) Remove(ctx context.Context, taskID uuid.UUID) error {
	if err := s.available(); err != nil {
		return err
	}
	w, err := s.db.LiveWorkspaceForTask(ctx, taskID)
	if db.IsNotFound(err) {
		return apperr.NotFound("the task has no workspace")
	}
	if err != nil {
		return err
	}
	if err := s.remove(ctx, w); err != nil {
		return err
	}
	tid := taskID
	return s.db.InTx(ctx, func(q *dbgen.Queries) error {
		return audit.Record(ctx, q, audit.Entry{Action: "workspace.removed", EntityType: "task", EntityID: &tid,
			Payload: map[string]any{"workspaceId": w.ID}})
	})
}

// Sweep removes expired workspaces and re-pushes the egress allowlist.
func (s *Service) Sweep(ctx context.Context) {
	if s.exec == nil {
		return
	}
	st := s.Settings()
	if err := s.exec.SetEgress(ctx, st.EgressAllowlist); err != nil {
		s.log.Debug("push egress allowlist", "err", err)
	}
	// Preparations nobody tracks (the API restarted meanwhile): resume.
	// Workflows track their own in an activity, which retries after a restart.
	if prep, err := s.db.ListPreparingWorkspaces(ctx); err == nil {
		for _, w := range prep {
			if w.WorkflowRunID == nil {
				s.trackInBackground(w)
			}
		}
	}
	rows, err := s.db.ListExpiredWorkspaces(ctx, int32(max(st.RetentionDays, 1)))
	if err != nil {
		s.log.Warn("list expired workspaces", "err", err)
		return
	}
	for _, w := range rows {
		if err := s.remove(ctx, w); err != nil {
			s.log.Warn("remove expired workspace", "workspace", w.ID, "err", err)
		}
	}
}

// RunSweeper sweeps every 10 minutes (first after a minute) until ctx is done.
func (s *Service) RunSweeper(ctx context.Context) {
	wait := time.Minute
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = 10 * time.Minute
		s.Sweep(ctx)
	}
}

// ---- commit identity ----

// CommitAuthor is the identity for task commits: the setting, else the
// repository's git identity, else Wise.
func (s *Service) CommitAuthor(ctx context.Context, repo dbgen.Repository) Author {
	if a := s.Settings().CommitAuthor; a.Name != "" {
		return a
	}
	if repo.LocalPath != nil {
		get := func(k string) string {
			out, err := exec.CommandContext(ctx, "git", "-C", *repo.LocalPath, "config", "--get", k).Output()
			if err != nil {
				return ""
			}
			return strings.TrimSpace(string(out))
		}
		if n, e := get("user.name"), get("user.email"); n != "" && strings.Contains(e, "@") {
			return Author{Name: n, Email: e}
		}
	}
	return Author{Name: "Wise", Email: "wise@localhost"}
}
