// Package executor is Wise's trusted execution worker. It clones a task's
// repository into a private workspace, runs a sandbox container for it, runs
// commands and file operations inside that sandbox, and delivers approved
// commits as branches into the user's repository.
//
// Ground rule: once an agent has run, the workspace is hostile. After the
// initial clone the executor never runs git or does file IO in a workspace;
// everything happens inside the sandbox, whose output is data. Commits leave
// the sandbox only as a git bundle, which is verified in an executor-private
// repository before anything reaches the user's repository.
package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"wise/apps/api/internal/secretguard"
)

// Config configures the executor.
type Config struct {
	Dir       string   // where workspaces live (the workspaces volume)
	RepoRoots []string // repositories may only come from here
	Helper    string   // path of the wise-sandbox binary to install in sandboxes
	User      string   // uid:gid sandboxes run as
	Proxy     string   // egress proxy URL given to sandboxes
}

// Manager owns workspaces, sandboxes and jobs.
type Manager struct {
	cfg  Config
	rt   Runtime
	log  *slog.Logger
	jobs *jobs

	mu    sync.Mutex
	locks map[string]*sync.Mutex

	egressMu sync.RWMutex
	egress   []string
}

// NewManager returns a Manager.
func NewManager(cfg Config, rt Runtime, log *slog.Logger) *Manager {
	m := &Manager{cfg: cfg, rt: rt, log: log, jobs: newJobs(), locks: map[string]*sync.Mutex{}}
	m.loadEgress()
	return m
}

func (m *Manager) lock(id string) func() {
	m.mu.Lock()
	l, ok := m.locks[id]
	if !ok {
		l = &sync.Mutex{}
		m.locks[id] = l
	}
	m.mu.Unlock()
	l.Lock()
	return l.Unlock
}

// ---- requests and state ----

// Limits bound a sandbox's resources.
type Limits struct {
	CPUs     float64 `json:"cpus"`
	MemoryMB int64   `json:"memoryMb"`
	Pids     int64   `json:"pids"`
}

// PrepareRequest creates a workspace for a task.
type PrepareRequest struct {
	ID           string            `json:"id"`
	RepoPath     string            `json:"repoPath"`
	Base         string            `json:"base,omitempty"` // branch to start from; empty = the repository's current branch
	Branch       string            `json:"branch"`
	Image        string            `json:"image"`
	Env          map[string]string `json:"env,omitempty"`
	Limits       Limits            `json:"limits"`
	Tools        []string          `json:"tools,omitempty"` // commands that must exist (preflight)
	Setup        []string          `json:"setup,omitempty"`
	SetupTimeout int               `json:"setupTimeoutS,omitempty"`
}

// State is a workspace's executor-private record.
type State struct {
	ID        string    `json:"id"`
	RepoPath  string    `json:"repoPath"`
	Branch    string    `json:"branch"`
	Base      string    `json:"base"`
	BaseSHA   string    `json:"baseSha"`
	Image     string    `json:"image"`
	Prepared  bool      `json:"prepared"`
	Tools     []string  `json:"toolVersions,omitempty"`
	Scrubbed  []string  `json:"scrubbed,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

// Info is a workspace as callers see it.
type Info struct {
	State
	Sandbox string `json:"sandbox"` // running | stopped | missing
}

var (
	idRe    = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	imageRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._/:@-]{0,254}$`)
	envRe   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
)

// ErrNotFound is returned for unknown workspaces and jobs.
var ErrNotFound = errors.New("not found")

// BadRequest marks invalid input.
type BadRequest struct{ Msg string }

func (e BadRequest) Error() string { return e.Msg }

func bad(format string, a ...any) error { return BadRequest{fmt.Sprintf(format, a...)} }

func (m *Manager) dir(id string) string   { return filepath.Join(m.cfg.Dir, id) }
func (m *Manager) state(id string) string { return filepath.Join(m.dir(id), "state.json") }
func (m *Manager) trusted(id string) string {
	return filepath.Join(m.dir(id), "trusted.git")
}

