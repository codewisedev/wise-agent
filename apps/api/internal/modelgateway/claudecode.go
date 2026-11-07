package modelgateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ClaudeCode runs the Claude Code CLI in headless print mode, so a Claude
// subscription can serve Wise without an API key.
//
// Isolation: an empty temporary working directory, only project settings (none
// exist there, so user hooks/plugins are not loaded), no built-in tools, no MCP
// servers, a replaced system prompt and no session persistence. --bare is NOT
// used because it disables subscription (OAuth) authentication. When
// CLAUDE_CODE_OAUTH_TOKEN is set (from `claude setup-token`), the CLI also gets
// a dedicated empty config directory.
type ClaudeCode struct {
	name string
	cfg  ProviderConfig
}

func NewClaudeCode(name string, cfg ProviderConfig) *ClaudeCode {
	return &ClaudeCode{name: name, cfg: cfg}
}

func (p *ClaudeCode) Name() string { return p.name }
func (p *ClaudeCode) Cloud() bool  { return p.cfg.Cloud }

// Args builds the CLI arguments (exported for tests).
func (p *ClaudeCode) Args(req ProviderRequest) []string {
	args := []string{
		"-p",
		"--output-format", "json",
		"--no-session-persistence",
		"--setting-sources", "project",
		"--strict-mcp-config",
		"--tools", "",
		"--system-prompt", req.System,
	}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	if len(req.Schema) > 0 {
		args = append(args, "--json-schema", string(req.Schema))
	}
	if req.MaxCostUSD > 0 {
		args = append(args, "--max-budget-usd", strconv.FormatFloat(req.MaxCostUSD, 'f', 4, 64))
	}
	return args
}

// Transcript renders the conversation for stdin (the CLI call is stateless).
func Transcript(msgs []Message) string {
	if len(msgs) == 1 && msgs[0].Role == "user" {
		return msgs[0].Content
	}
	var b strings.Builder
	b.WriteString("Conversation so far (oldest first):\n")
	for _, m := range msgs {
		switch m.Role {
		case "assistant":
			b.WriteString("\n<assistant>\n" + m.Content + "\n</assistant>\n")
		case "tool":
			b.WriteString("\n<tool_result name=\"" + m.Name + "\">\n" + m.Content + "\n</tool_result>\n")
		default:
			b.WriteString("\n<user>\n" + m.Content + "\n</user>\n")
		}
	}
	b.WriteString("\nContinue as the assistant: respond to the latest message following the response protocol.")
	return b.String()
}

type claudeResult struct {
	Type             string          `json:"type"`
	Subtype          string          `json:"subtype"`
	IsError          bool            `json:"is_error"`
	Result           string          `json:"result"`
	StructuredOutput json.RawMessage `json:"structured_output"`
	TotalCostUSD     float64         `json:"total_cost_usd"`
	Usage            struct {
		InputTokens              int64 `json:"input_tokens"`
		OutputTokens             int64 `json:"output_tokens"`
		CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	} `json:"usage"`
}

// ParseClaudeResult decodes `claude -p --output-format json` output (exported for tests).
func ParseClaudeResult(stdout []byte, model string) (ProviderResponse, error) {
	var r claudeResult
	if err := json.Unmarshal(bytes.TrimSpace(stdout), &r); err != nil {
		return ProviderResponse{}, &ProviderError{Err: fmt.Errorf("claude-code: decode output: %w", err)}
	}
	if r.IsError || (r.Subtype != "" && r.Subtype != "success") {
		msg := r.Result
		if msg == "" {
			msg = r.Subtype
		}
		lower := strings.ToLower(msg)
		return ProviderResponse{}, &ProviderError{
			Transient:    strings.Contains(lower, "overloaded") || strings.Contains(lower, "rate limit") || strings.Contains(lower, "timeout"),
			ProviderWide: isAuthError(lower),
			Err:          fmt.Errorf("claude-code: %s", truncate(msg, 500)),
		}
	}
	return ProviderResponse{
		Text: r.Result,
		JSON: r.StructuredOutput,
		Usage: Usage{
			InputTokens:  r.Usage.InputTokens + r.Usage.CacheCreationInputTokens + r.Usage.CacheReadInputTokens,
			OutputTokens: r.Usage.OutputTokens,
		},
		CostUSD: r.TotalCostUSD,
		Model:   model,
	}, nil
}

