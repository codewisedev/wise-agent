package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var helperBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "wise-sandbox-bin")
	if err != nil {
		panic(err)
	}
	helperBin = filepath.Join(dir, "wise-sandbox")
	cmd := exec.Command("go", "build", "-o", helperBin, "wise/apps/api/cmd/wise-sandbox")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		panic(string(out))
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// local runs "sandboxes" as host processes in the workspace directory.
type local struct {
	dir     string
	mu      sync.Mutex
	running map[string]bool
	runners map[string]RunnerSpec
	cliDir  string // stands in for the CLI bundle
}

func (l *local) CreateRunner(_ context.Context, s RunnerSpec) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.runners == nil {
		l.runners = map[string]RunnerSpec{}
	}
	l.runners[s.RunID] = s
	return nil
}

func (l *local) RemoveRunner(_ context.Context, runID string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.runners, runID)
	return nil
}

func (l *local) ExecRunner(ctx context.Context, runID string, cmd []string, env []string, stdin io.Reader, out io.Writer) (int, error) {
	l.mu.Lock()
	spec, ok := l.runners[runID]
	l.mu.Unlock()
	if !ok {
		return -1, fmt.Errorf("no runner %s", runID)
	}
	args := make([]string, len(cmd))
	for i, a := range cmd {
		a = strings.ReplaceAll(a, "/opt/cli/", l.cliDir+"/")
		args[i] = strings.ReplaceAll(a, "/home/wise/", filepath.Join(l.dir, spec.WorkspaceID, "runs", runID, "home")+"/")
	}
	if args[0] == "/opt/wise/wise-sandbox" {
		args[0] = helperBin
	}
	c := exec.CommandContext(ctx, args[0], args[1:]...)
	c.Dir = filepath.Join(l.dir, spec.WorkspaceID, "repo")
	home := filepath.Join(l.dir, spec.WorkspaceID, "runs", runID, "home")
	c.Env = append(append([]string{"PATH=" + os.Getenv("PATH"), "TMPDIR=" + home}, spec.Env...), env...)
	for i, e := range c.Env { // the runner's HOME is its fresh run home
		if strings.HasPrefix(e, "HOME=") {
			c.Env[i] = "HOME=" + home
		}
	}
	c.Stdin, c.Stdout, c.Stderr = stdin, out, out
	c.WaitDelay = 200 * time.Millisecond
	err := c.Run()
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode(), nil
	}
	if err != nil {
		return -1, err
	}
	return 0, nil
}

func (l *local) EnsureImage(context.Context, string, io.Writer) error { return nil }
func (l *local) ImageExists(context.Context, string) (bool, error)    { return true, nil }
func (l *local) Create(_ context.Context, s SandboxSpec) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.running[s.ID] = true
	return nil
}
func (l *local) Ensure(_ context.Context, id string) error { return nil }
func (l *local) Stop(_ context.Context, id string) error   { return nil }
func (l *local) Remove(_ context.Context, id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.running, id)
	return nil
}
func (l *local) State(_ context.Context, id string) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.running[id] {
		return "running", nil
	}
	return "missing", nil
}
func (l *local) Exec(ctx context.Context, id string, cmd []string, env []string, stdin io.Reader, out io.Writer) (int, error) {
	if cmd[0] == "/opt/wise/wise-sandbox" {
		cmd = append([]string{helperBin}, cmd[1:]...)
	}
	c := exec.CommandContext(ctx, cmd[0], cmd[1:]...)
	ws := filepath.Join(l.dir, id, "repo")
	tmp := filepath.Join(l.dir, id, "home", "tmp")
	_ = os.MkdirAll(tmp, 0o755)
	c.Dir = ws
	c.Env = append(append(os.Environ(), "WISE_WORKSPACE="+ws, "TMPDIR="+tmp, "GIT_CONFIG_NOSYSTEM=1"), env...)
	c.Stdin, c.Stdout, c.Stderr = stdin, out, out
	c.WaitDelay = 200 * time.Millisecond // like a closed docker attach
	err := c.Run()
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode(), nil
	}
	if err != nil {
		return -1, err
	}
	return 0, nil
}

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command(args[0], args[1:]...)
	c.Dir = dir
	c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@example.com")
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

type fixture struct {
	m    *Manager
	repo string
	root string
	ws   string
}

