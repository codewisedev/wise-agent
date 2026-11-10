package workspaces

import (
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"strings"

	"wise/apps/api/internal/apperr"
	"wise/apps/api/internal/executor"
)

// Settings is the panel-editable "execution" setting.
type Settings struct {
	Limits          Limits            `json:"limits"`
	EgressAllowlist []string          `json:"egress_allowlist"`
	CommandTimeoutS int               `json:"command_timeout_s"`
	SetupTimeoutS   int               `json:"setup_timeout_s"`
	MaxOutputKB     int               `json:"max_output_kb"`
	RetentionDays   int               `json:"retention_days"`
	MaxFixRounds    int               `json:"max_fix_rounds"`
	BranchTemplate  string            `json:"branch_template"`
	CommitAuthor    Author            `json:"commit_author"`
	Images          map[string]string `json:"images"`
	// ProtectedPaths are globs a task's change may not touch (checked on the
	// verified changeset at the quality gate, for every runtime).
	ProtectedPaths []string `json:"protected_paths,omitempty"`
}

// Limits bound a sandbox.
type Limits struct {
	CPUs     float64 `json:"cpus"`
	MemoryMB int64   `json:"memory_mb"`
	Pids     int64   `json:"pids"`
}

// Author is a commit identity.
type Author struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

var imageRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._/:@-]{0,254}$`)

// ParseSettings validates the execution setting.
func ParseSettings(raw json.RawMessage) (Settings, error) {
	var s Settings
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return s, apperr.Validation(apperr.FieldError{Field: "value", Message: err.Error()})
	}
	var fe []apperr.FieldError
	add := func(f, m string) { fe = append(fe, apperr.FieldError{Field: f, Message: m}) }
	if s.Limits.CPUs < 0.25 || s.Limits.CPUs > 32 {
		add("limits.cpus", "between 0.25 and 32")
	}
	if s.Limits.MemoryMB < 256 || s.Limits.MemoryMB > 131072 {
		add("limits.memory_mb", "between 256 and 131072")
	}
	if s.Limits.Pids < 64 || s.Limits.Pids > 32768 {
		add("limits.pids", "between 64 and 32768")
	}
	for i, h := range s.EgressAllowlist {
		if !executor.ValidHost(h) {
			add(fmt.Sprintf("egress_allowlist[%d]", i), "a host name like registry.npmjs.org or *.example.org (no IPs, ports or schemes)")
		}
	}
	if len(s.EgressAllowlist) > 200 {
		add("egress_allowlist", "at most 200 hosts")
	}
	if s.CommandTimeoutS < 10 || s.CommandTimeoutS > 6*3600 {
		add("command_timeout_s", "between 10 and 21600")
	}
	if s.SetupTimeoutS < 10 || s.SetupTimeoutS > 6*3600 {
		add("setup_timeout_s", "between 10 and 21600")
	}
	if s.MaxOutputKB < 4 || s.MaxOutputKB > 1024 {
		add("max_output_kb", "between 4 and 1024")
	}
	if s.RetentionDays < 1 || s.RetentionDays > 365 {
		add("retention_days", "between 1 and 365")
	}
	if s.MaxFixRounds < 0 || s.MaxFixRounds > 10 {
		add("max_fix_rounds", "between 0 and 10")
	}
	if !strings.Contains(s.BranchTemplate, "{slug}") && !strings.Contains(s.BranchTemplate, "{id}") && !strings.Contains(s.BranchTemplate, "{external}") {
		add("branch_template", "must contain {slug}, {id} or {external}")
	} else if _, ok := checkBranch(BranchName(s.BranchTemplate, "Example task", "0b8f8a52", "")); !ok {
		add("branch_template", "does not produce a valid git branch name")
	}
	if (s.CommitAuthor.Name == "") != (s.CommitAuthor.Email == "") || (s.CommitAuthor.Email != "" && !strings.Contains(s.CommitAuthor.Email, "@")) {
		add("commit_author", "set both a name and an email, or neither")
	}
	for i, g := range s.ProtectedPaths {
		if strings.TrimSpace(g) == "" || len(g) > 300 {
			add(fmt.Sprintf("protected_paths[%d]", i), "a glob like .github/workflows/**")
		} else if _, err := path.Match(strings.ReplaceAll(g, "**", "*"), "x"); err != nil {
			add(fmt.Sprintf("protected_paths[%d]", i), "invalid glob")
		}
	}
	for k, v := range s.Images {
		if !imageRe.MatchString(v) {
			add("images."+k, "an image reference like wise-toolchain-node:22")
		}
	}
	if len(fe) > 0 {
		return s, apperr.Validation(fe...)
	}
	return s, nil
}

// RepoSettings is a repository's execution setup (stored in its settings
// under "execution").
type RepoSettings struct {
	Image      string            `json:"image"`
	Setup      []string          `json:"setup"`
	Commands   Commands          `json:"commands"`
	Tools      []string          `json:"tools"`
	BaseBranch string            `json:"baseBranch"`
	Env        map[string]string `json:"env"`
}

// Commands are the repository's quality checks; empty ones are skipped.
type Commands struct {
	Format    string `json:"format"`
	Lint      string `json:"lint"`
	Typecheck string `json:"typecheck"`
	Test      string `json:"test"`
}

// List returns the configured checks in order.
func (c Commands) List() [][2]string {
	var out [][2]string
	for _, kv := range [][2]string{{"format", c.Format}, {"lint", c.Lint}, {"typecheck", c.Typecheck}, {"test", c.Test}} {
		if strings.TrimSpace(kv[1]) != "" {
			out = append(out, kv)
		}
	}
	return out
}

var (
	envName  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
	toolName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`)
)

