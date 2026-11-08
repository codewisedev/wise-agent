package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wise/apps/api/internal/accounts"
	"wise/apps/api/internal/agents"
	"wise/apps/api/internal/auth"
	"wise/apps/api/internal/db"
	"wise/apps/api/internal/events"
	"wise/apps/api/internal/gitinspect"
	"wise/apps/api/internal/httpapi"
	"wise/apps/api/internal/integrations"
	"wise/apps/api/internal/modelgateway"
	"wise/apps/api/internal/projects"
	"wise/apps/api/internal/repositories"
	"wise/apps/api/internal/runqueue"
	"wise/apps/api/internal/secrets"
	"wise/apps/api/internal/settings"
	"wise/apps/api/internal/tasks"
	"wise/apps/api/internal/testutil"
)

const (
	adminUser = "admin"
	adminPass = "a-very-long-password"
)

type env struct {
	t        *testing.T
	db       *db.DB
	srv      *httptest.Server
	client   *http.Client
	csrf     string
	bearer   string
	repoRoot string
	queue    *runqueue.Service
	model    *scriptedProvider
	tasks    *tasks.Service
	agents   *agents.Service
	hub      *events.Hub
	accounts *accounts.Service
	gw       *modelgateway.Gateway
	sources  *integrations.Service
	settings *settings.Service
}

func setup(t *testing.T) *env {
	t.Helper()
	d := testutil.NewDB(t)
	root := t.TempDir()
	authSvc := auth.NewService(d, time.Hour)
	if _, err := authSvc.Bootstrap(context.Background(), adminUser, adminPass); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	catalog, err := agents.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := agents.Sync(context.Background(), d, catalog, log); err != nil {
		t.Fatal(err)
	}
	model := &scriptedProvider{}
	hub := events.NewHub(d)
	gw := modelgateway.New(d, modelgateway.Config{
		Providers: map[string]modelgateway.ProviderConfig{"scripted": {Type: "fake", Cloud: true}},
		Aliases: map[string]modelgateway.AliasConfig{
			"reasoning": {Targets: []modelgateway.Target{{Provider: "scripted", Model: "test-model"}}},
			"local":     {Targets: []modelgateway.Target{{Provider: "scripted", Model: "test-model"}}},
		},
	}, modelgateway.Policy{CloudAllowedAgents: []string{"*"}, MaxRetries: 1}, hub, log).WithProvider(model)
	queue := runqueue.NewService(d, hub, gw, catalog.Tools, log)
	agentSvc := agents.NewService(d, hub).WithTools(catalog.Tools)
	taskSvc := tasks.NewService(d, tasks.DefaultWorkflowConfig())
	projectSvc := projects.NewService(d)
	projectSvc.OnCreate = taskSvc.SeedColumns
	taskSvc.Runs = agentSvc
	box, err := secrets.New("MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=") // test-only key
	if err != nil {
		t.Fatal(err)
	}
	accountSvc := accounts.NewService(d, box, gw, log)
	gw.WithSecretSaver(accountSvc.SaveSecret)
	taskSvc.Agents = tasks.LookupFrom(agentSvc.Lookup, func(i agents.Info) tasks.AgentInfo { return tasks.AgentInfo(i) })
	taskSvc.Accounts = accountSvc.ValidateModelChoice
	if err := taskSvc.SeedTemplates(context.Background(), log); err != nil {
		t.Fatal(err)
	}
	integrationSvc := integrations.NewService(d, accountSvc, taskSvc, log)
	settingsSvc := settings.NewService(d, log)
	settings.RegisterDefaults(settingsSvc, testSettingFiles(t), gw, accountSvc, taskSvc)
	if err := settingsSvc.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	server := &httpapi.Server{
		DB: d, Auth: authSvc, Projects: projectSvc,
		Repositories: repositories.NewService(d, gitinspect.New([]string{root})),
		Agents:       agentSvc, RunQueue: queue, Gateway: gw, Hub: hub, Tasks: taskSvc, Accounts: accountSvc, Integrations: integrationSvc, Settings: settingsSvc,
		Cookie:       httpapi.CookieConfig{TTL: time.Hour},
		RuntimeToken: runtimeToken,
	}
	h, err := httpapi.NewRouter(server, httpapi.RouterOptions{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	jar, _ := cookiejar.New(nil)
	return &env{t: t, db: d, srv: ts, client: &http.Client{Jar: jar}, repoRoot: root, queue: queue, model: model,
		tasks: taskSvc, agents: agentSvc, hub: hub, accounts: accountSvc, gw: gw, sources: integrationSvc, settings: settingsSvc}
}

type resp struct {
	status int
	body   map[string]any
}

func (e *env) do(method, path string, body any) resp {
	e.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.srv.URL+httpapi.BasePath+path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if e.csrf != "" {
		req.Header.Set(auth.CSRFHeader, e.csrf)
	}
	if e.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+e.bearer)
	}
	res, err := e.client.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	out := resp{status: res.StatusCode, body: map[string]any{}}
	raw, _ := io.ReadAll(res.Body)
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out.body)
	}
	return out
}

