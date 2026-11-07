// Package gitinspect validates local repository paths and reads Git metadata
// using the git CLI. Commands run without a shell and with a timeout.
package gitinspect

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

var (
	ErrNoRoots     = errors.New("no repository roots are configured (set WISE_REPO_ROOTS)")
	ErrOutsideRoot = errors.New("path is outside the allowed repository roots")
	ErrNotAbsolute = errors.New("path must be absolute")
	ErrNotGitRepo  = errors.New("path is not a git repository")
)

// Remote is a configured git remote.
type Remote struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// Info is the Git metadata of a working tree.
type Info struct {
	CurrentBranch string // empty when HEAD is detached
	DefaultBranch string // best effort: origin/HEAD, else current branch
	HeadSHA       string // empty for a repo without commits
	Remotes       []Remote
}

type Inspector struct {
	roots   []string
	timeout time.Duration
	gitBin  string
}

// New returns an Inspector restricted to roots (absolute paths).
func New(roots []string) *Inspector {
	resolved := make([]string, 0, len(roots))
	for _, r := range roots {
		if rr, err := filepath.EvalSymlinks(r); err == nil {
			r = rr
		}
		resolved = append(resolved, filepath.Clean(r))
	}
	return &Inspector{roots: resolved, timeout: 10 * time.Second, gitBin: "git"}
}

// Roots returns the configured roots.
func (i *Inspector) Roots() []string { return append([]string(nil), i.roots...) }

// ResolvePath cleans path, resolves symlinks and ensures the result lies
// within an allowed root and is a git working tree root.
func (i *Inspector) ResolvePath(path string) (string, error) {
	if len(i.roots) == 0 {
		return "", ErrNoRoots
	}
	if !filepath.IsAbs(path) {
		return "", ErrNotAbsolute
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("path does not exist: %s", path)
		}
		return "", fmt.Errorf("resolve path: %w", err)
	}
	if !i.withinRoots(resolved) {
		return "", ErrOutsideRoot
	}
	st, err := os.Stat(resolved)
	if err != nil || !st.IsDir() {
		return "", fmt.Errorf("path is not a directory: %s", path)
	}
	if _, err := os.Stat(filepath.Join(resolved, ".git")); err != nil {
		return "", ErrNotGitRepo
	}
	return resolved, nil
}

func (i *Inspector) withinRoots(p string) bool {
	for _, root := range i.roots {
		rel, err := filepath.Rel(root, p)
		if err != nil {
			continue
		}
		if rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
			return true
		}
	}
	return false
}

// Inspect reads Git metadata for the working tree at path (validated first).
func (i *Inspector) Inspect(ctx context.Context, path string) (Info, error) {
	dir, err := i.ResolvePath(path)
	if err != nil {
		return Info{}, err
	}
	var info Info

	if out, err := i.git(ctx, dir, "symbolic-ref", "--quiet", "--short", "HEAD"); err == nil {
		info.CurrentBranch = out
	}
	if out, err := i.git(ctx, dir, "rev-parse", "--verify", "--quiet", "HEAD^{commit}"); err == nil {
		info.HeadSHA = out
	}
	if out, err := i.git(ctx, dir, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"); err == nil {
		info.DefaultBranch = strings.TrimPrefix(out, "origin/")
	} else {
		info.DefaultBranch = info.CurrentBranch
	}

	out, err := i.git(ctx, dir, "config", "--get-regexp", `^remote\..*\.url$`)
	if err == nil && out != "" {
		for _, line := range strings.Split(out, "\n") {
			key, url, ok := strings.Cut(line, " ")
			if !ok {
				continue
			}
			name := strings.TrimSuffix(strings.TrimPrefix(key, "remote."), ".url")
			info.Remotes = append(info.Remotes, Remote{Name: name, URL: redactURL(url)})
		}
		sort.Slice(info.Remotes, func(a, b int) bool { return info.Remotes[a].Name < info.Remotes[b].Name })
	}
	return info, nil
}

func (i *Inspector) git(ctx context.Context, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, i.timeout)
	defer cancel()
	full := append([]string{"-C", dir, "-c", "safe.directory=" + dir}, args...)
	cmd := exec.CommandContext(ctx, i.gitBin, full...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// redactURL removes credentials embedded in remote URLs (https://user:pass@host).
func redactURL(raw string) string {
	scheme, rest, ok := strings.Cut(raw, "://")
	if !ok {
		return raw
	}
	at := strings.LastIndex(rest, "@")
	slash := strings.Index(rest, "/")
	if at >= 0 && (slash < 0 || at < slash) {
		return scheme + "://" + rest[at+1:]
	}
	return raw
}
