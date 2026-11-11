package config

import (
	"net/url"
	"os"
	"strings"
)

// ExpandFileEnv reads Docker secrets: for every NAME_FILE variable whose
// NAME is unset or empty, NAME is set to the file's content (trailing newline
// trimmed). Call it first thing in main, before any configuration is read.
func ExpandFileEnv() error {
	for _, kv := range os.Environ() {
		k, path, _ := strings.Cut(kv, "=")
		name, ok := strings.CutSuffix(k, "_FILE")
		// Only Wise's own variables (other tools use NAME_FILE for other things).
		if !ok || !(strings.HasPrefix(name, "WISE_") || name == "CLAUDE_CODE_OAUTH_TOKEN") || path == "" || os.Getenv(name) != "" {
			continue
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.Setenv(name, strings.TrimRight(string(b), "\r\n")); err != nil {
			return err
		}
	}
	return nil
}

// withPassword adds WISE_POSTGRES_PASSWORD to a database URL that names a
// user but no password (so the URL itself can stay out of the secrets).
func withPassword(raw string) string {
	pw := os.Getenv("WISE_POSTGRES_PASSWORD")
	u, err := url.Parse(raw)
	if pw == "" || err != nil || u.User == nil {
		return raw
	}
	if _, set := u.User.Password(); set {
		return raw
	}
	u.User = url.UserPassword(u.User.Username(), pw)
	return u.String()
}