func (e *env) expect(r resp, status int) map[string]any {
	e.t.Helper()
	if r.status != status {
		e.t.Fatalf("status %d, want %d: %v", r.status, status, r.body)
	}
	return r.body
}

func (e *env) login() {
	e.t.Helper()
	body := e.expect(e.do("POST", "/auth/login", map[string]string{"username": adminUser, "password": adminPass}), 200)
	e.csrf = body["csrfToken"].(string)
}

func gitRepo(t *testing.T, root, name string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"commit", "-q", "--allow-empty", "-m", "init"}} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t", "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	return dir
}

func TestHealthIsPublic(t *testing.T) {
	e := setup(t)
	e.expect(e.do("GET", "/healthz", nil), 200)
	e.expect(e.do("GET", "/readyz", nil), 200)
	e.expect(e.do("GET", "/projects", nil), 401)
}

func TestAuthFlow(t *testing.T) {
	e := setup(t)
	e.expect(e.do("POST", "/auth/login", map[string]string{"username": adminUser, "password": "wrong-password"}), 401)
	e.expect(e.do("POST", "/auth/login", map[string]string{"username": "ghost", "password": "whatever"}), 401)

	e.login()
	me := e.expect(e.do("GET", "/auth/me", nil), 200)
	if me["authMethod"] != "session" || me["user"].(map[string]any)["username"] != adminUser {
		t.Fatalf("unexpected me: %v", me)
	}

	// CSRF is enforced for cookie-authenticated mutations.
	csrf := e.csrf
	e.csrf = ""
	e.expect(e.do("POST", "/projects", map[string]string{"name": "x"}), 403)
	e.csrf = "wrong"
	e.expect(e.do("POST", "/projects", map[string]string{"name": "x"}), 403)
	e.csrf = csrf

	// API tokens.
	created := e.expect(e.do("POST", "/auth/tokens", map[string]string{"name": "ci"}), 201)
	secret := created["secret"].(string)
	tokenID := created["token"].(map[string]any)["id"].(string)

	e.expect(e.do("POST", "/auth/logout", nil), 204)
	e.expect(e.do("GET", "/auth/me", nil), 401)

	e.bearer = secret
	me = e.expect(e.do("GET", "/auth/me", nil), 200)
	if me["authMethod"] != "api_token" || me["csrfToken"] != nil {
		t.Fatalf("unexpected bearer me: %v", me)
	}
	// Bearer requests do not need CSRF.
	e.expect(e.do("POST", "/projects", map[string]string{"name": "Via Token"}), 201)
	e.expect(e.do("DELETE", "/auth/tokens/"+tokenID, nil), 204)
	e.expect(e.do("GET", "/auth/me", nil), 401)
}

func TestLoginRateLimit(t *testing.T) {
	e := setup(t)
	for range 10 {
		e.expect(e.do("POST", "/auth/login", map[string]string{"username": adminUser, "password": "nope-nope"}), 401)
	}
	e.expect(e.do("POST", "/auth/login", map[string]string{"username": adminUser, "password": adminPass}), 429)
}

