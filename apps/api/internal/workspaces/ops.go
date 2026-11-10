package workspaces

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"wise/apps/api/internal/apperr"
	"wise/apps/api/internal/audit"
	"wise/apps/api/internal/db"
	"wise/apps/api/internal/db/dbgen"
	"wise/apps/api/internal/executor"
)

// Ops is the part of the executor client used for work in a workspace.
type Ops interface {
	FS(ctx context.Context, id string, req any) (json.RawMessage, error)
	Command(ctx context.Context, id, command string, timeoutS int) (string, error)
	Diff(ctx context.Context, id string, maxBytes int) (executor.WorkingDiff, error)
	Snapshot(ctx context.Context, id string, req executor.SnapshotRequest) (executor.Changeset, error)
	Deliver(ctx context.Context, id string, req executor.DeliverRequest) (executor.Delivery, error)
}

func (s *Service) ops() (Ops, error) {
	if err := s.available(); err != nil {
		return nil, err
	}
	return s.exec, nil
}

// ForRun returns the ready workspace of the task a run works on (runs that
// belong to no workflow have none).
func (s *Service) ForRun(ctx context.Context, run dbgen.AgentRun) (*dbgen.Workspace, error) {
	if run.WorkflowRunID == nil {
		return nil, nil
	}
	w, err := s.db.WorkspaceForWorkflowRun(ctx, *run.WorkflowRunID)
	if db.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &w, nil
}

// fsOps are the file operations agents may request, and whether each writes.
var fsOps = map[string]bool{"list": false, "read": false, "search": false, "write": true, "edit": true, "delete": true}

// FS runs a file operation in the workspace. canWrite says whether the
// caller may change files.
func (s *Service) FS(ctx context.Context, w dbgen.Workspace, req map[string]any, canWrite bool) (json.RawMessage, error) {
	o, err := s.ops()
	if err != nil {
		return nil, err
	}
	if w.Status != "ready" && w.Status != "stopped" {
		return nil, apperr.Conflict("the workspace is " + w.Status)
	}
	op, _ := req["op"].(string)
	writes, known := fsOps[op]
	if !known {
		return nil, apperr.Validation(apperr.FieldError{Field: "op", Message: "one of list, read, search, write, edit, delete"})
	}
	if writes && !canWrite {
		return nil, apperr.Forbidden("this agent may not change files")
	}
	if _, ok := req["maxBytes"]; !ok {
		req["maxBytes"] = s.Settings().MaxOutputKB << 10
	}
	return o.FS(ctx, w.ID.String(), req)
}

// CommandResult is a finished command as an agent sees it.
type CommandResult struct {
	ExitCode   *int   `json:"exitCode"`
	Status     string `json:"status"`
	Output     string `json:"output"`
	Truncated  bool   `json:"truncated,omitempty"`
	TimedOut   bool   `json:"timedOut,omitempty"`
	DurationMs int64  `json:"durationMs"`
}

// Command runs a shell command in the workspace's sandbox and waits for it.
// The output kept is the start and the end, within max_output_kb.
func (s *Service) Command(ctx context.Context, w dbgen.Workspace, command string, timeoutS int) (CommandResult, error) {
	return s.CommandBeat(ctx, w, command, timeoutS, 0, nil)
}

// CommandBeat is Command with an output budget (0 = the setting) and a
// heartbeat called while waiting.
func (s *Service) CommandBeat(ctx context.Context, w dbgen.Workspace, command string, timeoutS, maxOutput int, beat func()) (CommandResult, error) {
	o, err := s.ops()
	if err != nil {
		return CommandResult{}, err
	}
	if w.Status != "ready" && w.Status != "stopped" {
		return CommandResult{}, apperr.Conflict("the workspace is " + w.Status)
	}
	st := s.Settings()
	if timeoutS <= 0 || timeoutS > st.CommandTimeoutS {
		timeoutS = st.CommandTimeoutS
	}
	start := time.Now()
	job, err := o.Command(ctx, w.ID.String(), command, timeoutS)
	if err != nil {
		return CommandResult{}, err
	}
	if maxOutput <= 0 {
		maxOutput = st.MaxOutputKB << 10
	}
	out := newHeadTail(maxOutput)
	res, err := s.exec.Wait(ctx, job, func(chunk string) { out.add(chunk) }, beat)
	if err != nil {
		return CommandResult{}, err
	}
	r := CommandResult{ExitCode: res.ExitCode, Status: res.Status, DurationMs: time.Since(start).Milliseconds()}
	r.Output, r.Truncated = out.text()
	r.TimedOut = res.ExitCode != nil && *res.ExitCode == 124 && strings.Contains(r.Output, "[wise] command timed out")
	return r, nil
}

