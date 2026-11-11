package gitworker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"wise/apps/api/internal/release"
)

const token = "github_pat_TEST_0123456789abcdef"

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t", "GIT_CONFIG_GLOBAL=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

type fixture struct {
	w        *Worker
	sha      string
	other    string
	ws       uuid.UUID
	remote   string
	base     string
	gitRoot  string
	mu       sync.Mutex
	results  []map[string]any
	grantFor func(approval string) (release.Grant, int)
}

func setup(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{ws: uuid.New()}
	root := t.TempDir()
	// The workspace's trusted repository with the approved commit.
	src := filepath.Join(root, "src")
	_ = os.MkdirAll(src, 0o755)
	run(t, src, "init", "-q", "-b", "main")
	run(t, src, "commit", "-q", "--allow-empty", "-m", "base")
	_ = os.WriteFile(filepath.Join(src, "a.txt"), []byte("approved\n"), 0o644)
	run(t, src, "add", "a.txt")
	run(t, src, "commit", "-q", "-m", "approved change")
	f.sha = run(t, src, "rev-parse", "HEAD")
	_ = os.WriteFile(filepath.Join(src, "a.txt"), []byte("other\n"), 0o644)
	run(t, src, "commit", "-q", "-am", "other work")
	f.other = run(t, src, "rev-parse", "HEAD")
	wsDir := filepath.Join(root, "workspaces", f.ws.String())
	_ = os.MkdirAll(wsDir, 0o755)
	run(t, root, "clone", "-q", "--bare", src, filepath.Join(wsDir, "trusted.git"))
	// The "GitHub" remote: git http-backend over TLS, requiring the token.
	f.gitRoot = filepath.Join(root, "remotes")
	_ = os.MkdirAll(filepath.Join(f.gitRoot, "o"), 0o755)
	run(t, root, "clone", "-q", "--bare", src, filepath.Join(f.gitRoot, "o", "r.git"))
	run(t, filepath.Join(f.gitRoot, "o", "r.git"), "update-ref", "-d", "refs/heads/main") // only base below
	run(t, filepath.Join(f.gitRoot, "o", "r.git"), "update-ref", "refs/heads/main", f.sha+"~1")
	backend, _ := exec.Command("git", "--exec-path").Output()
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+token))
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != want {
			w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
			w.WriteHeader(401)
			return
		}
		h := &cgi.Handler{Path: filepath.Join(strings.TrimSpace(string(backend)), "git-http-backend"),
			Env: []string{"GIT_PROJECT_ROOT=" + f.gitRoot, "GIT_HTTP_EXPORT_ALL=1", "REMOTE_USER=wise", "GIT_CONFIG_GLOBAL=/dev/null"}}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "https://")
	f.base = "https://" + host
	f.remote = f.base + "/o/r.git"
	ca := filepath.Join(root, "ca.pem")
	_ = os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o644)
	// The API: grants and results.
	f.grantFor = func(string) (release.Grant, int) {
		return release.Grant{PushID: uuid.New(), WorkspaceID: f.ws, SHA: f.sha, Branch: "wise/add-x-1234", Remote: f.remote, BaseBranch: "main",
			Kind: "github", BaseURL: f.base, Token: token, TimeoutS: 60}, 200
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer api-token" {
			w.WriteHeader(401)
			return
		}
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v1/internal/git/push-grants/"):
			g, st := f.grantFor(strings.TrimPrefix(r.URL.Path, "/api/v1/internal/git/push-grants/"))
			if st != 200 {
				w.WriteHeader(st)
				_ = json.NewEncoder(w).Encode(map[string]string{"detail": "the push is not approved (rejected)"})
				return
			}
			_ = json.NewEncoder(w).Encode(g)
		case strings.HasSuffix(r.URL.Path, "/result"):
			var m map[string]any
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &m)
			f.mu.Lock()
			f.results = append(f.results, m)
			f.mu.Unlock()
			_, _ = w.Write([]byte(`{"recorded":true}`))
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(api.Close)
	f.w = New(Config{APIURL: api.URL + "/api/v1", APIToken: "api-token", WorkspacesDir: filepath.Join(root, "workspaces"),
		ExtraGitConfig: []string{"http.sslCAInfo=" + ca}}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return f
}

