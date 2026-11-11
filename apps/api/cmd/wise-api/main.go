// Command wise-api runs the Wise control-plane API and its admin tasks.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/google/uuid"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	"wise/apps/api/internal/codeindex"
	"wise/apps/api/internal/codingruns"
	"wise/apps/api/internal/costs"
	"wise/apps/api/internal/db/dbgen"
	"wise/apps/api/internal/executor"
	"wise/apps/api/internal/feedback"
	"wise/apps/api/internal/gitworker"
	"wise/apps/api/internal/improvement"
	"wise/apps/api/internal/insights"
	"wise/apps/api/internal/memory"
	"wise/apps/api/internal/modelproxy"
	"wise/apps/api/internal/monitor"
	"wise/apps/api/internal/release"
	"wise/apps/api/internal/workspaces"

	"golang.org/x/term"

	"wise/apps/api/internal/accounts"
	"wise/apps/api/internal/agents"
	"wise/apps/api/internal/auth"
	"wise/apps/api/internal/config"
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
	"wise/apps/api/internal/workflow"
)

const usage = `Usage: wise-api <command> [args]

Commands:
  serve                                  Run the HTTP API (default)
  orchestrator                           Run the Temporal worker and workflow command dispatcher
  cli-runner                             Run Claude Code / Codex calls for the API (isolated container)
  executor                               Run the execution worker (task workspaces and sandboxes)
  egress                                 Run the sandbox egress proxy
  model-proxy                            Run the Runtime Proxy for coding CLIs (API-key accounts)
  git-worker                             Run the git worker (the only component that pushes; approved pushes only)
  migrate [up|down|reset|status]         Manage database migrations
  user create -username NAME [-role R]   Create a user (password read from stdin/prompt)
  user set-password -username NAME      Replace a user's password
  token create -username NAME -name N    Issue a personal API token
`

func main() {
	args := os.Args[1:]
	cmd := "serve"
	if len(args) > 0 {
		cmd, args = args[0], args[1:]
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	switch cmd {
	case "serve":
		err = serve(ctx)
	case "orchestrator":
		err = orchestrator(ctx)
	case "cli-runner":
		err = cliRunner(ctx)
	case "executor":
		err = executorCmd(ctx)
	case "egress":
		err = egressCmd(ctx)
	case "model-proxy":
		err = modelProxyCmd(ctx)
	case "git-worker":
		err = gitWorkerCmd(ctx)
	case "migrate":
		err = migrate(ctx, args)
	case "user":
		err = userCmd(ctx, args)
	case "token":
		err = tokenCmd(ctx, args)
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))
}

func openDB(ctx context.Context) (config.Config, *db.DB, error) {
	cfg, err := config.Load()
	if err != nil {
		return cfg, nil, err
	}
	var d *db.DB
	// Retry briefly so the API can start alongside the database container.
	for attempt := 1; ; attempt++ {
		d, err = db.Open(ctx, cfg.DatabaseURL)
		if err == nil || attempt == 10 || ctx.Err() != nil {
			break
		}
		time.Sleep(time.Duration(attempt) * 300 * time.Millisecond)
	}
	return cfg, d, err
}