// reservedEnv are set by Wise for every sandbox.
var reservedEnv = map[string]bool{"HOME": true, "PATH": true, "HTTP_PROXY": true, "HTTPS_PROXY": true, "http_proxy": true,
	"https_proxy": true, "NO_PROXY": true, "no_proxy": true}

// Validate checks a repository's execution settings.
func (r *RepoSettings) Validate() error {
	var fe []apperr.FieldError
	add := func(f, m string) { fe = append(fe, apperr.FieldError{Field: f, Message: m}) }
	r.Image = strings.TrimSpace(r.Image)
	if r.Image != "" && !imageRe.MatchString(r.Image) {
		add("image", "an image reference like wise-toolchain-node:22")
	}
	if len(r.Setup) > 20 {
		add("setup", "at most 20 commands")
	}
	for i, c := range r.Setup {
		if strings.TrimSpace(c) == "" || len(c) > 2000 {
			add(fmt.Sprintf("setup[%d]", i), "a command of at most 2000 characters")
		}
	}
	for _, kv := range [][2]string{{"format", r.Commands.Format}, {"lint", r.Commands.Lint}, {"typecheck", r.Commands.Typecheck}, {"test", r.Commands.Test}} {
		if len(kv[1]) > 2000 {
			add("commands."+kv[0], "at most 2000 characters")
		}
	}
	for i, t := range r.Tools {
		if !toolName.MatchString(t) {
			add(fmt.Sprintf("tools[%d]", i), "a command name like npm or go")
		}
	}
	if r.BaseBranch != "" {
		if _, ok := checkBranch(r.BaseBranch); !ok {
			add("baseBranch", "not a valid branch name")
		}
	}
	if len(r.Env) > 50 {
		add("env", "at most 50 variables")
	}
	for k, v := range r.Env {
		if !envName.MatchString(k) || reservedEnv[k] {
			add("env."+k, "a variable name (HOME, PATH and proxy variables are set by Wise)")
		}
		if len(v) > 4000 {
			add("env."+k, "at most 4000 characters")
		}
	}
	if len(fe) > 0 {
		return apperr.Validation(fe...)
	}
	return nil
}

// RepoSettingsFrom reads the execution settings from a repository's settings.
func RepoSettingsFrom(settings []byte) RepoSettings {
	var all struct {
		Execution RepoSettings `json:"execution"`
	}
	_ = json.Unmarshal(settings, &all)
	return all.Execution
}

// Protected reports the files of a change that match a protected glob
// ("**" matches any number of directories).
func Protected(globs []string, files []string) []string {
	var hit []string
	for _, f := range files {
		for _, g := range globs {
			if matchGlob(g, f) {
				hit = append(hit, f)
				break
			}
		}
	}
	return hit
}

func matchGlob(g, f string) bool {
	if !strings.Contains(g, "**") {
		ok, _ := path.Match(g, f)
		if !ok && !strings.Contains(g, "/") {
			ok, _ = path.Match(g, path.Base(f)) // "*.pem" matches anywhere
		}
		return ok
	}
	parts := strings.SplitN(g, "**", 2)
	prefix, rest := parts[0], strings.TrimPrefix(parts[1], "/")
	if !strings.HasPrefix(f, prefix) {
		return false
	}
	tail := strings.TrimPrefix(f, prefix)
	if rest == "" {
		return true
	}
	segs := strings.Split(tail, "/")
	for i := range segs {
		if matchGlob(rest, strings.Join(segs[i:], "/")) {
			return true
		}
	}
	return false
}