func (f *fixture) remoteRef(t *testing.T, branch string) string {
	out, err := exec.Command("git", "-C", filepath.Join(f.gitRoot, "o", "r.git"), "rev-parse", "-q", "--verify", "refs/heads/"+branch).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func TestPushCreatesOnlyTheApprovedBranch(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	res, err := f.w.Push(ctx, uuid.New())
	if err != nil || res.Existed {
		t.Fatalf("push: %+v %v", res, err)
	}
	if got := f.remoteRef(t, "wise/add-x-1234"); got != f.sha {
		t.Fatalf("remote branch at %s, want %s", got, f.sha)
	}
	// A retry finds the branch already at the commit: counted as pushed, nothing moved.
	if res, err := f.w.Push(ctx, uuid.New()); err != nil || !res.Existed {
		t.Fatalf("retry: %+v %v", res, err)
	}
	// Another commit to the same branch is refused (create-only, never moved).
	f.grantFor = func(string) (release.Grant, int) {
		g := release.Grant{PushID: uuid.New(), WorkspaceID: f.ws, SHA: f.other, Branch: "wise/add-x-1234", Remote: f.remote, BaseBranch: "main",
			Kind: "github", BaseURL: f.base, Token: token, TimeoutS: 60}
		return g, 200
	}
	if _, err := f.w.Push(ctx, uuid.New()); !errors.Is(err, ErrBranchExists) {
		t.Fatalf("existing branch: %v", err)
	}
	if got := f.remoteRef(t, "wise/add-x-1234"); got != f.sha {
		t.Fatal("the existing branch was moved")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.results) != 3 || f.results[0]["ok"] != true || f.results[2]["ok"] != false {
		t.Fatalf("reported: %v", f.results)
	}
	for _, r := range f.results {
		b, _ := json.Marshal(r)
		if strings.Contains(string(b), token) {
			t.Fatal("the token appears in a reported result")
		}
	}
}

func TestPushRefusesWhatTheRulesForbid(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	for name, mutate := range map[string]func(*release.Grant){
		"main":        func(g *release.Grant) { g.Branch = "main" },
		"base":        func(g *release.Grant) { g.Branch, g.BaseBranch = "wise/x", "wise/x" },
		"force spec":  func(g *release.Grant) { g.Branch = "+wise/x" },
		"other host":  func(g *release.Grant) { g.BaseURL = "https://github.com" },
		"short sha":   func(g *release.Grant) { g.SHA = g.SHA[:12] },
		"refs inject": func(g *release.Grant) { g.Branch = "wise/x:refs/heads/main" },
	} {
		f.grantFor = func(string) (release.Grant, int) {
			g := release.Grant{PushID: uuid.New(), WorkspaceID: f.ws, SHA: f.sha, Branch: "wise/ok", Remote: f.remote, BaseBranch: "main",
				Kind: "github", BaseURL: f.base, Token: token, TimeoutS: 60}
			mutate(&g)
			return g, 200
		}
		if _, err := f.w.Push(ctx, uuid.New()); err == nil {
			t.Errorf("%s: pushed", name)
		}
	}
	if f.remoteRef(t, "main") == f.sha {
		t.Fatal("main was moved")
	}
	// Without a grant (unapproved, rejected or used approval) nothing is pushed.
	f.grantFor = func(string) (release.Grant, int) { return release.Grant{}, 409 }
	var re *RefusedError
	if _, err := f.w.Push(ctx, uuid.New()); !errors.As(err, &re) || !strings.Contains(err.Error(), "not approved") {
		t.Fatalf("refusal: %v", err)
	}
	// A wrong token is reported as an auth problem, without the token.
	f.grantFor = func(string) (release.Grant, int) {
		return release.Grant{PushID: uuid.New(), WorkspaceID: f.ws, SHA: f.sha, Branch: "wise/auth", Remote: f.remote, BaseBranch: "main",
			Kind: "github", BaseURL: f.base, Token: "wrong-token-value", TimeoutS: 60}, 200
	}
	if _, err := f.w.Push(ctx, uuid.New()); err == nil || strings.Contains(err.Error(), "wrong-token-value") {
		t.Fatalf("auth: %v", err)
	}
}