func serve(ctx context.Context) error {
	cfg, d, err := openDB(ctx)
	if err != nil {
		return err
	}
	defer d.Close()
	log := newLogger(cfg.LogLevel)
	slog.SetDefault(log)

	if cfg.AutoMigrate {
		if err := d.MigrateUp(ctx, log); err != nil {
			return err
		}
	}
	authSvc := auth.NewService(d, cfg.SessionTTL)
	if created, err := authSvc.Bootstrap(ctx, cfg.BootstrapUser, cfg.BootstrapPassword); err != nil {
		return fmt.Errorf("bootstrap admin: %w", err)
	} else if created {
		log.Info("bootstrap admin user created", "username", cfg.BootstrapUser)
	}
	if len(cfg.RepoRoots) == 0 {
		log.Warn("WISE_REPO_ROOTS is empty; local repositories cannot be registered")
	}

	// Agent infrastructure: registry, model gateway, event hub and run queue.
	catalog, err := agents.Load()
	if err != nil {
		return fmt.Errorf("load agent definitions: %w", err)
	}
	if err := agents.Sync(ctx, d, catalog, log); err != nil {
		return fmt.Errorf("register agent definitions: %w", err)
	}
	modelCfg, modelPolicy, err := modelgateway.LoadConfig(cfg.ModelsConfig, cfg.ModelsPolicy)
	if err != nil {
		return fmt.Errorf("load model config: %w", err)
	}
	hub := events.NewHub(d)
	gateway := modelgateway.New(d, modelCfg, modelPolicy, hub, log)
	if cfg.CLIRunnerURL != "" {
		gateway.UseCLIRunner(cfg.CLIRunnerURL, cfg.CLIRunnerToken)
		log.Info("CLI providers run in the CLI runner", "url", cfg.CLIRunnerURL)
	}
	box, err := secrets.New(cfg.SecretsKey)
	if err != nil {
		return err
	}
	if !box.Enabled() {
		log.Warn("WISE_SECRETS_KEY is empty; accounts cannot be added")
	}
	accountSvc := accounts.NewService(d, box, gateway, log)
	gateway.WithSecretSaver(accountSvc.SaveSecret)
	accountSvc.Reload(ctx)
	runQueue := runqueue.NewService(d, hub, gateway, catalog.Tools, log)
	if cfg.RuntimeToken == "" {
		log.Warn("WISE_RUNTIME_TOKEN is empty; agent runtimes cannot claim runs")
	}

	wfCfg, err := tasks.LoadWorkflowConfig(cfg.WorkflowsConfig)
	if err != nil {
		return err
	}

	agentSvc := agents.NewService(d, hub).WithTools(catalog.Tools)
	taskSvc := tasks.NewService(d, wfCfg)
	projectSvc := projects.NewService(d)
	projectSvc.OnCreate = taskSvc.SeedColumns
	taskSvc.Runs = agentSvc
	taskSvc.Agents = tasks.LookupFrom(agentSvc.Lookup, func(i agents.Info) tasks.AgentInfo { return tasks.AgentInfo(i) })
	taskSvc.Accounts = accountSvc.ValidateModelChoice
	// Panel-managed settings: files are defaults; the database wins and edits apply live.
	settingsSvc := settings.NewService(d, log.With("component", "settings"))
	inspector := gitinspect.New(cfg.RepoRoots)
	repoSvc := repositories.NewService(d, inspector)
	indexSvc := codeindex.NewService(d, repoSvc, log.With("component", "codeindex"))
	settings.RegisterIndexing(settingsSvc, cfg.IndexingDefaults, indexSvc)
	memorySvc := memory.NewService(d, agentSvc, gateway, log.With("component", "memory"))
	settings.RegisterMemory(settingsSvc, cfg.MemoryDefaults, memorySvc)
	var execClient workspaces.Executor
	if c := executor.NewClient(cfg.ExecutorURL, cfg.ExecutorToken); c != nil {
		execClient = c
	} else {
		log.Warn("executor not configured: task workspaces are unavailable (set WISE_EXECUTOR_URL and WISE_EXECUTOR_TOKEN)")
	}
	workspaceSvc := workspaces.New(d, execClient, log.With("component", "workspaces"))
	settings.RegisterExecution(settingsSvc, cfg.ExecutionDefaults, workspaceSvc)
	var codingRunner codingruns.Runner
	if c := executor.NewClient(cfg.ExecutorURL, cfg.ExecutorToken); c != nil {
		codingRunner = c
	}
	var codingProxy codingruns.Proxy
	if c := modelproxy.NewClient(cfg.ModelProxyURL, cfg.ModelProxyToken); c != nil {
		codingProxy = c
	}
	codingSvc := codingruns.New(d, codingRunner, codingProxy, workspaceSvc, accountSvc, gateway, log.With("component", "coding-runs"))
	// Same context as native runs (rules and relevant memories); resolved per call.
	codingSvc.ContextFor = func(ctx context.Context, run dbgen.AgentRun) (string, error) {
		if runQueue.ContextFor == nil {
			return "", nil
		}
		return runQueue.ContextFor(ctx, run)
	}
	settings.RegisterCodingRuntimes(settingsSvc, cfg.CodingDefaults, codingSvc)
	codingSvc.SaveArtifact = func(ctx context.Context, wf, task uuid.UUID, kind string, content json.RawMessage, run *uuid.UUID) error {
		_, err := taskSvc.SaveArtifact(ctx, wf, task, kind, content, run)
		return err
	}
	taskSvc.RuntimeCheck = codingSvc.CheckRuntime
	runQueue.ContextFor = func(ctx context.Context, run dbgen.AgentRun) (string, error) {
		p, err := memorySvc.ForRun(ctx, run)
		return p.Text, err
	}
	insightSvc := insights.New(d)
	improvementSvc := improvement.New(d, agentSvc, memorySvc, settingsSvc, taskSvc, insightSvc, log.With("component", "improvement"))
	improvement.RegisterSetting(settingsSvc, cfg.ImprovementDefaults, improvementSvc)
	releaseSvc := release.New(d, accountSvc)
	monitorSvc := monitor.New(d)
	monitor.RegisterSetting(settingsSvc, cfg.MonitorDefaults, monitorSvc)
	release.RegisterSetting(settingsSvc, cfg.ReleaseDefaults, releaseSvc)
	runQueue.OnFinished = func(ctx context.Context, runID uuid.UUID) {
		memorySvc.OnRunFinished(ctx, runID)
		improvementSvc.OnRunFinished(ctx, runID)
	}
	taskSvc.OnApprovalDecided = memorySvc.OnApprovalDecided
	settings.RegisterDefaults(settingsSvc, settings.Files{
		Models: cfg.ModelsConfig, Policy: cfg.ModelsPolicy, Workflows: cfg.WorkflowsConfig, Board: cfg.BoardDefaults,
	}, gateway, accountSvc, taskSvc)
	if err := settingsSvc.Start(ctx); err != nil {
		return fmt.Errorf("settings: %w", err)
	}
	if err := taskSvc.SeedTemplates(ctx, log); err != nil {
		return fmt.Errorf("seed workflow templates: %w", err)
	}

	integrationSvc := integrations.NewService(d, accountSvc, taskSvc, log.With("component", "integrations"))

	srv := &httpapi.Server{
		DB:               d,
		Auth:             authSvc,
		Projects:         projectSvc,
		Repositories:     repoSvc,
		Index:            indexSvc,
		Memory:           memorySvc,
		Workspaces:       workspaceSvc,
		CodingRuns:       codingSvc,
		Feedback:         feedback.New(d),
		Insights:         insightSvc,
		Improvement:      improvementSvc,
		Release:          releaseSvc,
		Monitor:          monitorSvc,
		Costs:            costs.New(d),
		GitWorkerToken:   os.Getenv("WISE_GIT_WORKER_TOKEN"),
		Agents:           agentSvc,
		RunQueue:         runQueue,
		Gateway:          gateway,
		Hub:              hub,
		Tasks:            taskSvc,
		Accounts:         accountSvc,
		Integrations:     integrationSvc,
		Settings:         settingsSvc,
		Cookie:           httpapi.CookieConfig{Secure: cfg.CookieSecure, TTL: cfg.SessionTTL},
		RuntimeToken:     cfg.RuntimeToken,
		WSOriginPatterns: wsOriginPatterns(cfg.CORSOrigins),
	}
	handler, err := httpapi.NewRouter(srv, httpapi.RouterOptions{
		Logger: log, TrustProxyHeaders: os.Getenv("WISE_TRUST_PROXY_HEADERS") == "true", CORSOrigins: cfg.CORSOrigins,
	})
	if err != nil {
		return err
	}

	go pruneSessions(ctx, authSvc, log)
	go runQueue.RunReaper(ctx, 15*time.Second)
	go hub.ListenWorkflowEvents(ctx, d, log)
	go integrationSvc.RunScheduler(ctx, time.Minute)
	go indexSvc.RunScheduler(ctx)
	indexSvc.SetEmbedder(gateway)
	go memorySvc.RunBackground(ctx)
	go improvementSvc.RunScheduler(ctx, 5*time.Minute)
	go releaseSvc.RunRefresher(ctx, time.Minute)
	go workspaceSvc.RunSweeper(ctx)
	go indexSvc.RunEmbedder(ctx)

	hs := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      90 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	errCh := make(chan error, 1)
	go func() {
		log.Info("wise-api listening", "addr", cfg.HTTPAddr, "repo_roots", inspector.Roots())
		errCh <- hs.ListenAndServe()
	}()
	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		log.Info("shutting down")
		shutCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return hs.Shutdown(shutCtx)
	}
	return nil
}