func TestProjectsAndRepositories(t *testing.T) {
	e := setup(t)
	e.login()

	p := e.expect(e.do("POST", "/projects", map[string]any{"name": "Salon Booking Platform", "metadata": map[string]any{"team": "me"}}), 201)
	pid := p["id"].(string)
	if p["slug"] != "salon-booking-platform" {
		t.Fatalf("slug: %v", p["slug"])
	}
	e.expect(e.do("POST", "/projects", map[string]any{"name": "Again", "slug": "salon-booking-platform"}), 409)
	e.expect(e.do("POST", "/projects", map[string]any{"name": ""}), 400)

	upd := e.expect(e.do("PATCH", "/projects/"+pid, map[string]any{"description": "Bookings"}), 200)
	if upd["description"] != "Bookings" || upd["name"] != "Salon Booking Platform" {
		t.Fatalf("patch: %v", upd)
	}

	// Local repository inside the allowed root.
	dir := gitRepo(t, e.repoRoot, "backend")
	repo := e.expect(e.do("POST", "/projects/"+pid+"/repositories", map[string]any{
		"name": "backend", "role": "backend", "sourceType": "local", "localPath": dir,
	}), 201)
	rid := repo["id"].(string)
	if sha, _ := repo["headSha"].(string); len(sha) != 40 || repo["currentBranch"] != "main" {
		t.Fatalf("git info missing: %v", repo)
	}

	// Path outside the root and non-repo paths are rejected.
	outside := gitRepo(t, t.TempDir(), "evil")
	e.expect(e.do("POST", "/projects/"+pid+"/repositories", map[string]any{"name": "evil", "sourceType": "local", "localPath": outside}), 400)
	e.expect(e.do("POST", "/projects/"+pid+"/repositories", map[string]any{"name": "rel", "sourceType": "local", "localPath": "../x"}), 400)
	e.expect(e.do("POST", "/projects/"+pid+"/repositories", map[string]any{"name": "bad", "sourceType": "nope"}), 400)

	// Remote repository + duplicates.
	fe := e.expect(e.do("POST", "/projects/"+pid+"/repositories", map[string]any{
		"name": "frontend", "role": "frontend", "sourceType": "remote", "remoteUrl": "git@github.com:org/fe.git",
	}), 201)
	feID := fe["id"].(string)
	e.expect(e.do("POST", "/projects/"+pid+"/repositories", map[string]any{"name": "frontend", "sourceType": "remote", "remoteUrl": "git@github.com:org/fe.git"}), 409)
	e.expect(e.do("POST", "/projects/"+pid+"/repositories", map[string]any{"name": "creds", "sourceType": "remote", "remoteUrl": "https://u:p@github.com/x"}), 400)
	e.expect(e.do("POST", "/repositories/"+feID+"/inspect", nil), 422)

	list := e.expect(e.do("GET", "/projects/"+pid+"/repositories", nil), 200)
	if n := len(list["items"].([]any)); n != 2 {
		t.Fatalf("want 2 repos, got %d", n)
	}

	// Relationships.
	rel := e.expect(e.do("POST", "/projects/"+pid+"/repository-relationships", map[string]any{
		"fromRepositoryId": feID, "toRepositoryId": rid, "kind": "consumes_api",
	}), 201)
	e.expect(e.do("POST", "/projects/"+pid+"/repository-relationships", map[string]any{
		"fromRepositoryId": feID, "toRepositoryId": rid, "kind": "consumes_api",
	}), 409)
	e.expect(e.do("POST", "/projects/"+pid+"/repository-relationships", map[string]any{
		"fromRepositoryId": rid, "toRepositoryId": rid, "kind": "depends_on",
	}), 400)

	// Repo in a different project cannot be related.
	other := e.expect(e.do("POST", "/projects", map[string]any{"name": "Other"}), 201)
	otherRepo := e.expect(e.do("POST", "/projects/"+other["id"].(string)+"/repositories", map[string]any{
		"name": "x", "sourceType": "remote", "remoteUrl": "https://github.com/o/x.git",
	}), 201)
	e.expect(e.do("POST", "/projects/"+pid+"/repository-relationships", map[string]any{
		"fromRepositoryId": rid, "toRepositoryId": otherRepo["id"], "kind": "depends_on",
	}), 400)
	e.expect(e.do("DELETE", "/repository-relationships/"+rel["id"].(string), nil), 204)

	// Index request and status.
	st := e.expect(e.do("POST", "/repositories/"+rid+"/index", nil), 202)
	if st["indexStatus"] != "queued" {
		t.Fatalf("index: %v", st)
	}
	st = e.expect(e.do("GET", "/repositories/"+rid+"/status", nil), 200)
	if st["indexStatus"] != "queued" {
		t.Fatalf("status: %v", st)
	}

	// Inspect records an error when the directory disappears.
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	insp := e.expect(e.do("POST", "/repositories/"+rid+"/inspect", nil), 200)
	if insp["gitError"] == nil || insp["headSha"] == nil {
		t.Fatalf("expected gitError and preserved headSha: %v", insp)
	}

	// Resources.
	res := e.expect(e.do("POST", "/projects/"+pid+"/resources", map[string]any{"kind": "documentation", "name": "PRD", "uri": "https://docs"}), 201)
	e.expect(e.do("POST", "/projects/"+pid+"/resources", map[string]any{"kind": "spaceship", "name": "x"}), 400)
	e.expect(e.do("DELETE", "/project-resources/"+res["id"].(string), nil), 204)

	// Archiving hides the project and blocks new children.
	e.expect(e.do("DELETE", "/projects/"+pid, nil), 204)
	e.expect(e.do("DELETE", "/projects/"+pid, nil), 404)
	projectsList := e.expect(e.do("GET", "/projects", nil), 200)
	for _, it := range projectsList["items"].([]any) {
		if it.(map[string]any)["id"] == pid {
			t.Fatal("archived project should be hidden by default")
		}
	}
	e.expect(e.do("POST", "/projects/"+pid+"/repositories", map[string]any{"name": "late", "sourceType": "remote", "remoteUrl": "https://github.com/o/l.git"}), 404)
	e.expect(e.do("PATCH", "/projects/"+pid, map[string]any{"name": "nope"}), 404)

	e.expect(e.do("DELETE", "/repositories/"+feID, nil), 204)
	e.expect(e.do("GET", "/repositories/"+feID, nil), 404)

	// Every mutation was audited.
	var count int
	if err := e.db.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_logs WHERE entity_type = 'repository' AND entity_id = $1`, rid).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count < 3 { // create, inspect (on create), index_requested, inspect
		t.Fatalf("expected audit entries for repository, got %d", count)
	}
	var actor string
	if err := e.db.Pool.QueryRow(context.Background(),
		`SELECT actor_type FROM audit_logs WHERE action = 'project.archive' AND entity_id = $1`, pid).Scan(&actor); err != nil || actor != "user" {
		t.Fatalf("archive audit actor = %q, err=%v", actor, err)
	}
}

func TestUnknownRoutesAndBadInput(t *testing.T) {
	e := setup(t)
	e.login()
	e.expect(e.do("GET", "/does-not-exist", nil), 404)
	e.expect(e.do("GET", "/projects/not-a-uuid", nil), 400)
	e.expect(e.do("GET", "/projects/00000000-0000-0000-0000-000000000000", nil), 404)
}

func TestMigrationsRoundTrip(t *testing.T) {
	d := testutil.NewDB(t)
	ctx := context.Background()
	if err := d.MigrateReset(ctx); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if err := d.MigrateUp(ctx, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatalf("up after reset: %v", err)
	}
}

func TestBearerTokenCannotMintTokens(t *testing.T) {
	e := setup(t)
	e.login()
	secret := e.expect(e.do("POST", "/auth/tokens", map[string]string{"name": "short"}), 201)["secret"].(string)
	e.expect(e.do("POST", "/auth/logout", nil), 204)
	e.csrf = ""
	e.bearer = secret
	e.expect(e.do("POST", "/auth/tokens", map[string]string{"name": "forever"}), 403)
	e.expect(e.do("GET", "/auth/tokens", nil), 200) // listing is still allowed
}

func TestDerivedSlugs(t *testing.T) {
	e := setup(t)
	e.login()
	a := e.expect(e.do("POST", "/projects", map[string]any{"name": "Billing"}), 201)
	b := e.expect(e.do("POST", "/projects", map[string]any{"name": "Billing"}), 201)
	if a["slug"] != "billing" || b["slug"] == "billing" || !strings.HasPrefix(b["slug"].(string), "billing-") {
		t.Fatalf("duplicate name slugs: %v, %v", a["slug"], b["slug"])
	}
	jp := e.expect(e.do("POST", "/projects", map[string]any{"name": "日本語"}), 201)
	if !strings.HasPrefix(jp["slug"].(string), "project-") {
		t.Fatalf("non-ASCII name slug: %v", jp["slug"])
	}
	// An explicitly requested slug still conflicts instead of being rewritten.
	e.expect(e.do("POST", "/projects", map[string]any{"name": "Other", "slug": "billing"}), 409)
}

func TestSessionRenewalReissuesCookie(t *testing.T) {
	e := setup(t)
	e.login()
	// Age the session so the next request extends it.
	if _, err := e.db.Pool.Exec(context.Background(), `UPDATE sessions SET expires_at = now() + interval '10 minutes'`); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest("GET", e.srv.URL+httpapi.BasePath+"/auth/me", nil)
	res, err := e.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	var renewed *http.Cookie
	for _, c := range res.Cookies() {
		if c.Name == auth.SessionCookieName {
			renewed = c
		}
	}
	if res.StatusCode != 200 || renewed == nil || renewed.MaxAge < int((50*time.Minute).Seconds()) {
		t.Fatalf("expected renewed cookie with ~1h max-age, got status %d cookie %+v", res.StatusCode, renewed)
	}
}

// testSettingFiles are the shipped defaults for tests: the scripted provider
// serves both aliases; workflow and board defaults come from the repo.
func testSettingFiles(t *testing.T) settings.Files {
	t.Helper()
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	repo := filepath.Join("..", "..", "..", "..", "configs")
	return settings.Files{
		Models: write("models.yaml", "providers: {}\naliases:\n  reasoning: {targets: [{provider: scripted, model: test-model}]}\n"+
			"  local: {targets: [{provider: scripted, model: test-model}]}\n"),
		Policy:    write("policy.yaml", "cloud_allowed_agents: [\"*\"]\nmax_retries: 1\n"),
		Workflows: filepath.Join(repo, "workflows", "engineering.yaml"),
		Board:     filepath.Join(repo, "board", "defaults.yaml"),
	}
}