// headTail keeps the first quarter and the last three quarters of a stream.
type headTail struct {
	max     int
	head    strings.Builder
	tail    []byte
	dropped int
}

func newHeadTail(max int) *headTail { return &headTail{max: max} }

func (h *headTail) add(s string) {
	if room := h.max/4 - h.head.Len(); room > 0 {
		n := min(room, len(s))
		h.head.WriteString(s[:n])
		s = s[n:]
	}
	h.tail = append(h.tail, s...)
	if over := len(h.tail) - (h.max - h.max/4); over > 0 {
		h.tail = h.tail[over:]
		h.dropped += over
	}
}

func (h *headTail) text() (string, bool) {
	if h.dropped == 0 {
		return h.head.String() + string(h.tail), false
	}
	return fmt.Sprintf("%s\n… [%d bytes of output omitted] …\n%s", h.head.String(), h.dropped, h.tail), true
}

// Diff returns the workspace's current changes (for agents).
func (s *Service) Diff(ctx context.Context, w dbgen.Workspace) (executor.WorkingDiff, error) {
	o, err := s.ops()
	if err != nil {
		return executor.WorkingDiff{}, err
	}
	return o.Diff(ctx, w.ID.String(), max(s.Settings().MaxOutputKB<<10, 64<<10)*2)
}

// Snapshot commits the workspace's changes as one verified commit.
func (s *Service) Snapshot(ctx context.Context, w dbgen.Workspace, message string) (executor.Changeset, error) {
	o, err := s.ops()
	if err != nil {
		return executor.Changeset{}, err
	}
	repo, err := s.db.GetRepository(ctx, w.RepositoryID)
	if err != nil {
		return executor.Changeset{}, err
	}
	a := s.CommitAuthor(ctx, repo)
	cs, err := o.Snapshot(ctx, w.ID.String(), executor.SnapshotRequest{Message: message, AuthorName: a.Name, AuthorEmail: a.Email})
	if err != nil {
		return cs, err
	}
	var head *string
	if cs.HeadSHA != "" {
		head = &cs.HeadSHA
	}
	return cs, s.db.SetWorkspaceHead(ctx, dbgen.SetWorkspaceHeadParams{ID: w.ID, HeadSha: head})
}

// StopSandbox stops a workspace's sandbox (its files stay).
func (s *Service) StopSandbox(ctx context.Context, w dbgen.Workspace) error {
	if err := s.available(); err != nil {
		return err
	}
	if err := s.exec.Stop(ctx, w.ID.String()); err != nil {
		return err
	}
	_, err := s.db.SetWorkspaceStatus(ctx, dbgen.SetWorkspaceStatusParams{ID: w.ID, Status: "stopped"})
	return err
}

// Deliver writes an approved commit into the user's repository as the
// task's branch.
func (s *Service) Deliver(ctx context.Context, w dbgen.Workspace, sha string, approvalID *uuid.UUID) (dbgen.Workspace, error) {
	o, err := s.ops()
	if err != nil {
		return w, err
	}
	d, err := o.Deliver(ctx, w.ID.String(), executor.DeliverRequest{SHA: sha})
	if err != nil {
		return w, err
	}
	var out dbgen.Workspace
	err = s.db.InTx(ctx, func(q *dbgen.Queries) error {
		var err error
		out, err = q.SetWorkspaceDelivered(ctx, dbgen.SetWorkspaceDeliveredParams{ID: w.ID, DeliveredBranch: &d.Branch, DeliveredSha: &d.SHA})
		if err != nil {
			return err
		}
		tid := w.TaskID
		return audit.Record(ctx, q, audit.Entry{Action: "workspace.delivered", EntityType: "task", EntityID: &tid,
			Payload: map[string]any{"workspaceId": w.ID, "branch": d.Branch, "sha": d.SHA, "repository": d.RepoPath, "approvalId": approvalID}})
	})
	return out, err
}
