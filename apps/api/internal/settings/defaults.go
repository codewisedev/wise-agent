package settings

import (
	"context"
	"encoding/json"
	"wise/apps/api/internal/codeindex"

	"wise/apps/api/internal/accounts"
	"wise/apps/api/internal/modelgateway"
	"wise/apps/api/internal/tasks"
)

// Files are the shipped defaults of the built-in settings.
type Files struct{ Models, Policy, Workflows, Board string }

// RegisterDefaults declares every panel-managed setting: its shipped default
// (a file under configs/), validation and how it takes effect live.
func RegisterDefaults(s *Service, files Files, gw *modelgateway.Gateway, acc *accounts.Service, t *tasks.Service) {
	// Alias targets may name stored model accounts (or adapters registered in code).
	isAccount := func(name string) bool {
		return acc.IsModelAccount(context.Background(), name) || gw.Has(name)
	}
	applyModels := func(models, policy json.RawMessage) error {
		mc, err := modelgateway.ParseModels(models, isAccount)
		if err != nil {
			return err
		}
		pol, err := modelgateway.ParsePolicy(policy)
		if err != nil {
			return err
		}
		gw.Reconfigure(mc, pol)
		return nil
	}
	s.Register(Def{
		Key: "models", Title: "Models", Description: "Built-in providers, model aliases (with fallbacks) and the response cache.",
		Default: FileDefault(files.Models),
		Validate: func(_ context.Context, v json.RawMessage) (json.RawMessage, error) {
			_, err := modelgateway.ParseModels(v, isAccount)
			return v, err
		},
		Apply: func(_ context.Context, v json.RawMessage) error {
			pol := s.Value("model_policy")
			if pol == nil {
				return nil // applied together once the policy is loaded
			}
			return applyModels(v, pol)
		},
	})
	s.Register(Def{
		Key: "model_policy", Title: "Model policy", Description: "Which agents may use cloud models, daily budgets per agent and retries.",
		Default: FileDefault(files.Policy),
		Validate: func(_ context.Context, v json.RawMessage) (json.RawMessage, error) {
			_, err := modelgateway.ParsePolicy(v)
			return v, err
		},
		Apply: func(_ context.Context, v json.RawMessage) error {
			models := s.Value("models")
			if models == nil {
				return nil
			}
			return applyModels(models, v)
		},
	})
	s.Register(Def{
		Key: "workflow_defaults", Title: "Workflow defaults", Description: "Limits for every workflow run (templates may override them).",
		Default: FileDefault(files.Workflows),
		Validate: func(_ context.Context, v json.RawMessage) (json.RawMessage, error) {
			_, err := tasks.ParseWorkflowConfig(v)
			return v, err
		},
		Apply: func(_ context.Context, v json.RawMessage) error {
			c, err := tasks.ParseWorkflowConfig(v)
			if err == nil {
				t.SetWorkflowConfig(c)
			}
			return err
		},
	})
	s.Register(Def{
		Key: "board_defaults", Title: "Board defaults", Description: "The columns new projects start with.",
		Default: FileDefault(files.Board),
		Validate: func(_ context.Context, v json.RawMessage) (json.RawMessage, error) {
			_, err := tasks.ParseBoardDefaults(v)
			return v, err
		},
		Apply: func(_ context.Context, v json.RawMessage) error {
			b, err := tasks.ParseBoardDefaults(v)
			if err == nil {
				t.SetBoardDefaults(b)
			}
			return err
		},
	})
}

// RegisterIndexing declares the "indexing" setting (globs, caps, auto-index, secret patterns).
func RegisterIndexing(s *Service, file string, idx *codeindex.Service) {
	s.Register(Def{
		Key: "indexing", Title: "Indexing",
		Description: "What the code indexer reads: exclude/include globs, documentation globs, size caps, chunk size, automatic re-indexing when HEAD moves, and extra secret patterns (the built-in secret guard always applies).",
		Default:     FileDefault(file),
		Validate: func(_ context.Context, v json.RawMessage) (json.RawMessage, error) {
			st, err := codeindex.ParseSettings(v)
			if err != nil {
				return nil, err
			}
			return json.Marshal(st)
		},
		Apply: func(_ context.Context, v json.RawMessage) error {
			st, err := codeindex.ParseSettings(v)
			if err == nil {
				idx.SetSettings(st)
			}
			return err
		},
	})
}
