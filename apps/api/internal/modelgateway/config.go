package modelgateway

import (
	"fmt"
	"os"
	"time"

	"sigs.k8s.io/yaml"
)

// Config is loaded from configs/models/models.yaml.
type Config struct {
	Providers map[string]ProviderConfig `json:"providers"`
	Aliases   map[string]AliasConfig    `json:"aliases"`
	Cache     CacheConfig               `json:"cache"`
}

type ProviderConfig struct {
	Type      string        `json:"type"` // "openai-compatible" | "claude-code"
	BaseURL   string        `json:"base_url,omitempty"`
	APIKeyEnv string        `json:"api_key_env,omitempty"`
	Binary    string        `json:"binary,omitempty"`
	Cloud     bool          `json:"cloud"`
	Timeout   time.Duration `json:"-"`
	TimeoutS  int           `json:"timeout_s,omitempty"`
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
	var cfg Config
	var pol Policy
	if err := readYAML(modelsPath, &cfg); err != nil {
		return cfg, pol, err
	}
	if err := readYAML(policyPath, &pol); err != nil {
		return cfg, pol, err
	}
	for name, p := range cfg.Providers {
		switch p.Type {
		case "openai-compatible":
			if p.BaseURL == "" {
				return cfg, pol, fmt.Errorf("provider %q: base_url is required", name)
			}
		case "claude-code":
			if p.Binary == "" {
				p.Binary = "claude"
			}
		default:
			return cfg, pol, fmt.Errorf("provider %q: unknown type %q", name, p.Type)
		}
		p.Timeout = 5 * time.Minute
		if p.TimeoutS > 0 {
			p.Timeout = time.Duration(p.TimeoutS) * time.Second
		}
		cfg.Providers[name] = p
	}
	for alias, a := range cfg.Aliases {
		if len(a.Targets) == 0 {
			return cfg, pol, fmt.Errorf("alias %q has no targets", alias)
		}
		for _, t := range a.Targets {
			if _, ok := cfg.Providers[t.Provider]; !ok {
				return cfg, pol, fmt.Errorf("alias %q: unknown provider %q", alias, t.Provider)
			}
		}
	}
	if pol.MaxRetries <= 0 {
		pol.MaxRetries = 2
	}
	return cfg, pol, nil
}

func readYAML(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if err := yaml.Unmarshal([]byte(os.ExpandEnv(string(b))), v); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	return nil
}