func (m *Manager) load(id string) (State, error) {
	if !idRe.MatchString(id) {
		return State{}, ErrNotFound
	}
	data, err := os.ReadFile(m.state(id))
	if errors.Is(err, fs.ErrNotExist) {
		return State{}, ErrNotFound
	}
	if err != nil {
		return State{}, err
	}
	var st State
	return st, json.Unmarshal(data, &st)
}

func (m *Manager) save(st State) error {
	data, _ := json.MarshalIndent(st, "", "  ")
	tmp := m.state(st.ID) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, m.state(st.ID))
}

// git runs git in a trusted directory (the user's repository, a fresh clone
// before any agent ran, or the executor-private verification repository).
func git(ctx context.Context, dir string, args ...string) (string, error) {
	full := append([]string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "protocol.file.allow=always"}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return out.String(), fmt.Errorf("git %s: %s", args[0], msg)
	}
	return out.String(), nil
}

func (m *Manager) checkRepo(p string) (string, error) {
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", bad("repository path %s is not readable", p)
	}
	for _, r := range m.cfg.RepoRoots {
		rr, err := filepath.EvalSymlinks(r)
		if err != nil {
			continue
		}
		if real == rr || strings.HasPrefix(real, rr+string(os.PathSeparator)) {
			return real, nil
		}
	}
	return "", bad("repository path %s is outside the allowed repository roots", p)
}

// ValidBranch checks a branch name the way git does.
func ValidBranch(ctx context.Context, b string) bool {
	if b == "" || strings.HasPrefix(b, "-") {
		return false
	}
	_, err := git(ctx, "", "check-ref-format", "--branch", b)
	return err == nil
}

func (r *PrepareRequest) validate(ctx context.Context) error {
	if !idRe.MatchString(r.ID) {
		return bad("id must be a UUID")
	}
	if !ValidBranch(ctx, r.Branch) {
		return bad("invalid branch name %q", r.Branch)
	}
	if r.Base != "" && !ValidBranch(ctx, r.Base) {
		return bad("invalid base branch %q", r.Base)
	}
	if !imageRe.MatchString(r.Image) {
		return bad("invalid image %q", r.Image)
	}
	for k := range r.Env {
		if !envRe.MatchString(k) {
			return bad("invalid environment variable name %q", k)
		}
	}
	if r.Limits.CPUs <= 0 || r.Limits.MemoryMB < 128 || r.Limits.Pids < 32 {
		return bad("limits are required")
	}
	return nil
}

// ---- prepare ----

// Prepare starts preparing a workspace and returns the job that does it.
func (m *Manager) Prepare(ctx context.Context, req PrepareRequest) (string, error) {
	if err := req.validate(ctx); err != nil {
		return "", err
	}
	repo, err := m.checkRepo(req.RepoPath)
	if err != nil {
		return "", err
	}
	req.RepoPath = repo
	j := m.jobs.start(req.ID, "prepare", func(ctx context.Context, j *job) (int, any, error) {
		st, err := m.prepare(ctx, req, j)
		if err != nil {
			fmt.Fprintf(j, "\n✗ %v\n", err)
			return -1, nil, err
		}
		return 0, st, nil
	})
	return j.st.ID, nil
}

func (m *Manager) sandboxEnv(extra map[string]string) []string {
	env := []string{
		"HOME=/home/wise", "XDG_CACHE_HOME=/home/wise/.cache", "npm_config_cache=/home/wise/.npm",
		"GOPATH=/home/wise/go", "GOCACHE=/home/wise/.cache/go-build", "GOFLAGS=-modcacherw",
		"PIP_CACHE_DIR=/home/wise/.cache/pip", "CARGO_HOME=/home/wise/.cargo",
		"NPM_CONFIG_PREFIX=/home/wise/.npm-global", "GIT_TERMINAL_PROMPT=0", "CI=true",
		// Tools installed by setup (npm -g, pip --user, go install) land under
		// HOME; the image's own directories come after.
		"PATH=/home/wise/.local/bin:/home/wise/.npm-global/bin:/home/wise/go/bin:/home/wise/.cargo/bin:/usr/local/go/bin:" +
			"/usr/local/cargo/bin:/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
	}
	if m.cfg.Proxy != "" {
		env = append(env, "HTTP_PROXY="+m.cfg.Proxy, "HTTPS_PROXY="+m.cfg.Proxy, "http_proxy="+m.cfg.Proxy,
			"https_proxy="+m.cfg.Proxy, "NO_PROXY=localhost,127.0.0.1", "no_proxy=localhost,127.0.0.1")
	}
	for k, v := range extra {
		env = append(env, k+"="+v)
	}
	return env
}

