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
}

// Load reads configuration from WISE_* environment variables.
func Load() (Config, error) {
	c := Config{
		HTTPAddr:          getenv("WISE_HTTP_ADDR", ":8080"),
		DatabaseURL:       os.Getenv("WISE_DATABASE_URL"),
		BootstrapUser:     os.Getenv("WISE_BOOTSTRAP_ADMIN_USER"),
		BootstrapPassword: os.Getenv("WISE_BOOTSTRAP_ADMIN_PASSWORD"),
		LogLevel:          getenv("WISE_LOG_LEVEL", "info"),
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