// wsOriginPatterns allows configured CORS origins. Same-host origins are always
// accepted by the WebSocket library; no wildcard is added because any page on
// another local port is same-site with the dashboard and would get the cookie.
func wsOriginPatterns(origins []string) []string {
	var patterns []string
	for _, o := range origins {
		if u, err := url.Parse(o); err == nil && u.Host != "" {
			patterns = append(patterns, u.Host)
		}
	}
	return patterns
}

// orchestrator runs the Temporal worker for engineering workflows plus the
// outbox dispatcher. It needs Postgres and Temporal only: agent work is queued
// for the runtime, and model credentials stay with the API.
func orchestrator(ctx context.Context) error {
	cfg, d, err := openDB(ctx)
	if err != nil {
		return err
	}
	defer d.Close()
	log := newLogger(cfg.LogLevel).With("service", "orchestrator")
	slog.SetDefault(log)
	wfCfg, err := tasks.LoadWorkflowConfig(cfg.WorkflowsConfig)
	if err != nil {
		return err
	}
	var execClient workspaces.Executor
	if c := executor.NewClient(cfg.ExecutorURL, cfg.ExecutorToken); c != nil {
		execClient = c
	}
	var git *gitworker.Client
	if url, tok := os.Getenv("WISE_GIT_WORKER_URL"), os.Getenv("WISE_GIT_PUSH_TOKEN"); url != "" && len(tok) >= 32 {
		git = gitworker.NewClient(url, tok)
	}
	return workflow.RunOrchestrator(ctx, workflow.OrchestratorConfig{
		TemporalAddress: cfg.TemporalAddress, Namespace: cfg.TemporalNamespace,
		Workspaces: workspaces.New(d, execClient, log.With("component", "workspaces")),
		// The orchestrator reads release targets from the database; it never holds a git token.
		Release: release.New(d, nil), Git: git,
	}, d, tasks.NewService(d, wfCfg), agents.NewService(d, events.NewHub(d)), log)
}

