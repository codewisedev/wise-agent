package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"wise/apps/api/internal/secretguard"
)

// SnapshotRequest commits the workspace's changes as one commit on the task
// branch (replacing an earlier snapshot).
type SnapshotRequest struct {
	Message       string `json:"message"`
	AuthorName    string `json:"authorName"`
	AuthorEmail   string `json:"authorEmail"`
	MaxPatchBytes int    `json:"maxPatchBytes,omitempty"`
}

// FileChange is one changed file.
type FileChange struct {
	Path      string `json:"path"`
	OldPath   string `json:"oldPath,omitempty"`
	Status    string `json:"status"` // added | modified | deleted | renamed | copied | type_changed
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Binary    bool   `json:"binary,omitempty"`
	Secret    bool   `json:"secret,omitempty"` // named like a credential or contains a token
}

// Changeset is a verified snapshot: computed in the executor-private
// repository from the commit itself, never from sandbox output.
type Changeset struct {
	BaseSHA     string       `json:"baseSha"`
	HeadSHA     string       `json:"headSha,omitempty"` // empty when there are no changes
	Branch      string       `json:"branch"`
	Message     string       `json:"message,omitempty"`
	Files       []FileChange `json:"files"`
	Additions   int          `json:"additions"`
	Deletions   int          `json:"deletions"`
	Patch       string       `json:"patch"`
	Truncated   bool         `json:"truncated,omitempty"`
	SecretFound bool         `json:"secretFound,omitempty"`
	CreatedAt   time.Time    `json:"createdAt"`
}

