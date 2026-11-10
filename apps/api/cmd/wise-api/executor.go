package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"wise/apps/api/internal/egress"
	"wise/apps/api/internal/executor"
	"wise/apps/api/internal/modelproxy"
)

func listen(ctx context.Context, hs *http.Server) error {
	errCh := make(chan error, 1)
	go func() { errCh <- hs.ListenAndServe() }()
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return hs.Shutdown(shutCtx)
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// executorCmd runs the trusted execution worker: workspaces, sandboxes,
// commands and branch delivery. Only the API and orchestrator can call it.
func executorCmd(ctx context.Context) error {
	log := newLogger(os.Getenv("WISE_LOG_LEVEL")).With("service", "executor")
	token := os.Getenv("WISE_EXECUTOR_TOKEN")
	if len(token) < 32 {
		return errors.New("WISE_EXECUTOR_TOKEN must be at least 32 characters")
	}
	var roots []string
	for _, r := range strings.Split(os.Getenv("WISE_REPO_ROOTS"), ",") {
		if r = strings.TrimSpace(r); r != "" {
			roots = append(roots, r)
		}
	}
	if len(roots) == 0 {
		return errors.New("WISE_REPO_ROOTS is required")
	}
	rt, err := executor.NewDocker(envOr("WISE_DOCKER_HOST", "tcp://wise-docker-proxy:2375"), envOr("WISE_WORKSPACES_VOLUME", "wise-workspaces"))
	if err != nil {
		return err
	}
	rt.SetCLIVolume(os.Getenv("WISE_CODING_CLIS_VOLUME"))
	pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	if err := rt.Ping(pctx); err != nil {
		log.Warn("docker api not reachable yet", "err", err)
	}
	cancel()
	m := executor.NewManager(executor.Config{
		Dir:       envOr("WISE_WORKSPACES_DIR", "/workspaces"),
		RepoRoots: roots,
		Helper:    envOr("WISE_SANDBOX_HELPER", "/usr/local/bin/wise-sandbox"),
		User:      envOr("WISE_SANDBOX_USER", "1000:1000"),
		Proxy:     envOr("WISE_EGRESS_PROXY", "http://wise-egress:3128"),
	}, rt, log)
	egressToken := os.Getenv("WISE_EGRESS_TOKEN")
	if len(egressToken) < 32 || egressToken == token {
		return errors.New("WISE_EGRESS_TOKEN must be at least 32 characters and differ from WISE_EXECUTOR_TOKEN")
	}
	addr := envOr("WISE_EXECUTOR_ADDR", ":8091")
	log.Info("executor listening", "addr", addr)
	return listen(ctx, &http.Server{Addr: addr, Handler: executor.Handler(m, token, egressToken, log),
		ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second, MaxHeaderBytes: 1 << 16})
}

// modelProxyCmd runs the Runtime Proxy: runners' model calls with per-run
// tokens, the real API keys injected here, usage and budgets.
func modelProxyCmd(ctx context.Context) error {
	log := newLogger(os.Getenv("WISE_LOG_LEVEL")).With("service", "model-proxy")
	token := os.Getenv("WISE_MODEL_PROXY_TOKEN")
	if len(token) < 32 {
		return errors.New("WISE_MODEL_PROXY_TOKEN must be at least 32 characters")
	}
	addr := envOr("WISE_MODEL_PROXY_ADDR", ":8095")
	log.Info("model proxy listening", "addr", addr)
	return listen(ctx, &http.Server{Addr: addr, Handler: modelproxy.New(token, log).Handler(),
		ReadHeaderTimeout: 30 * time.Second, MaxHeaderBytes: 1 << 16})
}

// egressCmd runs the sandbox egress proxy. It holds only a token that can
// read the allowlist.
func egressCmd(ctx context.Context) error {
	log := newLogger(os.Getenv("WISE_LOG_LEVEL")).With("service", "egress")
	token := os.Getenv("WISE_EGRESS_TOKEN")
	if len(token) < 32 {
		return errors.New("WISE_EGRESS_TOKEN must be at least 32 characters")
	}
	allow := &egress.Allowlist{}
	go egress.Follow(ctx, allow, envOr("WISE_EXECUTOR_URL", "http://wise-executor:8091"), token, log)
	// Runners authenticate with their run token; their grants add hosts.
	addr := envOr("WISE_EGRESS_ADDR", ":3128")
	log.Info("egress proxy listening", "addr", addr)
	execURL := envOr("WISE_EXECUTOR_URL", "http://wise-executor:8091")
	proxy := &egress.Proxy{Allow: allow, Log: log, Grants: egress.GrantLookup(execURL, token)}
	return listen(ctx, &http.Server{Addr: addr, Handler: proxy,
		ReadHeaderTimeout: 30 * time.Second, MaxHeaderBytes: 1 << 16})
}
