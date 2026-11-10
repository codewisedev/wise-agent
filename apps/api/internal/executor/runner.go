package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// RunnerRequest starts a coding CLI on a task workspace. The adapter (API)
// renders the invocation; the executor owns the container template, the
// fresh run home, the egress grant, supervision and cleanup.
type RunnerRequest struct {
	CLI        string            `json:"cli"`     // claude-code | codex
	Version    string            `json:"version"` // bundle version, e.g. 2.1.319
	Command    []string          `json:"command"` // argv; "{cli}" is replaced by the CLI path
	Env        map[string]string `json:"env,omitempty"`
	Files      map[string]string `json:"files,omitempty"`   // written into the fresh run home before start
	Collect    []string          `json:"collect,omitempty"` // home-relative files read back after the run
	GrantHosts []string          `json:"grantHosts,omitempty"`
	NoProxy    []string          `json:"noProxy,omitempty"`
	WallClockS int               `json:"wallClockS"`
	IdleS      int               `json:"idleS"`
}

// RunnerResult is a finished runner job's result.
type RunnerResult struct {
	ExitCode  int               `json:"exitCode"`
	TimedOut  bool              `json:"timedOut,omitempty"`
	IdleOut   bool              `json:"idleOut,omitempty"`
	Collected map[string]string `json:"collected,omitempty"`
}

var (
	cliRe     = regexp.MustCompile(`^(claude-code|codex)$`)
	versionRe = regexp.MustCompile(`^[0-9][0-9A-Za-z.+-]{0,40}$`)
	cliBins   = map[string]string{"claude-code": "claude", "codex": "codex"}
	// Variables Wise sets for every runner; adapters cannot override them.
	runnerReserved = map[string]bool{"HOME": true, "PATH": true, "HTTP_PROXY": true, "HTTPS_PROXY": true, "http_proxy": true,
		"https_proxy": true, "NO_PROXY": true, "no_proxy": true, "WISE_WORKSPACE": true}
)

func cleanHomePath(p string) (string, bool) {
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return "", false
		}
	}
	c := path.Clean("/" + p)[1:]
	return c, c != "" && c != "." && !strings.Contains(p, "\x00") && !strings.HasPrefix(p, "/")
}

