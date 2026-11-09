// Package config loads Wise API configuration from the environment.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	HTTPAddr          string
	DatabaseURL       string
	SessionTTL        time.Duration
	CookieSecure      bool
	RepoRoots         []string
	AutoMigrate       bool
	BootstrapUser     string
	BootstrapPassword string
	LogLevel          string
	CORSOrigins       []string
	RuntimeToken      string
	ModelsConfig      string
	ModelsPolicy      string
	WorkflowsConfig   string
	BoardDefaults     string
	IndexingDefaults  string
	MemoryDefaults    string
	TemporalAddress   string
	TemporalNamespace string
	SecretsKey        string
	CLIRunnerURL      string
	CLIRunnerToken    string
}

// Load reads configuration from WISE_* environment variables.
func Load() (Config, error) {
	c := Config{
		HTTPAddr:          getenv("WISE_HTTP_ADDR", ":8080"),
		DatabaseURL:       os.Getenv("WISE_DATABASE_URL"),
		BootstrapUser:     os.Getenv("WISE_BOOTSTRAP_ADMIN_USER"),
		BootstrapPassword: os.Getenv("WISE_BOOTSTRAP_ADMIN_PASSWORD"),
		LogLevel:          getenv("WISE_LOG_LEVEL", "info"),
		RuntimeToken:      os.Getenv("WISE_RUNTIME_TOKEN"),
		ModelsConfig:      getenv("WISE_MODELS_CONFIG", "configs/models/models.yaml"),
		ModelsPolicy:      getenv("WISE_MODELS_POLICY", "configs/policies/models.yaml"),
		WorkflowsConfig:   getenv("WISE_WORKFLOWS_CONFIG", "configs/workflows/engineering.yaml"),
		BoardDefaults:     getenv("WISE_BOARD_DEFAULTS", "configs/board/defaults.yaml"),
		IndexingDefaults:  getenv("WISE_INDEXING_DEFAULTS", "configs/indexing/defaults.yaml"),
		MemoryDefaults:    getenv("WISE_MEMORY_DEFAULTS", "configs/memory/defaults.yaml"),
		TemporalAddress:   getenv("WISE_TEMPORAL_ADDRESS", "127.0.0.1:7233"),
		TemporalNamespace: getenv("WISE_TEMPORAL_NAMESPACE", "wise"),
		SecretsKey:        os.Getenv("WISE_SECRETS_KEY"),
		CLIRunnerURL:      os.Getenv("WISE_CLI_RUNNER_URL"),
		CLIRunnerToken:    os.Getenv("WISE_CLI_RUNNER_TOKEN"),
	}
	var errs []error

	ttl, err := time.ParseDuration(getenv("WISE_SESSION_TTL", "168h"))
	if err != nil || ttl <= 0 {
		errs = append(errs, fmt.Errorf("WISE_SESSION_TTL: invalid duration"))
	}
	c.SessionTTL = ttl

	if c.CookieSecure, err = parseBool("WISE_COOKIE_SECURE", false); err != nil {
		errs = append(errs, err)
	}
	if c.AutoMigrate, err = parseBool("WISE_AUTO_MIGRATE", true); err != nil {
		errs = append(errs, err)
	}

	for _, root := range splitList(os.Getenv("WISE_REPO_ROOTS")) {
		if !filepath.IsAbs(root) {
			errs = append(errs, fmt.Errorf("WISE_REPO_ROOTS: %q is not an absolute path", root))
			continue
		}
		c.RepoRoots = append(c.RepoRoots, filepath.Clean(root))
	}
	// Origins contain ':' (scheme, port), so split on commas only.
	for _, o := range strings.Split(os.Getenv("WISE_CORS_ORIGINS"), ",") {
		if o = strings.TrimSuffix(strings.TrimSpace(o), "/"); o != "" {
			c.CORSOrigins = append(c.CORSOrigins, o)
		}
	}

	if c.DatabaseURL == "" {
		errs = append(errs, errors.New("WISE_DATABASE_URL is required"))
	}
	if c.RuntimeToken != "" && len(c.RuntimeToken) < 32 {
		errs = append(errs, errors.New("WISE_RUNTIME_TOKEN must be at least 32 characters"))
	}
	if c.CLIRunnerURL != "" && len(c.CLIRunnerToken) < 32 {
		errs = append(errs, errors.New("WISE_CLI_RUNNER_TOKEN must be at least 32 characters when WISE_CLI_RUNNER_URL is set"))
	}
	if (c.BootstrapUser == "") != (c.BootstrapPassword == "") {
		errs = append(errs, errors.New("WISE_BOOTSTRAP_ADMIN_USER and WISE_BOOTSTRAP_ADMIN_PASSWORD must be set together"))
	}
	return c, errors.Join(errs...)
}

func getenv(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func parseBool(key string, def bool) (bool, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def, fmt.Errorf("%s: invalid boolean %q", key, v)
	}
	return b, nil
}

func splitList(v string) []string {
	var out []string
	for _, part := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ':' }) {
		if s := strings.TrimSpace(part); s != "" {
			out = append(out, s)
		}
	}
	return out
}
