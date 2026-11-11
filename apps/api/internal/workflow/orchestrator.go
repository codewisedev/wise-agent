package workflow

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
	"wise/apps/api/internal/gitworker"
	"wise/apps/api/internal/release"
	"wise/apps/api/internal/workspaces"

	"go.temporal.io/api/serviceerror"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	tlog "go.temporal.io/sdk/log"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	"google.golang.org/protobuf/types/known/durationpb"

	"wise/apps/api/internal/agents"
	"wise/apps/api/internal/db"
	"wise/apps/api/internal/tasks"
)

// OrchestratorConfig configures RunOrchestrator.
type OrchestratorConfig struct {
	TemporalAddress string
	Namespace       string
	Retention       time.Duration
	Workspaces      *workspaces.Service // engineering steps; nil when the executor is not configured
	Release         *release.Service    // release steps; nil when not configured
	Git             *gitworker.Client   // the git worker (approval ids only); nil when not configured
}

// RunOrchestrator connects to Temporal (waiting for it to come up), makes sure
// the namespace exists, then runs the engineering worker and the outbox
// dispatcher until ctx is cancelled.
func RunOrchestrator(ctx context.Context, cfg OrchestratorConfig, d *db.DB, taskSvc *tasks.Service, agentSvc *agents.Service, log *slog.Logger) error {
	opts := client.Options{
		HostPort:  cfg.TemporalAddress,
		Namespace: cfg.Namespace,
		Logger:    tlog.NewStructuredLogger(log.With("component", "temporal")),
	}
	if err := ensureNamespace(ctx, opts, cfg, log); err != nil {
		return err
	}
	c, err := dialWithRetry(ctx, opts, log)
	if err != nil {
		return err
	}
	defer c.Close()

	w := worker.New(c, TaskQueue, worker.Options{})
	w.RegisterWorkflowWithOptions(PipelineWorkflow, workflow.RegisterOptions{Name: tasks.PipelineType})
	w.RegisterActivity(&Activities{Tasks: taskSvc, Agents: agentSvc, Workspaces: cfg.Workspaces, Release: cfg.Release, Git: cfg.Git})
	if err := w.Start(); err != nil {
		return fmt.Errorf("start temporal worker: %w", err)
	}
	defer w.Stop()
	log.Info("orchestrator running", "temporal", cfg.TemporalAddress, "namespace", cfg.Namespace, "task_queue", TaskQueue)

	(&Dispatcher{DB: d, Tasks: taskSvc, Temporal: c, Log: log.With("component", "dispatcher")}).Run(ctx)
	return nil
}

func dialWithRetry(ctx context.Context, opts client.Options, log *slog.Logger) (client.Client, error) {
	backoff := time.Second
	for {
		c, err := client.DialContext(ctx, opts)
		if err == nil {
			return c, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		log.Warn("temporal not reachable yet", "addr", opts.HostPort, "err", err, "retry_in", backoff)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 15*time.Second)
	}
}

func ensureNamespace(ctx context.Context, opts client.Options, cfg OrchestratorConfig, log *slog.Logger) error {
	backoff := time.Second
	for {
		err := registerNamespace(ctx, opts, cfg)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		log.Warn("register temporal namespace", "namespace", cfg.Namespace, "err", err, "retry_in", backoff)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 15*time.Second)
	}
}

func registerNamespace(ctx context.Context, opts client.Options, cfg OrchestratorConfig) error {
	nc, err := client.NewNamespaceClient(opts)
	if err != nil {
		return err
	}
	defer nc.Close()
	retention := cfg.Retention
	if retention <= 0 {
		retention = 30 * 24 * time.Hour
	}
	rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	err = nc.Register(rctx, &workflowservice.RegisterNamespaceRequest{
		Namespace:                        cfg.Namespace,
		Description:                      "Wise engineering workflows",
		WorkflowExecutionRetentionPeriod: durationpb.New(retention),
	})
	var exists *serviceerror.NamespaceAlreadyExists
	if errors.As(err, &exists) {
		return nil
	}
	return err
}