func (m *Manager) prepare(ctx context.Context, req PrepareRequest, log io.Writer) (State, error) {
	defer m.lock(req.ID)()
	if st, err := m.load(req.ID); err == nil && st.Prepared {
		// Already prepared (a retry): never touch git again, just make sure
		// the sandbox runs.
		fmt.Fprintln(log, "workspace already prepared; starting its sandbox")
		return st, m.rt.Ensure(ctx, req.ID)
	}
	// A half-prepared workspace never ran an agent: start over.
	_ = m.rt.Remove(ctx, req.ID)
	if err := removeTree(m.dir(req.ID)); err != nil {
		return State{}, err
	}
	for _, d := range []string{"repo", "home", "tools"} {
		if err := os.MkdirAll(filepath.Join(m.dir(req.ID), d), 0o755); err != nil {
			return State{}, err
		}
	}
	st := State{ID: req.ID, RepoPath: req.RepoPath, Branch: req.Branch, Base: req.Base, Image: req.Image, CreatedAt: time.Now().UTC()}

	fmt.Fprintf(log, "cloning %s …\n", req.RepoPath)
	repoDir := filepath.Join(m.dir(req.ID), "repo")
	args := []string{"clone", "-q", "--no-hardlinks", "--single-branch"}
	if req.Base != "" {
		args = append(args, "--branch", req.Base)
	}
	if _, err := git(ctx, "", append(args, "--", req.RepoPath, repoDir)...); err != nil {
		return State{}, err
	}
	if st.Base == "" {
		b, _ := git(ctx, repoDir, "symbolic-ref", "--short", "-q", "HEAD")
		st.Base = strings.TrimSpace(b)
	}
	sha, err := git(ctx, repoDir, "rev-parse", "HEAD")
	if err != nil {
		return State{}, fmt.Errorf("the repository has no commits on %s", st.Base)
	}
	st.BaseSHA = strings.TrimSpace(sha)
	for _, a := range [][]string{{"remote", "remove", "origin"}, {"checkout", "-q", "-b", req.Branch}} {
		if _, err := git(ctx, repoDir, a...); err != nil {
			return State{}, err
		}
	}
	fmt.Fprintf(log, "branch %s from %s at %s\n", req.Branch, st.Base, st.BaseSHA[:12])

	// Tracked files named like credentials stay out of the sandbox.
	files, err := git(ctx, repoDir, "ls-files", "-z")
	if err != nil {
		return State{}, err
	}
	for _, f := range strings.Split(files, "\x00") {
		if f != "" && secretguard.LooksSecret(f) {
			if err := os.Remove(filepath.Join(repoDir, f)); err == nil || errors.Is(err, fs.ErrNotExist) {
				if _, err := git(ctx, repoDir, "update-index", "--skip-worktree", "--", f); err != nil {
					return State{}, err
				}
				st.Scrubbed = append(st.Scrubbed, f)
			}
		}
	}
	if len(st.Scrubbed) > 0 {
		fmt.Fprintf(log, "kept %d credential-like file(s) out of the workspace: %s\n", len(st.Scrubbed), strings.Join(st.Scrubbed, ", "))
	}

	// The executor-private repository that verifies commits from the sandbox
	// (shares the user's objects; never mounted anywhere).
	if _, err := git(ctx, "", "clone", "-q", "--bare", "--shared", "--single-branch", "--", req.RepoPath, m.trusted(req.ID)); err != nil {
		return State{}, err
	}
	if _, err := git(ctx, m.trusted(req.ID), "config", "uploadpack.allowAnySHA1InWant", "true"); err != nil {
		return State{}, err
	}

	if err := copyFile(m.cfg.Helper, filepath.Join(m.dir(req.ID), "tools", "wise-sandbox"), 0o755); err != nil {
		return State{}, fmt.Errorf("install sandbox helper: %w", err)
	}
	if err := m.rt.EnsureImage(ctx, req.Image, log); err != nil {
		return State{}, err
	}
	fmt.Fprintf(log, "starting sandbox (%s, %.2g CPU, %d MB)\n", req.Image, req.Limits.CPUs, req.Limits.MemoryMB)
	if err := m.rt.Create(ctx, SandboxSpec{ID: req.ID, Image: req.Image, User: m.cfg.User, Env: m.sandboxEnv(req.Env),
		CPUs: req.Limits.CPUs, MemoryMB: req.Limits.MemoryMB, Pids: req.Limits.Pids}); err != nil {
		return State{}, err
	}

	// Preflight: the tools the repository's commands need answer.
	tools := append([]string{"sh", "git"}, req.Tools...)
	seen := map[string]bool{}
	for _, t := range tools {
		if seen[t] || !envRe.MatchString(strings.ReplaceAll(t, "-", "_")) {
			continue
		}
		seen[t] = true
		var out bytes.Buffer
		code, err := m.rt.Exec(ctx, req.ID, []string{"/bin/sh", "-c", `command -v "$1" >/dev/null || exit 127; "$1" --version 2>&1 | head -n 1`, "sh", t}, nil, nil, &limitWriter{w: &out, n: 4096})
		if err != nil {
			return State{}, err
		}
		if code != 0 && t != "sh" {
			return State{}, fmt.Errorf("preflight: %q is not available in image %s", t, req.Image)
		}
		v := strings.TrimSpace(out.String())
		if t == "sh" {
			v = "sh"
		}
		fmt.Fprintf(log, "✓ %s: %s\n", t, v)
		st.Tools = append(st.Tools, t+": "+v)
	}

	timeout := req.SetupTimeout
	if timeout <= 0 {
		timeout = 900
	}
	for _, c := range req.Setup {
		fmt.Fprintf(log, "$ %s\n", c)
		code, err := m.run(ctx, req.ID, newID(), c, timeout, log)
		if err != nil {
			return State{}, err
		}
		if code != 0 {
			return State{}, fmt.Errorf("setup command %q exited with %d", c, code)
		}
	}
	st.Prepared = true
	if err := m.save(st); err != nil {
		return State{}, err
	}
	fmt.Fprintln(log, "✓ workspace ready")
	return st, nil
}

