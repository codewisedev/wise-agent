package modelgateway

import (
	"fmt"
	"os"
	"strings"
	"time"

	"sigs.k8s.io/yaml"
)

// Config is loaded from configs/models/models.yaml.
type Config struct {
	Providers map[string]ProviderConfig `json:"providers"`
	Aliases   map[string]AliasConfig    `json:"aliases"`
	Cache     CacheConfig               `json:"cache"`
	// Embedding turns on semantic search of the code index (and memory).
	Embedding *EmbeddingConfig `json:"embedding,omitempty"`
}

// EmbeddingConfig picks the model that embeds code, docs and queries.
type EmbeddingConfig struct {
	Provider   string `json:"provider"` // built-in provider or model account (OpenAI-compatible)
	Model      string `json:"model"`
	Dimensions int    `json:"dimensions,omitempty"` // 0 = the model's default
	BatchSize  int    `json:"batch_size,omitempty"`
	// AllowCloud is the explicit consent to send code and docs to a cloud provider.
	AllowCloud bool `json:"allow_cloud"`
}

// Key identifies vectors made with this configuration; changing it re-embeds everything.
func (e *EmbeddingConfig) Key() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("%s/%s@%d", e.Provider, e.Model, e.Dimensions)
}

type ProviderConfig struct {
	Type      string `json:"type"` // "openai-compatible" | "claude-code" | "codex-cli"
	BaseURL   string `json:"base_url,omitempty"`
	APIKeyEnv string `json:"api_key_env,omitempty"`
	// Secret is an account credential (API key, Claude OAuth token, Codex
	// auth.json). Set only for stored accounts; never serialized.
	Secret   string        `json:"-"`
	Binary   string        `json:"binary,omitempty"`
	Cloud    bool          `json:"cloud"`
	Timeout  time.Duration `json:"-"`
	TimeoutS int           `json:"timeout_s,omitempty"`
	// Pricing per million tokens (optional; claude-code reports its own cost).
	InputPerMTok  float64 `json:"input_per_mtok,omitempty"`
	OutputPerMTok float64 `json:"output_per_mtok,omitempty"`
}

type AliasConfig struct {
	Description string   `json:"description,omitempty"`
	Targets     []Target `json:"targets"`
}

type Target struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

type CacheConfig struct {
	Enabled bool `json:"enabled"`
	TTLH    int  `json:"ttl_hours"`
}

// Policy is loaded from configs/policies/models.yaml.
type Policy struct {
	// CloudAllowedAgents may use providers marked cloud. "*" allows all agents.
	CloudAllowedAgents []string `json:"cloud_allowed_agents"`
	// DailyBudgetUSD per agent; key "default" applies to agents without an entry.
	DailyBudgetUSD map[string]float64 `json:"daily_budget_usd"`
	MaxRetries     int                `json:"max_retries"`
}

func (p Policy) cloudAllowed(agent string) bool {
	for _, a := range p.CloudAllowedAgents {
		if a == "*" || a == agent {
			return true
		}
	}
	return false
}

func (p Policy) dailyBudget(agent string) (float64, bool) {
	if v, ok := p.DailyBudgetUSD[agent]; ok {
		return v, true
	}
	v, ok := p.DailyBudgetUSD["default"]
	return v, ok
}

// LoadConfig reads both files, expanding ${ENV} references.
func LoadConfig(modelsPath, policyPath string) (Config, Policy, error) {
	mb, err := os.ReadFile(modelsPath)
	if err != nil {
		return Config{}, Policy{}, fmt.Errorf("read %s: %w", modelsPath, err)
	}
	pb, err := os.ReadFile(policyPath)
	if err != nil {
		return Config{}, Policy{}, fmt.Errorf("read %s: %w", policyPath, err)
	}
	cfg, err := ParseModels(mb, nil)
	if err != nil {
		return cfg, Policy{}, fmt.Errorf("%s: %w", modelsPath, err)
	}
	pol, err := ParsePolicy(pb)
	if err != nil {
		return cfg, pol, fmt.Errorf("%s: %w", policyPath, err)
	}
	return cfg, pol, nil
}