// cliRunner executes CLI model providers on behalf of the API. Its container has
// no database, repositories or control-plane secrets: each request carries the
// one account credential it needs.
func cliRunner(ctx context.Context) error {
	log := newLogger(os.Getenv("WISE_LOG_LEVEL")).With("service", "cli-runner")
	token := os.Getenv("WISE_CLI_RUNNER_TOKEN")
	if len(token) < 32 {
		return errors.New("WISE_CLI_RUNNER_TOKEN must be at least 32 characters")
	}
	addr := os.Getenv("WISE_CLI_RUNNER_ADDR")
	if addr == "" {
		addr = ":8090"
	}
	hs := &http.Server{
		Addr: addr, Handler: modelgateway.RunnerHandler(token, log),
		ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second, MaxHeaderBytes: 1 << 16,
	}
	errCh := make(chan error, 1)
	go func() {
		log.Info("cli runner listening", "addr", addr)
		errCh <- hs.ListenAndServe()
	}()
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return hs.Shutdown(shutCtx)
	}
}

func pruneSessions(ctx context.Context, a *auth.Service, log *slog.Logger) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if n, err := a.PruneSessions(ctx); err != nil {
				log.Warn("prune sessions", "err", err)
			} else if n > 0 {
				log.Info("pruned expired sessions", "count", n)
			}
		}
	}
}

