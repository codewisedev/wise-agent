// Command wise-api runs the Wise control-plane API and its admin tasks.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"wise/apps/api/internal/agents"
	"wise/apps/api/internal/auth"
	"wise/apps/api/internal/config"
	"wise/apps/api/internal/db"
	"wise/apps/api/internal/events"
	"wise/apps/api/internal/gitinspect"
	"wise/apps/api/internal/httpapi"
	"wise/apps/api/internal/modelgateway"
	"wise/apps/api/internal/projects"
	"wise/apps/api/internal/repositories"
	"wise/apps/api/internal/runqueue"
)

const usage = `Usage: wise-api <command> [args]

Commands:
  serve                                  Run the HTTP API (default)
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
	runQueue := runqueue.NewService(d, hub, gateway, catalog.Tools, log)
	if cfg.RuntimeToken == "" {
		log.Warn("WISE_RUNTIME_TOKEN is empty; agent runtimes cannot claim runs")
	}

	inspector := gitinspect.New(cfg.RepoRoots)
	srv := &httpapi.Server{
		DB:               d,
		Auth:             authSvc,
		Projects:         projects.NewService(d),
		Repositories:     repositories.NewService(d, inspector),
		Agents:           agents.NewService(d, hub),
		RunQueue:         runQueue,
		Gateway:          gateway,
		Hub:              hub,
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