// ParseModels decodes and validates a models configuration (YAML or JSON),
// expanding ${ENV} references. Alias targets must name a provider in the
// configuration or, when isAccount is given, a stored model account.
func ParseModels(raw []byte, isAccount func(string) bool) (Config, error) {
	var cfg Config
	if err := yaml.Unmarshal([]byte(os.ExpandEnv(string(raw))), &cfg); err != nil {
		return cfg, fmt.Errorf("parse: %w", err)
	}
	if cfg.Providers == nil {
		cfg.Providers = map[string]ProviderConfig{}
	}
	for name, p := range cfg.Providers {
		switch p.Type {
		case "openai-compatible":
			if p.BaseURL == "" {
				return cfg, fmt.Errorf("provider %q: base_url is required", name)
			}
		case "claude-code":
			if p.Binary == "" {
				p.Binary = "claude"
			}
		case "codex-cli":
			return cfg, fmt.Errorf("provider %q: codex-cli needs an auth.json; add a Codex account in Settings → Accounts instead", name)
		default:
			return cfg, fmt.Errorf("provider %q: unknown type %q (openai-compatible or claude-code)", name, p.Type)
		}
		if p.TimeoutS < 0 || p.InputPerMTok < 0 || p.OutputPerMTok < 0 {
			return cfg, fmt.Errorf("provider %q: timeout and prices must not be negative", name)
		}
		p.Timeout = 5 * time.Minute
		if p.TimeoutS > 0 {
			p.Timeout = time.Duration(p.TimeoutS) * time.Second
		}
		cfg.Providers[name] = p
	}
	if e := cfg.Embedding; e != nil {
		e.Provider, e.Model = strings.TrimSpace(e.Provider), strings.TrimSpace(e.Model)
		switch {
		case e.Provider == "" && e.Model == "":
			cfg.Embedding = nil
		case e.Model == "":
			return cfg, fmt.Errorf("embedding: model is required (e.g. gemini-embedding-001 or text-embedding-3-small)")
		case e.Dimensions != 0 && (e.Dimensions < 32 || e.Dimensions > 4096):
			return cfg, fmt.Errorf("embedding: dimensions must be 32-4096 (or 0 for the model's default)")
		case e.BatchSize < 0 || e.BatchSize > 256:
			return cfg, fmt.Errorf("embedding: batch_size must be 1-256")
		}
		if cfg.Embedding != nil {
			if e.BatchSize == 0 {
				e.BatchSize = 32
			}
			if p, ok := cfg.Providers[e.Provider]; ok {
				if p.Type != "openai-compatible" {
					return cfg, fmt.Errorf("embedding: provider %q is not OpenAI-compatible", e.Provider)
				}
			} else if isAccount == nil || !isAccount(e.Provider) {
				return cfg, fmt.Errorf("embedding: %q is neither a provider nor a model account", e.Provider)
			}
		}
	}
	for alias, a := range cfg.Aliases {
		if len(a.Targets) == 0 {
			return cfg, fmt.Errorf("alias %q has no targets", alias)
		}
		for _, t := range a.Targets {
			if _, ok := cfg.Providers[t.Provider]; ok {
				continue
			}
			if isAccount != nil && isAccount(t.Provider) {
				continue
			}
			return cfg, fmt.Errorf("alias %q: unknown provider or account %q", alias, t.Provider)
		}
	}
	if cfg.Cache.TTLH < 0 {
		return cfg, fmt.Errorf("cache ttl_hours must not be negative")
	}
	return cfg, nil
}

// ParsePolicy decodes and validates a model policy (YAML or JSON).
func ParsePolicy(raw []byte) (Policy, error) {
	var pol Policy
	if err := yaml.Unmarshal(raw, &pol); err != nil {
		return pol, fmt.Errorf("parse: %w", err)
	}
	var probe struct {
		MaxRetries *int `json:"max_retries"`
	}
	_ = yaml.Unmarshal(raw, &probe)
	for agent, v := range pol.DailyBudgetUSD {
		if v < 0 {
			return pol, fmt.Errorf("daily budget for %q must not be negative", agent)
		}
	}
	if pol.MaxRetries < 0 || pol.MaxRetries > 10 {
		return pol, fmt.Errorf("max_retries must be between 0 and 10")
	}
	if probe.MaxRetries == nil {
		pol.MaxRetries = 2 // default when unset; an explicit 0 means no retries
	}
	return pol, nil
}