func setup(t *testing.T) fixture {
	t.Helper()
	root := t.TempDir()
	repo := filepath.Join(root, "repos", "app")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, repo, "git", "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644)
	os.WriteFile(filepath.Join(repo, ".env"), []byte("TOKEN=supersecretvalue\n"), 0o644)
	os.WriteFile(filepath.Join(repo, "README.md"), []byte("# app\n"), 0o644)
	run(t, repo, "git", "add", "-A")
	run(t, repo, "git", "commit", "-q", "-m", "init")
	wsDir := filepath.Join(root, "workspaces")
	os.MkdirAll(wsDir, 0o755)
	m := NewManager(Config{Dir: wsDir, RepoRoots: []string{filepath.Join(root, "repos")}, Helper: helperBin, User: "1000:1000"},
		&local{dir: wsDir, running: map[string]bool{}}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return fixture{m: m, repo: repo, root: root, ws: "0b8f8a52-2a8e-4c39-9b47-1a2b3c4d5e6f"}
}

func waitJob(t *testing.T, m *Manager, id string) JobStatus {
	t.Helper()
	var offset int64
	var out strings.Builder
	for i := 0; i < 600; i++ {
		st, err := m.Job(context.Background(), id, offset, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		out.WriteString(st.Output)
		offset = st.NextOffset
		if st.Status != JobRunning && !st.More {
			st.Output = out.String()
			return st
		}
	}
	t.Fatal("job did not finish")
	return JobStatus{}
}

func (f fixture) prepare(t *testing.T) {
	t.Helper()
	id, err := f.m.Prepare(context.Background(), PrepareRequest{ID: f.ws, RepoPath: f.repo, Branch: "wise/7-add-greeting",
		Image: "wise-toolchain-go:1", Limits: Limits{CPUs: 1, MemoryMB: 512, Pids: 256}, Setup: []string{"echo setup-ran"}})
	if err != nil {
		t.Fatal(err)
	}
	st := waitJob(t, f.m, id)
	if st.Status != JobSucceeded {
		t.Fatalf("prepare: %s %s\n%s", st.Status, st.Error, st.Output)
	}
	if !strings.Contains(st.Output, "setup-ran") || !strings.Contains(st.Output, "kept 1 credential-like file") {
		t.Fatalf("prepare output: %s", st.Output)
	}
}

func (f fixture) fs(t *testing.T, req map[string]any) map[string]any {
	t.Helper()
	raw, _ := json.Marshal(req)
	res, err := f.m.FS(context.Background(), f.ws, raw)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(res, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestPrepareBranchesAndKeepsSecretsOut(t *testing.T) {
	f := setup(t)
	f.prepare(t)
	info, err := f.m.Get(context.Background(), f.ws)
	if err != nil {
		t.Fatal(err)
	}
	if info.Branch != "wise/7-add-greeting" || info.Base != "main" || len(info.BaseSHA) != 40 || !info.Prepared {
		t.Fatalf("info: %+v", info)
	}
	ws := filepath.Join(f.root, "workspaces", f.ws, "repo")
	if _, err := os.Stat(filepath.Join(ws, ".env")); !os.IsNotExist(err) {
		t.Fatal(".env must not be in the workspace")
	}
	if b := run(t, ws, "git", "branch", "--show-current"); b != "wise/7-add-greeting" {
		t.Fatalf("branch %s", b)
	}
	if r := run(t, ws, "git", "remote"); r != "" {
		t.Fatalf("remotes left: %s", r)
	}
	// A retry of an already prepared workspace does not re-clone.
	os.WriteFile(filepath.Join(ws, "agent.txt"), []byte("work"), 0o644)
	f.prepare2(t)
	if _, err := os.Stat(filepath.Join(ws, "agent.txt")); err != nil {
		t.Fatal("a retried prepare must keep the workspace")
	}
}

func (f fixture) prepare2(t *testing.T) {
	id, err := f.m.Prepare(context.Background(), PrepareRequest{ID: f.ws, RepoPath: f.repo, Branch: "wise/7-add-greeting",
		Image: "wise-toolchain-go:1", Limits: Limits{CPUs: 1, MemoryMB: 512, Pids: 256}})
	if err != nil {
		t.Fatal(err)
	}
	if st := waitJob(t, f.m, id); st.Status != JobSucceeded {
		t.Fatalf("%s %s", st.Status, st.Error)
	}
}

func TestPrepareRefusesRepositoriesOutsideRoots(t *testing.T) {
	f := setup(t)
	other := t.TempDir()
	run(t, other, "git", "init", "-q")
	_, err := f.m.Prepare(context.Background(), PrepareRequest{ID: f.ws, RepoPath: other, Branch: "wise/x",
		Image: "img:1", Limits: Limits{CPUs: 1, MemoryMB: 512, Pids: 256}})
	if err == nil || !strings.Contains(err.Error(), "outside the allowed") {
		t.Fatalf("err = %v", err)
	}
	for _, b := range []string{"-x", "a..b", "x y", ""} {
		if ValidBranch(context.Background(), b) {
			t.Errorf("branch %q accepted", b)
		}
	}
}

func TestFileOperations(t *testing.T) {
	f := setup(t)
	f.prepare(t)
	if r := f.fs(t, map[string]any{"op": "write", "path": "pkg/greet.go", "content": "package pkg\n\nfunc Greet() string { return \"hi\" }\n"}); r["error"] != nil || r["created"] != true {
		t.Fatalf("write: %v", r)
	}
	if r := f.fs(t, map[string]any{"op": "edit", "path": "pkg/greet.go", "old": "\"hi\"", "new": "\"hello\""}); r["error"] != nil {
		t.Fatalf("edit: %v", r)
	}
	if r := f.fs(t, map[string]any{"op": "read", "path": "pkg/greet.go"}); !strings.Contains(r["content"].(string), "hello") {
		t.Fatalf("read: %v", r)
	}
	f.fs(t, map[string]any{"op": "write", "path": "dup.txt", "content": "a\na\n"})
	if r := f.fs(t, map[string]any{"op": "edit", "path": "dup.txt", "old": "a", "new": "b"}); !strings.Contains(r["error"].(string), "occurs 2 times") {
		t.Fatalf("ambiguous edit: %v", r)
	}
	for _, p := range []string{".git/config", ".git/hooks/pre-commit", "../escape.txt"} {
		r := f.fs(t, map[string]any{"op": "write", "path": p, "content": "x"})
		if p == "../escape.txt" {
			// Cleaned into the workspace root, never outside it.
			if _, err := os.Stat(filepath.Join(f.root, "workspaces", f.ws, "escape.txt")); err == nil {
				t.Fatal("wrote outside the workspace")
			}
			continue
		}
		if r["error"] == nil {
			t.Fatalf("write %s allowed", p)
		}
	}
	// A symlink out of the workspace is refused.
	ws := filepath.Join(f.root, "workspaces", f.ws, "repo")
	os.Symlink(f.repo, filepath.Join(ws, "link"))
	if r := f.fs(t, map[string]any{"op": "read", "path": "link/main.go"}); r["error"] == nil {
		t.Fatalf("read through symlink allowed: %v", r)
	}
	if r := f.fs(t, map[string]any{"op": "write", "path": "link/evil.go", "content": "x"}); r["error"] == nil {
		t.Fatal("write through symlink allowed")
	}
	// Secrets: by name and by content.
	f.fs(t, map[string]any{"op": "write", "path": "config/.env.local", "content": "A=1"})
	if r := f.fs(t, map[string]any{"op": "read", "path": "config/.env.local"}); !strings.Contains(r["error"].(string), "secrets") {
		t.Fatalf("secret file read: %v", r)
	}
	f.fs(t, map[string]any{"op": "write", "path": "notes.txt", "content": "key sk-ant-abcdefghijklmnopqrstuvwxyz0123\n"})
	if r := f.fs(t, map[string]any{"op": "read", "path": "notes.txt"}); r["error"] == nil {
		t.Fatalf("token file read: %v", r)
	}
	if r := f.fs(t, map[string]any{"op": "search", "query": "sk-ant"}); strings.Contains(mustJSON(r), "abcdefghij") {
		t.Fatalf("search leaked a token: %v", r)
	}
	if r := f.fs(t, map[string]any{"op": "list", "path": "."}); !strings.Contains(mustJSON(r), "pkg") {
		t.Fatalf("list: %v", r)
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestCommandsAndCancel(t *testing.T) {
	f := setup(t)
	f.prepare(t)
	id, err := f.m.Command(context.Background(), f.ws, "echo out; echo err >&2; exit 3", 30)
	if err != nil {
		t.Fatal(err)
	}
	st := waitJob(t, f.m, id)
	if st.Status != JobSucceeded || st.ExitCode == nil || *st.ExitCode != 3 || !strings.Contains(st.Output, "out") || !strings.Contains(st.Output, "err") {
		t.Fatalf("command: %+v", st)
	}
	id, _ = f.m.Command(context.Background(), f.ws, "echo started; sleep 30", 60)
	time.Sleep(500 * time.Millisecond)
	if err := f.m.CancelJob(id); err != nil {
		t.Fatal(err)
	}
	if st := waitJob(t, f.m, id); st.Status != JobCancelled {
		t.Fatalf("cancel: %+v", st)
	}
	id, _ = f.m.Command(context.Background(), f.ws, "sleep 5", 1)
	if st := waitJob(t, f.m, id); st.ExitCode == nil || *st.ExitCode != 124 || !strings.Contains(st.Output, "timed out") {
		t.Fatalf("timeout: %+v", st)
	}
}

func TestSnapshotAndDeliver(t *testing.T) {
	f := setup(t)
	f.prepare(t)
	ws := filepath.Join(f.root, "workspaces", f.ws, "repo")
	userHead := run(t, f.repo, "git", "rev-parse", "HEAD")

	cs, err := f.m.Snapshot(context.Background(), f.ws, SnapshotRequest{Message: "nothing", AuthorName: "Dev", AuthorEmail: "dev@example.com"})
	if err != nil || cs.HeadSHA != "" || len(cs.Files) != 0 {
		t.Fatalf("empty snapshot: %+v %v", cs, err)
	}

	// Hostile workspace: an fsmonitor and hooks that would run on the
	// executor side if it ever ran git there.
	marker := filepath.Join(f.root, "pwned")
	run(t, ws, "git", "config", "core.fsmonitor", "touch "+marker)
	os.WriteFile(filepath.Join(ws, ".git", "hooks", "pre-commit"), []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o755)

	f.fs(t, map[string]any{"op": "write", "path": "greet.go", "content": "package main\n\nfunc greet() string { return \"hi\" }\n"})
	f.fs(t, map[string]any{"op": "edit", "path": "main.go", "old": "func main() {}", "new": "func main() { println(greet()) }"})
	cs, err = f.m.Snapshot(context.Background(), f.ws, SnapshotRequest{Message: "Add greeting\n\nWise-Task: #7", AuthorName: "Dev", AuthorEmail: "dev@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if len(cs.HeadSHA) != 40 || len(cs.Files) != 2 || cs.Additions != 4 || cs.Deletions != 1 || !strings.Contains(cs.Patch, "+func greet()") {
		t.Fatalf("changeset: %+v", cs)
	}
	// A second snapshot replaces the first (one commit on the base).
	f.fs(t, map[string]any{"op": "write", "path": "greet.go", "content": "package main\n\nfunc greet() string { return \"hello\" }\n"})
	cs2, err := f.m.Snapshot(context.Background(), f.ws, SnapshotRequest{Message: "Add greeting", AuthorName: "Dev", AuthorEmail: "dev@example.com"})
	if err != nil || cs2.HeadSHA == cs.HeadSHA || len(cs2.Files) != 2 {
		t.Fatalf("second snapshot: %+v %v", cs2, err)
	}

	if _, err := f.m.Deliver(context.Background(), f.ws, DeliverRequest{SHA: cs.HeadSHA}); err == nil {
		t.Fatal("delivered a commit that is not the current snapshot")
	}
	// The branch name is taken by other work: never overwritten.
	run(t, f.repo, "git", "branch", "wise/7-add-greeting")
	d, err := f.m.Deliver(context.Background(), f.ws, DeliverRequest{SHA: cs2.HeadSHA})
	if err != nil {
		t.Fatal(err)
	}
	if d.Branch != "wise/7-add-greeting-2" {
		t.Fatalf("delivery: %+v", d)
	}
	if got := run(t, f.repo, "git", "rev-parse", "wise/7-add-greeting-2"); got != cs2.HeadSHA {
		t.Fatalf("branch at %s", got)
	}
	if got := run(t, f.repo, "git", "rev-parse", "wise/7-add-greeting"); got != userHead {
		t.Fatal("existing branch moved")
	}
	// The user's checkout is untouched.
	if run(t, f.repo, "git", "rev-parse", "HEAD") != userHead || run(t, f.repo, "git", "branch", "--show-current") != "main" ||
		run(t, f.repo, "git", "status", "--porcelain") != "" {
		t.Fatal("the user's checkout changed")
	}
	// Delivering again is idempotent.
	if d2, err := f.m.Deliver(context.Background(), f.ws, DeliverRequest{SHA: cs2.HeadSHA}); err != nil || !d2.Existed || d2.Branch != "wise/7-add-greeting-2" {
		t.Fatalf("redeliver: %+v %v", d2, err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a planted fsmonitor or hook ran")
	}
	msg := run(t, f.repo, "git", "log", "-1", "--format=%an <%ae>", cs2.HeadSHA)
	if msg != "Dev <dev@example.com>" {
		t.Fatalf("author %s", msg)
	}
	// Without an author, task commits take the base commit's author.
	f.fs(t, map[string]any{"op": "write", "path": "x.txt", "content": "x"})
	cs3, err := f.m.Snapshot(context.Background(), f.ws, SnapshotRequest{Message: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if got := run(t, filepath.Join(f.root, "workspaces", f.ws, "trusted.git"), "git", "log", "-1", "--format=%an <%ae>", cs3.HeadSHA); got != "T <t@example.com>" {
		t.Fatalf("default author %s", got)
	}

	if err := f.m.Remove(context.Background(), f.ws); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(f.root, "workspaces", f.ws)); !os.IsNotExist(err) {
		t.Fatal("workspace files left")
	}
}

func TestSnapshotFlagsSecrets(t *testing.T) {
	f := setup(t)
	f.prepare(t)
	f.fs(t, map[string]any{"op": "write", "path": "cfg.go", "content": "package main\n\nconst k = \"ghp_" + strings.Repeat("a", 36) + "\"\n"})
	cs, err := f.m.Snapshot(context.Background(), f.ws, SnapshotRequest{Message: "cfg", AuthorName: "Dev", AuthorEmail: "dev@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if !cs.SecretFound || strings.Contains(cs.Patch, strings.Repeat("a", 36)) {
		t.Fatalf("secret not flagged/redacted: %+v", cs)
	}
}

func TestHostPatterns(t *testing.T) {
	for _, h := range []string{"registry.npmjs.org", "*.pythonhosted.org", "proxy.golang.org"} {
		if !ValidHost(h) {
			t.Errorf("%s rejected", h)
		}
	}
	for _, h := range []string{"10.0.0.1", "*", "localhost", "a.b:443", "*.com.", "http://x.org"} {
		if ValidHost(h) {
			t.Errorf("%s accepted", h)
		}
	}
	_ = bytes.MinRead
}

func TestRunnerLifecycle(t *testing.T) {
	f := setup(t)
	f.m.cfg.Proxy = "http://wise-egress:3128"
	f.prepare(t)
	loc := f.m.rt.(*local)
	loc.cliDir = t.TempDir()
	// A stand-in CLI: prints JSON events and what it sees, writes a result file.
	script := `#!/bin/sh
echo '{"type":"start"}'
echo "home-file: $(cat "$HOME/.config/cli/settings.json")"
echo "proxy: $HTTPS_PROXY"
echo "no-proxy: $NO_PROXY"
echo "custom: $WISE_RUN_MODE"
echo "args: $*"
echo "stdin: $(cat)"
mkdir -p "$HOME/out" && echo '{"done":true}' > "$HOME/out/result.json"
echo '{"type":"result"}'
`
	os.WriteFile(filepath.Join(loc.cliDir, "claude"), []byte(script), 0o755)

	id, err := f.m.Runner(context.Background(), f.ws, RunnerRequest{
		CLI: "claude-code", Version: "2.1.319", Command: []string{"{cli}", "-p", "it's fine"},
		Env: map[string]string{"WISE_RUN_MODE": "test"}, Files: map[string]string{".config/cli/settings.json": `{"x":1}`, "prompt.md": "the prompt"},
		Collect: []string{"out/result.json", "missing.json"}, StdinFile: "prompt.md", GrantHosts: []string{"api.anthropic.com"}, NoProxy: []string{"wise-model-proxy"},
		WallClockS: 60, IdleS: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	st := waitJob(t, f.m, id)
	if st.Status != JobSucceeded {
		t.Fatalf("runner: %+v", st)
	}
	for _, want := range []string{`{"type":"start"}`, `home-file: {"x":1}`, "proxy: http://run:", "@wise-egress:3128",
		"no-proxy: localhost,127.0.0.1,wise-model-proxy", "custom: test", "args: -p it's fine", "stdin: the prompt", `{"type":"result"}`} {
		if !strings.Contains(st.Output, want) {
			t.Errorf("output lacks %q:\n%s", want, st.Output)
		}
	}
	raw, _ := json.Marshal(st.Result)
	var res RunnerResult
	json.Unmarshal(raw, &res)
	if res.ExitCode != 0 || strings.TrimSpace(res.Collected["out/result.json"]) != `{"done":true}` || res.Collected["missing.json"] != "" {
		t.Fatalf("result: %+v", res)
	}
	// The grant lived only during the run; the run directory is gone.
	tok := st.Output[strings.Index(st.Output, "run:")+4 : strings.Index(st.Output, "@wise-egress")]
	if _, ok := f.m.Grant(tok); ok {
		t.Fatal("grant outlived the run")
	}
	if entries, _ := os.ReadDir(filepath.Join(f.root, "workspaces", f.ws, "runs")); len(entries) != 0 {
		t.Fatalf("run directories left: %v", entries)
	}
	if len(loc.runners) != 0 {
		t.Fatal("runner container left")
	}
}

func TestRunnerGrantDuringRunAndIdleLimit(t *testing.T) {
	f := setup(t)
	f.m.cfg.Proxy = "http://wise-egress:3128"
	f.prepare(t)
	loc := f.m.rt.(*local)
	loc.cliDir = t.TempDir()
	os.WriteFile(filepath.Join(loc.cliDir, "codex"), []byte("#!/bin/sh\necho \"proxy: $HTTPS_PROXY\"\nsleep 30\n"), 0o755)
	id, err := f.m.Runner(context.Background(), f.ws, RunnerRequest{CLI: "codex", Version: "0.163.0", Command: []string{"{cli}", "exec"},
		GrantHosts: []string{"chatgpt.com"}, WallClockS: 60, IdleS: 1})
	if err != nil {
		t.Fatal(err)
	}
	var tok string
	for i := 0; i < 50 && tok == ""; i++ {
		st, _ := f.m.Job(context.Background(), id, 0, 200*time.Millisecond)
		if i := strings.Index(st.Output, "run:"); i >= 0 {
			tok = st.Output[i+4 : strings.Index(st.Output, "@")]
		}
	}
	if g, ok := f.m.Grant(tok); !ok || g.Hosts[0] != "chatgpt.com" {
		t.Fatalf("grant during the run: %v %+v", ok, g)
	}
	st := waitJob(t, f.m, id)
	raw, _ := json.Marshal(st.Result)
	var res RunnerResult
	json.Unmarshal(raw, &res)
	if !res.IdleOut || !strings.Contains(st.Output, "no output for 1 s") {
		t.Fatalf("idle limit: %+v %s", res, st.Output)
	}
	if _, ok := f.m.Grant(tok); ok {
		t.Fatal("grant outlived the run")
	}
}

func TestRunnerValidation(t *testing.T) {
	f := setup(t)
	f.prepare(t)
	for _, req := range []RunnerRequest{
		{CLI: "bash", Version: "1", Command: []string{"x"}},
		{CLI: "codex", Version: "1", Command: []string{"x"}, Env: map[string]string{"HTTPS_PROXY": "http://evil"}},
		{CLI: "codex", Version: "1", Command: []string{"x"}, Files: map[string]string{"../escape": "x"}},
		{CLI: "codex", Version: "1", Command: []string{"x"}, Files: map[string]string{"/etc/passwd": "x"}},
		{CLI: "codex", Version: "1", Command: []string{"x"}, Collect: []string{"../../state.json"}},
		{CLI: "codex", Version: "1", Command: []string{"x"}, GrantHosts: []string{"10.0.0.1"}},
	} {
		if _, err := f.m.Runner(context.Background(), f.ws, req); err == nil {
			t.Errorf("accepted %+v", req)
		}
	}
}

func TestOneRunnerPerWorkspace(t *testing.T) {
	f := setup(t)
	f.prepare(t)
	loc := f.m.rt.(*local)
	loc.cliDir = t.TempDir()
	os.WriteFile(filepath.Join(loc.cliDir, "claude"), []byte("#!/bin/sh\necho started\nsleep 30\n"), 0o755)
	req := RunnerRequest{CLI: "claude-code", Version: "1", Command: []string{"{cli}"}, WallClockS: 60, IdleS: 60}
	first, err := f.m.Runner(context.Background(), f.ws, req)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	second, err := f.m.Runner(context.Background(), f.ws, req)
	if err != nil {
		t.Fatal(err)
	}
	if st, _ := f.m.Job(context.Background(), first, 0, 0); st.Status != JobCancelled {
		t.Fatalf("the first runner should be stopped: %s", st.Status)
	}
	f.m.CancelJob(second)
	waitJob(t, f.m, second)
}