func copyFile(src, dst string, mode fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// removeTree deletes a workspace directory. It never follows symlinks; it
// makes directories writable first (module caches are read-only).
func removeTree(dir string) error {
	if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() && d.Type()&fs.ModeSymlink == 0 {
			_ = os.Chmod(p, 0o755)
		}
		return nil
	})
	return os.RemoveAll(dir)
}

type limitWriter struct {
	w io.Writer
	n int
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if l.n <= 0 {
		return len(p), nil
	}
	q := p
	if len(q) > l.n {
		q = q[:l.n]
	}
	l.n -= len(q)
	_, err := l.w.Write(q)
	return len(p), err
}

// ---- inspect, stop, remove ----

// Get returns a workspace.
func (m *Manager) Get(ctx context.Context, id string) (Info, error) {
	st, err := m.load(id)
	if err != nil {
		return Info{}, err
	}
	s, err := m.rt.State(ctx, id)
	if err != nil {
		return Info{}, err
	}
	return Info{State: st, Sandbox: s}, nil
}

// Stop stops a workspace's sandbox (files stay).
func (m *Manager) Stop(ctx context.Context, id string) error {
	if _, err := m.load(id); err != nil {
		return err
	}
	return m.rt.Stop(ctx, id)
}

// Remove deletes a workspace: its sandbox, network and files.
func (m *Manager) Remove(ctx context.Context, id string) error {
	if !idRe.MatchString(id) {
		return ErrNotFound
	}
	defer m.lock(id)()
	if err := m.rt.Remove(ctx, id); err != nil {
		return err
	}
	return removeTree(m.dir(id))
}

