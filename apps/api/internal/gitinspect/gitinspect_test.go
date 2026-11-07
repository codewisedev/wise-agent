package gitinspect

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t", "GIT_CONFIG_GLOBAL=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// NewTestRepo creates root/name as a git repo with one commit and a remote.
func newTestRepo(t *testing.T, root, name string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, dir, "init", "-q", "-b", "trunk")
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, dir, "add", ".")
	run(t, dir, "commit", "-q", "-m", "init")
	run(t, dir, "remote", "add", "origin", "https://user:secret@example.com/org/repo.git")
	return dir
}

func TestInspect(t *testing.T) {
	root := t.TempDir()
	dir := newTestRepo(t, root, "svc")
	in := New([]string{root})
	info, err := in.Inspect(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.CurrentBranch != "trunk" || info.DefaultBranch != "trunk" {
		t.Errorf("branches: %+v", info)
	}
	if len(info.HeadSHA) != 40 {
		t.Errorf("head sha: %q", info.HeadSHA)
	}
	if len(info.Remotes) != 1 || info.Remotes[0].URL != "https://example.com/org/repo.git" {
		t.Errorf("remotes not redacted: %+v", info.Remotes)
	}
}

func TestResolvePathRejections(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	repo := newTestRepo(t, root, "ok")
	notRepo := filepath.Join(root, "plain")
	_ = os.Mkdir(notRepo, 0o755)
	outsideRepo := newTestRepo(t, outside, "evil")
	link := filepath.Join(root, "link")
	if err := os.Symlink(outsideRepo, link); err != nil {
		t.Fatal(err)
	}
	in := New([]string{root})

	if got, err := in.ResolvePath(repo + "/"); err != nil || got != filepath.Clean(mustEval(t, repo)) {
		t.Fatalf("valid repo: %q %v", got, err)
	}
	cases := map[string]error{
		"relative/path":            ErrNotAbsolute,
		outsideRepo:                ErrOutsideRoot,
		link:                       ErrOutsideRoot, // symlink escaping the root
		root + "/ok/../../" + "x":  nil,            // does not exist -> generic error
		notRepo:                    ErrNotGitRepo,
		root + "/ok/../../outside": nil,
	}
	for p, want := range cases {
		_, err := in.ResolvePath(p)
		if err == nil {
			t.Errorf("%s: expected error", p)
			continue
		}
		if want != nil && !errors.Is(err, want) {
			t.Errorf("%s: got %v, want %v", p, err, want)
		}
	}
	if _, err := New(nil).ResolvePath(repo); !errors.Is(err, ErrNoRoots) {
		t.Errorf("no roots: %v", err)
	}
}

func mustEval(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRedactURL(t *testing.T) {
	cases := map[string]string{
		"https://u:p@host/x.git":   "https://host/x.git",
		"https://host/x.git":       "https://host/x.git",
		"git@github.com:org/x.git": "git@github.com:org/x.git",
		"ssh://git@host/x.git":     "ssh://host/x.git",
		"https://host/path@weird":  "https://host/path@weird",
	}
	for in, want := range cases {
		if got := redactURL(in); got != want {
			t.Errorf("redactURL(%q) = %q, want %q", in, got, want)
		}
	}
}