var shaRe = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// sandboxGit runs git inside the sandbox (the workspace may be hostile, so
// never on the executor side).
func (m *Manager) sandboxGit(ctx context.Context, ws string, env []string, out *bytes.Buffer, args ...string) (int, error) {
	full := append([]string{"git", "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "commit.gpgsign=false"}, args...)
	return m.rt.Exec(ctx, ws, full, env, nil, &limitWriter{w: out, n: 1 << 20})
}

// Snapshot commits the workspace's changes in the sandbox, brings the commit
// out as a bundle and verifies it in the executor-private repository.
func (m *Manager) Snapshot(ctx context.Context, ws string, req SnapshotRequest) (Changeset, error) {
	defer m.lock(ws)()
	st, err := m.load(ws)
	if err != nil {
		return Changeset{}, err
	}
	if !st.Prepared {
		return Changeset{}, bad("the workspace is not ready")
	}
	if strings.TrimSpace(req.AuthorName) == "" {
		// Default identity: the author of the repository's latest commit.
		req.AuthorName, req.AuthorEmail = st.AuthorName, st.AuthorEmail
		if req.AuthorName == "" {
			req.AuthorName, req.AuthorEmail = "Wise", "wise@localhost"
		}
	}
	if strings.TrimSpace(req.Message) == "" || !strings.Contains(req.AuthorEmail, "@") {
		return Changeset{}, bad("message and a valid author are required")
	}
	if err := m.rt.Ensure(ctx, ws); err != nil {
		return Changeset{}, err
	}
	env := []string{"GIT_AUTHOR_NAME=" + req.AuthorName, "GIT_AUTHOR_EMAIL=" + req.AuthorEmail,
		"GIT_COMMITTER_NAME=" + req.AuthorName, "GIT_COMMITTER_EMAIL=" + req.AuthorEmail}
	var out bytes.Buffer
	step := func(args ...string) error {
		out.Reset()
		code, err := m.sandboxGit(ctx, ws, env, &out, args...)
		if err != nil {
			return err
		}
		if code != 0 {
			return fmt.Errorf("git %s in the sandbox failed: %s", args[0], clip(strings.TrimSpace(out.String()), 500))
		}
		return nil
	}
	// One commit on top of the base, whatever the sandbox did in between.
	if err := step("checkout", "-q", "-B", st.Branch); err != nil {
		return Changeset{}, err
	}
	if err := step("reset", "-q", "--soft", st.BaseSHA); err != nil {
		return Changeset{}, err
	}
	if err := step("add", "-A"); err != nil {
		return Changeset{}, err
	}
	out.Reset()
	code, err := m.sandboxGit(ctx, ws, env, &out, "diff", "--cached", "--quiet")
	if err != nil {
		return Changeset{}, err
	}
	cs := Changeset{BaseSHA: st.BaseSHA, Branch: st.Branch, Files: []FileChange{}, CreatedAt: time.Now().UTC()}
	if code == 0 {
		return cs, nil // nothing changed
	}
	if err := step("commit", "-q", "--no-verify", "-m", req.Message); err != nil {
		return Changeset{}, err
	}

	// Bring the commit out as a bundle (data) and verify it.
	bundle := filepath.Join(m.dir(ws), "incoming.bundle")
	f, err := os.OpenFile(bundle, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return Changeset{}, err
	}
	var stderr bytes.Buffer
	code, err = m.rt.Exec(ctx, ws, []string{"/bin/sh", "-c", `git -c core.hooksPath=/dev/null bundle create - HEAD "^$1" 2>/dev/null`, "sh", st.BaseSHA},
		nil, nil, &limitWriter{w: f, n: 512 << 20})
	f.Close()
	defer os.Remove(bundle)
	if err != nil {
		return Changeset{}, err
	}
	if code != 0 {
		return Changeset{}, fmt.Errorf("could not bundle the commit (exit %d) %s", code, stderr.String())
	}
	tr := m.trusted(ws)
	if _, err := git(ctx, tr, "-c", "transfer.fsckObjects=true", "-c", "fetch.fsckObjects=true",
		"fetch", "-q", "--no-tags", "--no-write-fetch-head", bundle, "+HEAD:refs/wise/snapshot"); err != nil {
		return Changeset{}, fmt.Errorf("verify commit: %w", err)
	}
	head, err := git(ctx, tr, "rev-parse", "--verify", "refs/wise/snapshot^{commit}")
	if err != nil {
		return Changeset{}, err
	}
	cs.HeadSHA = strings.TrimSpace(head)
	// The commit must sit directly on the base.
	parent, err := git(ctx, tr, "rev-parse", "--verify", cs.HeadSHA+"^1")
	if err != nil || strings.TrimSpace(parent) != st.BaseSHA {
		return Changeset{}, errors.New("the sandbox commit is not based on the workspace's base commit")
	}
	return m.describe(ctx, tr, cs, req.MaxPatchBytes)
}

// describe fills a changeset from the trusted repository.
func (m *Manager) describe(ctx context.Context, tr string, cs Changeset, maxPatch int) (Changeset, error) {
	if maxPatch <= 0 {
		maxPatch = 400 << 10
	}
	msg, _ := git(ctx, tr, "log", "-1", "--format=%B", cs.HeadSHA)
	cs.Message = strings.TrimSpace(msg)
	ns, err := git(ctx, tr, "diff", "--no-ext-diff", "--no-textconv", "-M", "--numstat", "-z", cs.BaseSHA, cs.HeadSHA)
	if err != nil {
		return cs, err
	}
	st, err := git(ctx, tr, "diff", "--no-ext-diff", "-M", "--name-status", "-z", cs.BaseSHA, cs.HeadSHA)
	if err != nil {
		return cs, err
	}
	status := parseNameStatus(st)
	for _, fc := range parseNumstat(ns) {
		if s, ok := status[fc.Path]; ok {
			fc.Status = s
		} else {
			fc.Status = "modified"
		}
		fc.Secret = secretguard.LooksSecret(fc.Path)
		cs.Additions += fc.Additions
		cs.Deletions += fc.Deletions
		cs.Files = append(cs.Files, fc)
	}
	patch, err := git(ctx, tr, "diff", "--no-ext-diff", "--no-textconv", "-M", "--no-color", cs.BaseSHA, cs.HeadSHA)
	if err != nil {
		return cs, err
	}
	if secretguard.HasToken(patch) {
		cs.SecretFound = true
		patch = secretguard.Redact(patch)
	}
	for i := range cs.Files {
		if cs.Files[i].Secret {
			cs.SecretFound = true
		}
	}
	if len(patch) > maxPatch {
		cut := strings.LastIndex(patch[:maxPatch], "\ndiff --git ")
		if cut <= 0 {
			cut = maxPatch
		}
		patch, cs.Truncated = patch[:cut], true
	}
	cs.Patch = patch
	return cs, nil
}

func parseNumstat(s string) []FileChange {
	var out []FileChange
	parts := strings.Split(s, "\x00")
	for i := 0; i < len(parts); i++ {
		p := parts[i]
		if p == "" {
			continue
		}
		f := strings.SplitN(p, "\t", 3)
		if len(f) < 3 {
			continue
		}
		fc := FileChange{}
		if f[0] == "-" {
			fc.Binary = true
		} else {
			fc.Additions, _ = strconv.Atoi(f[0])
			fc.Deletions, _ = strconv.Atoi(f[1])
		}
		if f[2] == "" && i+2 < len(parts) { // rename: old\0new
			fc.OldPath, fc.Path = parts[i+1], parts[i+2]
			i += 2
		} else {
			fc.Path = f[2]
		}
		out = append(out, fc)
	}
	return out
}

func parseNameStatus(s string) map[string]string {
	names := map[byte]string{'A': "added", 'M': "modified", 'D': "deleted", 'R': "renamed", 'C': "copied", 'T': "type_changed"}
	out := map[string]string{}
	parts := strings.Split(s, "\x00")
	for i := 0; i < len(parts); i++ {
		code := parts[i]
		if code == "" {
			continue
		}
		st := names[code[0]]
		if st == "" {
			st = "modified"
		}
		if code[0] == 'R' || code[0] == 'C' {
			if i+2 < len(parts) {
				out[parts[i+2]] = st
			}
			i += 2
			continue
		}
		if i+1 < len(parts) {
			out[parts[i+1]] = st
		}
		i++
	}
	return out
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// DeliverRequest writes an approved commit into the user's repository.
type DeliverRequest struct {
	SHA    string `json:"sha"`
	Branch string `json:"branch,omitempty"` // defaults to the workspace branch
}

// Delivery is where an approved commit landed.
type Delivery struct {
	Branch   string `json:"branch"`
	SHA      string `json:"sha"`
	RepoPath string `json:"repoPath"`
	Existed  bool   `json:"existed,omitempty"` // the branch already pointed at this commit
}

// Deliver creates a branch in the user's repository at an approved commit
// from the trusted repository. It never moves an existing branch: when the
// name is taken by other work it adds a suffix. The user's checkout, index
// and working tree are untouched.
func (m *Manager) Deliver(ctx context.Context, ws string, req DeliverRequest) (Delivery, error) {
	defer m.lock(ws)()
	st, err := m.load(ws)
	if err != nil {
		return Delivery{}, err
	}
	if !shaRe.MatchString(req.SHA) {
		return Delivery{}, bad("sha must be a full commit id")
	}
	branch := req.Branch
	if branch == "" {
		branch = st.Branch
	}
	if !ValidBranch(ctx, branch) {
		return Delivery{}, bad("invalid branch name %q", branch)
	}
	tr := m.trusted(ws)
	snap, err := git(ctx, tr, "rev-parse", "--verify", "-q", "refs/wise/snapshot^{commit}")
	if err != nil || strings.TrimSpace(snap) != req.SHA {
		return Delivery{}, bad("commit %s is not the workspace's verified snapshot", short(req.SHA))
	}
	repo, err := m.checkRepo(st.RepoPath)
	if err != nil {
		return Delivery{}, err
	}
	if _, err := git(ctx, repo, "fetch", "-q", "--no-tags", "--no-write-fetch-head", tr, req.SHA); err != nil {
		return Delivery{}, fmt.Errorf("bring the commit into the repository: %w", err)
	}
	for i := 1; i <= 20; i++ {
		name := branch
		if i > 1 {
			name = fmt.Sprintf("%s-%d", branch, i)
		}
		cur, err := git(ctx, repo, "rev-parse", "--verify", "-q", "refs/heads/"+name)
		if err == nil {
			if strings.TrimSpace(cur) == req.SHA {
				return Delivery{Branch: name, SHA: req.SHA, RepoPath: repo, Existed: true}, nil
			}
			continue // taken by other work: never overwrite
		}
		// Create only if absent (the zero old value makes this atomic).
		if _, err := git(ctx, repo, "update-ref", "-m", "wise: deliver task branch", "refs/heads/"+name, req.SHA, strings.Repeat("0", len(req.SHA))); err != nil {
			continue
		}
		return Delivery{Branch: name, SHA: req.SHA, RepoPath: repo}, nil
	}
	return Delivery{}, fmt.Errorf("branch %s and its numbered variants are all taken", branch)
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// ---- egress allowlist ----

func (m *Manager) egressFile() string { return filepath.Join(m.cfg.Dir, ".egress.json") }

func (m *Manager) loadEgress() {
	data, err := os.ReadFile(m.egressFile())
	if err != nil {
		return
	}
	var hosts []string
	if json.Unmarshal(data, &hosts) == nil {
		m.egress = hosts
	}
}

// SetEgress replaces the hosts sandboxes may reach (kept across restarts).
func (m *Manager) SetEgress(hosts []string) error {
	clean := make([]string, 0, len(hosts))
	for _, h := range hosts {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" {
			continue
		}
		if !hostPattern.MatchString(h) {
			return bad("invalid host %q", h)
		}
		clean = append(clean, h)
	}
	m.egressMu.Lock()
	m.egress = clean
	m.egressMu.Unlock()
	data, _ := json.Marshal(clean)
	return os.WriteFile(m.egressFile(), data, 0o600)
}

// Egress returns the hosts sandboxes may reach.
func (m *Manager) Egress() []string {
	m.egressMu.RLock()
	defer m.egressMu.RUnlock()
	return append([]string(nil), m.egress...)
}

// hostPattern accepts host names and "*." wildcards (no IPs, no ports).
var hostPattern = regexp.MustCompile(`^(\*\.)?([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)

// ValidHost reports whether h is an acceptable allowlist entry.
func ValidHost(h string) bool { return hostPattern.MatchString(strings.ToLower(strings.TrimSpace(h))) }

// WorkingDiff is the workspace's current changes against its base, computed
// in the sandbox: untrusted, for agents to review their own work (approvals
// use the verified Snapshot).
type WorkingDiff struct {
	Stat      string `json:"stat"`
	Patch     string `json:"patch"`
	Truncated bool   `json:"truncated,omitempty"`
}

// Diff returns the workspace's changes since its base.
func (m *Manager) Diff(ctx context.Context, ws string, maxPatch int) (WorkingDiff, error) {
	st, err := m.load(ws)
	if err != nil {
		return WorkingDiff{}, err
	}
	if !st.Prepared {
		return WorkingDiff{}, bad("the workspace is not ready")
	}
	if err := m.rt.Ensure(ctx, ws); err != nil {
		return WorkingDiff{}, err
	}
	if maxPatch <= 0 {
		maxPatch = 200 << 10
	}
	var out bytes.Buffer
	if code, err := m.sandboxGit(ctx, ws, nil, &out, "add", "-A"); err != nil || code != 0 {
		return WorkingDiff{}, fmt.Errorf("git add failed in the sandbox: %v %s", err, clip(out.String(), 300))
	}
	out.Reset()
	if _, err := m.sandboxGit(ctx, ws, nil, &out, "diff", "--cached", "--no-ext-diff", "--stat", st.BaseSHA); err != nil {
		return WorkingDiff{}, err
	}
	d := WorkingDiff{Stat: secretguard.Redact(out.String())}
	var patch bytes.Buffer
	if _, err := m.rt.Exec(ctx, ws, []string{"git", "-c", "core.fsmonitor=false", "diff", "--cached", "--no-ext-diff", "--no-textconv", "--no-color", st.BaseSHA},
		nil, nil, &limitWriter{w: &patch, n: maxPatch + 1}); err != nil {
		return WorkingDiff{}, err
	}
	p := patch.String()
	if len(p) > maxPatch {
		p, d.Truncated = p[:maxPatch], true
	}
	d.Patch = secretguard.Redact(p)
	return d, nil
}