// ---- commands ----

func (m *Manager) helperCmd(args ...string) []string {
	return append([]string{"/opt/wise/wise-sandbox"}, args...)
}

func (m *Manager) run(ctx context.Context, ws, id, command string, timeout int, out io.Writer) (int, error) {
	code, err := m.rt.Exec(ctx, ws, m.helperCmd("run", "--id", id, "--timeout", strconv.Itoa(timeout), "--", command), nil, nil, out)
	if ctx.Err() != nil {
		// Cancelled: stop the command's process group too.
		kctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, _ = m.rt.Exec(kctx, ws, m.helperCmd("kill", id), nil, nil, io.Discard)
		cancel()
		return code, ctx.Err()
	}
	return code, err
}

// Command starts a shell command in the sandbox and returns its job id.
func (m *Manager) Command(ctx context.Context, ws, command string, timeout int) (string, error) {
	st, err := m.load(ws)
	if err != nil {
		return "", err
	}
	if !st.Prepared {
		return "", bad("the workspace is not ready")
	}
	if strings.TrimSpace(command) == "" {
		return "", bad("command is required")
	}
	if timeout <= 0 || timeout > 6*3600 {
		timeout = 600
	}
	if err := m.rt.Ensure(ctx, ws); err != nil {
		return "", err
	}
	j := m.jobs.start(ws, "command", func(ctx context.Context, j *job) (int, any, error) {
		code, err := m.run(ctx, ws, newID(), command, timeout, j)
		return code, nil, err
	})
	return j.st.ID, nil
}

// Job returns a job's status and output from offset, waiting up to wait for
// it to finish (or for new output).
func (m *Manager) Job(ctx context.Context, id string, offset int64, wait time.Duration) (JobStatus, error) {
	j := m.jobs.get(id)
	if j == nil {
		return JobStatus{}, ErrNotFound
	}
	if wait > 0 {
		deadline := time.NewTimer(wait)
		defer deadline.Stop()
		tick := time.NewTicker(250 * time.Millisecond)
		defer tick.Stop()
	loop:
		for {
			j.mu.Lock()
			// News = at least one whole line (partial lines are held back).
			more := false
			if start := offset - j.first; start < int64(len(j.buf)) {
				rest := j.buf[max(start, 0):]
				more = bytes.IndexByte(rest, '\n') >= 0 || len(rest) >= 64<<10
			}
			j.mu.Unlock()
			if more {
				break
			}
			select {
			case <-j.done:
				break loop
			case <-deadline.C:
				break loop
			case <-ctx.Done():
				break loop
			case <-tick.C:
			}
		}
	}
	return j.snapshot(offset, 256<<10), nil
}

// CancelJob stops a running job.
func (m *Manager) CancelJob(id string) error {
	j := m.jobs.get(id)
	if j == nil {
		return ErrNotFound
	}
	j.cancel()
	return nil
}

// ---- file operations ----

// FS runs one file operation (see cmd/wise-sandbox) and returns its JSON
// result. Output is redacted for credentials.
func (m *Manager) FS(ctx context.Context, ws string, req json.RawMessage) (json.RawMessage, error) {
	st, err := m.load(ws)
	if err != nil {
		return nil, err
	}
	if !st.Prepared {
		return nil, bad("the workspace is not ready")
	}
	if err := m.rt.Ensure(ctx, ws); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	code, err := m.rt.Exec(ctx, ws, m.helperCmd("fs"), nil, bytes.NewReader(req), &limitWriter{w: &out, n: 32 << 20})
	if err != nil {
		return nil, err
	}
	raw := bytes.TrimSpace(out.Bytes())
	if code != 0 || !json.Valid(raw) {
		return nil, fmt.Errorf("sandbox file operation failed (exit %d)", code)
	}
	return json.RawMessage(secretguard.Redact(string(raw))), nil
}