func (r *RunnerRequest) validate() error {
	if !cliRe.MatchString(r.CLI) || !versionRe.MatchString(r.Version) {
		return bad("unknown CLI or version")
	}
	if len(r.Command) == 0 || len(r.Command) > 200 {
		return bad("command is required")
	}
	for k := range r.Env {
		if !envRe.MatchString(k) || runnerReserved[k] {
			return bad("invalid or reserved environment variable %q", k)
		}
	}
	for p, c := range r.Files {
		if _, ok := cleanHomePath(p); !ok || len(c) > 4<<20 {
			return bad("invalid file %q", p)
		}
	}
	for _, p := range r.Collect {
		if _, ok := cleanHomePath(p); !ok {
			return bad("invalid collect path %q", p)
		}
	}
	for _, h := range append(append([]string{}, r.GrantHosts...), r.NoProxy...) {
		if !ValidHost(h) && !regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`).MatchString(h) {
			return bad("invalid host %q", h)
		}
	}
	if r.WallClockS < 0 || r.WallClockS > 6*3600 || r.WallClockS == 0 {
		r.WallClockS = 1800
	}
	if r.IdleS <= 0 || r.IdleS > r.WallClockS {
		r.IdleS = 300
	}
	return nil
}

// ---- egress grants ----

// Grant lets one runner reach extra hosts through the egress proxy.
type Grant struct {
	Hosts   []string  `json:"hosts"`
	Expires time.Time `json:"expires"`
}

type grants struct {
	mu sync.Mutex
	m  map[string]Grant
}

func (g *grants) put(token string, gr Grant) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.m == nil {
		g.m = map[string]Grant{}
	}
	g.m[token] = gr
}

func (g *grants) revoke(token string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.m, token)
}

// Grant returns a live grant by runner token.
func (m *Manager) Grant(token string) (Grant, bool) {
	m.grants.mu.Lock()
	defer m.grants.mu.Unlock()
	gr, ok := m.grants.m[token]
	if !ok || time.Now().After(gr.Expires) {
		return Grant{}, false
	}
	return gr, true
}

// ---- runners ----

// Runner starts a coding CLI on the workspace and returns its job id. The job
// output is the CLI's stdout and stderr (JSONL events for the adapters).
func (m *Manager) Runner(ctx context.Context, ws string, req RunnerRequest) (string, error) {
	st, err := m.load(ws)
	if err != nil {
		return "", err
	}
	if !st.Prepared {
		return "", bad("the workspace is not ready")
	}
	if err := req.validate(); err != nil {
		return "", err
	}
	runID := newID()
	token := newID() + newID()
	j := m.jobs.start(ws, "runner", func(ctx context.Context, j *job) (int, any, error) {
		res, err := m.runRunner(ctx, st, runID, token, req, j)
		return res.ExitCode, res, err
	})
	return j.st.ID, nil
}

func (m *Manager) runRunner(ctx context.Context, st State, runID, token string, req RunnerRequest, j *job) (RunnerResult, error) {
	res := RunnerResult{ExitCode: -1}
	home := filepath.Join(m.dir(st.ID), "runs", runID, "home")
	// A brand-new directory: nothing an earlier run planted can be in it.
	if err := os.MkdirAll(filepath.Dir(home), 0o700); err != nil {
		return res, err
	}
	if err := os.Mkdir(home, 0o755); err != nil {
		return res, fmt.Errorf("run home: %w", err)
	}
	defer func() { _ = removeTree(filepath.Dir(home)) }()
	for p, content := range req.Files {
		c, _ := cleanHomePath(p)
		full := filepath.Join(home, c)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return res, err
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			return res, err
		}
	}
	m.grants.put(token, Grant{Hosts: req.GrantHosts, Expires: time.Now().Add(time.Duration(req.WallClockS+300) * time.Second)})
	defer m.grants.revoke(token)

	proxy := ""
	if m.cfg.Proxy != "" {
		proxy = strings.Replace(m.cfg.Proxy, "://", "://run:"+token+"@", 1)
	}
	noProxy := strings.Join(append([]string{"localhost", "127.0.0.1"}, req.NoProxy...), ",")
	env := []string{"HOME=/home/wise", "XDG_CACHE_HOME=/home/wise/.cache", "NPM_CONFIG_PREFIX=/home/wise/.npm-global",
		"GOFLAGS=-modcacherw", "GIT_TERMINAL_PROMPT=0", "CI=true", "WISE_WORKSPACE=/workspace",
		"PATH=/opt/cli:/home/wise/.local/bin:/home/wise/.npm-global/bin:/home/wise/go/bin:/usr/local/go/bin:" +
			"/usr/local/cargo/bin:/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"NO_PROXY=" + noProxy, "no_proxy=" + noProxy}
	if proxy != "" {
		env = append(env, "HTTP_PROXY="+proxy, "HTTPS_PROXY="+proxy, "http_proxy="+proxy, "https_proxy="+proxy)
	}
	for k, v := range req.Env {
		env = append(env, k+"="+v)
	}
	limits := Limits{CPUs: 2, MemoryMB: 4096, Pids: 1024}
	if l, ok := m.limits(st.ID); ok {
		limits = l
	}
	spec := RunnerSpec{RunID: runID, WorkspaceID: st.ID, Image: st.Image, User: m.cfg.User, Env: env,
		CPUs: limits.CPUs, MemoryMB: limits.MemoryMB, Pids: limits.Pids, CLISubpath: req.CLI + "/" + req.Version + "/musl"}
	fmt.Fprintf(j, "[wise] starting %s %s runner\n", req.CLI, req.Version)
	if err := m.rt.CreateRunner(ctx, spec); err != nil {
		return res, err
	}
	defer func() {
		cctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		_ = m.rt.RemoveRunner(cctx, runID)
		cancel()
	}()

	bin := "/opt/cli/" + cliBins[req.CLI]
	argv := make([]string, len(req.Command))
	for i, a := range req.Command {
		argv[i] = strings.ReplaceAll(a, "{cli}", bin)
	}
	// Idle limit: no output for IdleS stops the run.
	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var idle atomic.Bool
	go func() {
		t := time.NewTicker(min(5*time.Second, max(200*time.Millisecond, time.Duration(req.IdleS)*time.Second/4)))
		defer t.Stop()
		for {
			select {
			case <-rctx.Done():
				return
			case <-t.C:
				if time.Since(j.lastWrite()) > time.Duration(req.IdleS)*time.Second {
					idle.Store(true)
					cancel()
					return
				}
			}
		}
	}()
	jobID := newID()
	code, err := m.rt.ExecRunner(rctx, runID, append(m.helperCmd("run", "--id", jobID, "--timeout", strconv.Itoa(req.WallClockS), "--"),
		shellJoin(argv)), nil, nil, j)
	if rctx.Err() != nil {
		kctx, kc := context.WithTimeout(context.Background(), 10*time.Second)
		_, _ = m.rt.ExecRunner(kctx, runID, m.helperCmd("kill", jobID), nil, nil, io.Discard)
		kc()
	}
	res.ExitCode = code
	switch {
	case idle.Load():
		res.IdleOut = true
		fmt.Fprintf(j, "\n[wise] stopped: no output for %d s\n", req.IdleS)
		return res, nil
	case ctx.Err() != nil:
		return res, ctx.Err()
	case err != nil:
		return res, err
	}
	res.TimedOut = code == 124
	// Read requested files back by exec (data; size-capped).
	res.Collected = map[string]string{}
	for _, p := range req.Collect {
		c, _ := cleanHomePath(p)
		var out bytes.Buffer
		cctx, cc := context.WithTimeout(context.Background(), 30*time.Second)
		rc, err := m.rt.ExecRunner(cctx, runID, []string{"/bin/sh", "-c", `[ -f "$1" ] && [ ! -L "$1" ] && head -c 4194304 "$1"`, "sh", "/home/wise/" + c},
			nil, nil, &limitWriter{w: &out, n: 4 << 20})
		cc()
		if err == nil && rc == 0 {
			res.Collected[c] = out.String()
		}
	}
	return res, nil
}

// shellJoin quotes argv for sh -c.
func shellJoin(argv []string) string {
	q := make([]string, len(argv))
	for i, a := range argv {
		q[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(q, " ")
}

// limits returns the sandbox limits a workspace was prepared with.
func (m *Manager) limits(ws string) (Limits, bool) {
	st, err := m.load(ws)
	if err != nil || st.Limits.CPUs == 0 {
		return Limits{}, false
	}
	return st.Limits, true
}