func (p *ClaudeCode) Complete(ctx context.Context, req ProviderRequest) (ProviderResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, p.cfg.Timeout)
	defer cancel()
	workdir, err := os.MkdirTemp("", "wise-claude-*")
	if err != nil {
		return ProviderResponse{}, err
	}
	defer os.RemoveAll(workdir)

	cmd := exec.CommandContext(ctx, p.cfg.Binary, p.Args(req)...)
	cmd.Dir = workdir
	cmd.Env = p.env(workdir)
	cmd.Stdin = strings.NewReader(Transcript(req.Messages))
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		// Not transient: retrying a full timeout would outlast the model-turn deadline.
		return ProviderResponse{}, &ProviderError{Err: fmt.Errorf("claude-code: timed out after %s", p.cfg.Timeout)}
	}
	if stdout.Len() > 0 {
		// The CLI reports errors (auth, budget) as JSON on stdout with a non-zero exit.
		if resp, err := ParseClaudeResult(stdout.Bytes(), req.Model); err == nil || runErr != nil {
			return resp, err
		}
	}
	if runErr != nil {
		var execErr *exec.Error
		if errors.As(runErr, &execErr) || errors.Is(runErr, fs.ErrNotExist) {
			return ProviderResponse{}, &ProviderError{ProviderWide: true, Err: fmt.Errorf("claude-code: CLI not found (%s)", p.cfg.Binary)}
		}
		return ProviderResponse{}, &ProviderError{Err: fmt.Errorf("claude-code: %v: %s", runErr, truncate(strings.TrimSpace(stderr.String()), 500))}
	}
	return ParseClaudeResult(stdout.Bytes(), req.Model)
}

// Env builds the CLI environment from an allow-list, so control-plane secrets
// (database URL, runtime token, bootstrap password, ...) never reach the CLI.
// Exported for tests.
func (p *ClaudeCode) Env() []string { return p.env(os.TempDir()) }

func allowedEnv(key string) bool {
	switch key {
	case "PATH", "HOME", "USER", "TMPDIR", "TZ", "LANG", "TERM", "SHELL",
		"HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY", "https_proxy", "http_proxy", "no_proxy",
		"SSL_CERT_FILE", "SSL_CERT_DIR", "NODE_EXTRA_CA_CERTS":
		return true
	}
	return strings.HasPrefix(key, "LC_") || strings.HasPrefix(key, "XDG_") ||
		strings.HasPrefix(key, "CLAUDE_") || strings.HasPrefix(key, "ANTHROPIC_")
}

func isAuthError(lower string) bool {
	for _, s := range []string{"not logged in", "/login", "invalid api key", "unauthorized", "authentication", "oauth token"} {
		if strings.Contains(lower, s) {
			return true
		}
	}
	return false
}

func (p *ClaudeCode) env(workdir string) []string {
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if p.cfg.Secret != "" && (k == "CLAUDE_CODE_OAUTH_TOKEN" || k == "CLAUDE_CONFIG_DIR" || strings.HasPrefix(k, "ANTHROPIC_")) {
			continue // a stored account brings its own credentials
		}
		if allowedEnv(k) {
			env = append(env, kv)
		}
	}
	if p.cfg.Secret != "" {
		// Per-call config directory: accounts never share CLI state.
		dir := filepath.Join(workdir, ".claude-config")
		if err := os.MkdirAll(dir, 0o700); err == nil {
			env = append(env, "CLAUDE_CONFIG_DIR="+dir)
		}
		env = append(env, "CLAUDE_CODE_OAUTH_TOKEN="+p.cfg.Secret)
	} else if os.Getenv("CLAUDE_CODE_OAUTH_TOKEN") != "" {
		dir := filepath.Join(os.TempDir(), "wise-claude-config")
		if err := os.MkdirAll(dir, 0o700); err == nil {
			env = append(env, "CLAUDE_CONFIG_DIR="+dir)
		}
	}
	return append(env, "DISABLE_AUTOUPDATER=1", "DISABLE_TELEMETRY=1", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1")
}

// Health checks that the CLI is installed (authentication is verified on first use).
func (p *ClaudeCode) Health(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, p.cfg.Binary, "--version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("claude CLI unavailable: %v %s", err, truncate(strings.TrimSpace(string(out)), 200))
	}
	return nil
}