func migrate(ctx context.Context, args []string) error {
	cfg, d, err := openDB(ctx)
	if err != nil {
		return err
	}
	defer d.Close()
	sub := "up"
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "up":
		return d.MigrateUp(ctx, newLogger(cfg.LogLevel))
	case "down":
		return d.MigrateDown(ctx)
	case "reset":
		return d.MigrateReset(ctx)
	case "status":
		lines, err := d.MigrationStatus(ctx)
		for _, l := range lines {
			fmt.Println(l)
		}
		return err
	default:
		return fmt.Errorf("unknown migrate subcommand %q", sub)
	}
}

func userCmd(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: user create|set-password -username NAME")
	}
	fs := flag.NewFlagSet("user", flag.ContinueOnError)
	username := fs.String("username", "", "username")
	role := fs.String("role", "admin", "role (admin|member)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *username == "" {
		return errors.New("-username is required")
	}
	_, d, err := openDB(ctx)
	if err != nil {
		return err
	}
	defer d.Close()
	svc := auth.NewService(d, time.Hour)
	password, err := readPassword()
	if err != nil {
		return err
	}
	switch args[0] {
	case "create":
		if *role != "admin" && *role != "member" {
			return errors.New("-role must be admin or member")
		}
		u, err := svc.CreateUser(ctx, *username, password, *role)
		if err != nil {
			return err
		}
		fmt.Printf("created user %s (%s)\n", u.Username, u.ID)
	case "set-password":
		if err := svc.SetPassword(ctx, *username, password); err != nil {
			return err
		}
		fmt.Println("password updated")
	default:
		return fmt.Errorf("unknown user subcommand %q", args[0])
	}
	return nil
}

func tokenCmd(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] != "create" {
		return errors.New("usage: token create -username NAME -name TOKEN_NAME [-days N]")
	}
	fs := flag.NewFlagSet("token", flag.ContinueOnError)
	username := fs.String("username", "", "owner username")
	name := fs.String("name", "", "token name")
	days := fs.Int("days", 0, "expire after N days (0 = never)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	_, d, err := openDB(ctx)
	if err != nil {
		return err
	}
	defer d.Close()
	u, err := d.GetUserByUsername(ctx, *username)
	if err != nil {
		return fmt.Errorf("load user %q: %w", *username, err)
	}
	var exp *int
	if *days > 0 {
		exp = days
	}
	p := auth.Principal{User: u, Method: auth.MethodAPIToken}
	created, err := auth.NewService(d, time.Hour).CreateAPIToken(auth.WithPrincipal(ctx, p), p, *name, exp)
	if err != nil {
		return err
	}
	fmt.Println(created.Secret)
	return nil
}

// readPassword prompts without echo on a terminal, or reads one line from stdin.
func readPassword() (string, error) {
	if term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprint(os.Stderr, "Password: ")
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", err
		}
		fmt.Fprint(os.Stderr, "Confirm:  ")
		c, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", err
		}
		if string(b) != string(c) {
			return "", errors.New("passwords do not match")
		}
		return string(b), nil
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return "", fmt.Errorf("read password from stdin: %w", err)
	}
	return strings.TrimRight(line, "\r\n"), nil
}
