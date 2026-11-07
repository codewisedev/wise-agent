package modelgateway

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// CodexCLI runs `codex exec` headless so a ChatGPT subscription can serve Wise.
//
// Each call gets a throwaway CODEX_HOME holding only the account's auth.json,
// an empty working directory, no user config, rules or session files, the
// read-only sandbox and the shell tools disabled. Codex still has other
// built-in tools, which is why Wise runs CLI providers in the secret-free
// wise-cli-runner container (see clirunner). Codex refreshes its OAuth tokens
// in auth.json; the refreshed file is returned as NewSecret.
type CodexCLI struct {
	name string
	cfg  ProviderConfig
}

func NewCodexCLI(name string, cfg ProviderConfig) *CodexCLI {
	if cfg.Binary == "" {
		cfg.Binary = "codex"
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Minute
	}
	return &CodexCLI{name: name, cfg: cfg}
}

func (p *CodexCLI) Name() string { return p.name }
func (p *CodexCLI) Cloud() bool  { return true }

// CodexArgs builds the CLI arguments (exported for tests).
func CodexArgs(model, schemaPath, instructionsPath, lastMessagePath string) []string {
	args := []string{
		"exec",
		"--skip-git-repo-check", "--ephemeral", "--ignore-user-config", "--ignore-rules",
		"--sandbox", "read-only",
		"--json", "--color", "never",
		"-o", lastMessagePath,
		"-c", fmt.Sprintf("model_instructions_file=%q", instructionsPath),
		"-c", `cli_auth_credentials_store="file"`,
		"--disable", "shell_tool", "--disable", "unified_exec",
	}
	if schemaPath != "" {
		args = append(args, "--output-schema", schemaPath)
	}
	if model != "" {
		args = append(args, "-m", model)
	}
	return append(args, "-") // prompt on stdin
}

func (p *CodexCLI) Complete(ctx context.Context, req ProviderRequest) (ProviderResponse, error) {
	if strings.TrimSpace(p.cfg.Secret) == "" {
		return ProviderResponse{}, &ProviderError{ProviderWide: true, Err: errors.New("codex: account has no auth.json (run `codex login` and paste ~/.codex/auth.json)")}
	}
	resp, err := p.run(ctx, req, true, p.cfg.Secret)
	var pe *ProviderError
	if errors.As(err, &pe) && pe.Status == statusSchemaRejected && len(req.Schema) > 0 {
		// Codex's structured output only accepts strict schemas; the envelope is
		// still validated by Wise, so retry with the schema in the prompt instead.
		// A token refreshed by the first attempt must be used (and kept).
		secret := p.cfg.Secret
		if resp.NewSecret != "" {
			secret = resp.NewSecret
		}
		r2, err2 := p.run(ctx, req, false, secret)
		if r2.NewSecret == "" && secret != p.cfg.Secret {
			r2.NewSecret = secret
		}
		return r2, err2
	}
	return resp, err
}

// statusSchemaRejected marks "the API refused our --output-schema".
const statusSchemaRejected = -2

func (p *CodexCLI) run(ctx context.Context, req ProviderRequest, withSchema bool, secret string) (ProviderResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, p.cfg.Timeout)
	defer cancel()
	workdir, err := os.MkdirTemp("", "wise-codex-*")
	if err != nil {
		return ProviderResponse{}, err
	}
	defer os.RemoveAll(workdir)
	home := filepath.Join(workdir, "home")
	cwd := filepath.Join(workdir, "work")
	for _, d := range []string{home, cwd} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return ProviderResponse{}, err
		}
	}
	authPath := filepath.Join(home, "auth.json")
	if err := os.WriteFile(authPath, []byte(secret), 0o600); err != nil {
		return ProviderResponse{}, err
	}
	system := req.System
	schemaPath := ""
	if len(req.Schema) > 0 {
		if withSchema {
			schemaPath = filepath.Join(workdir, "schema.json")
			if err := os.WriteFile(schemaPath, req.Schema, 0o600); err != nil {
				return ProviderResponse{}, err
			}
		} else {
			system += "\n\nYour final message must be a single JSON object matching this JSON Schema:\n" + string(req.Schema)
		}
	}
	instructions := filepath.Join(workdir, "instructions.md")
	if err := os.WriteFile(instructions, []byte(system), 0o600); err != nil {
		return ProviderResponse{}, err
	}
	last := filepath.Join(workdir, "last.txt")

	cmd := exec.CommandContext(ctx, p.cfg.Binary, CodexArgs(req.Model, schemaPath, instructions, last)...)
	cmd.Dir = cwd
	cmd.Env = codexEnv(home)
	cmd.Stdin = strings.NewReader(Transcript(req.Messages))
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return ProviderResponse{}, &ProviderError{Err: fmt.Errorf("codex: timed out after %s", p.cfg.Timeout)}
	}
	if runErr != nil {
		var execErr *exec.Error
		if errors.As(runErr, &execErr) || errors.Is(runErr, fs.ErrNotExist) {
			return ProviderResponse{}, &ProviderError{ProviderWide: true, Err: fmt.Errorf("codex: CLI not found (%s)", p.cfg.Binary)}
		}
	}
	text, _ := os.ReadFile(last)
	resp, perr := ParseCodexEvents(stdout.Bytes(), string(text), req.Model)
	if newAuth, err := os.ReadFile(authPath); err == nil && len(newAuth) > 0 && string(newAuth) != secret {
		resp.NewSecret = string(newAuth)
	}
	if perr != nil {
		var pe *ProviderError
		if errors.As(perr, &pe) && resp.NewSecret != "" {
			// Keep a refreshed token even when the call failed.
			return ProviderResponse{NewSecret: resp.NewSecret}, perr
		}
		return resp, perr
	}
	if runErr != nil && strings.TrimSpace(resp.Text) == "" {
		return resp, &ProviderError{Err: fmt.Errorf("codex: %v: %s", runErr, truncate(strings.TrimSpace(stderr.String()), 500))}
	}
	return resp, nil
}

// codexEnv is an allow-listed environment plus the per-call CODEX_HOME.
func codexEnv(home string) []string {
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch k {
		case "PATH", "USER", "TMPDIR", "TZ", "LANG", "TERM",
			"HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY", "https_proxy", "http_proxy", "no_proxy",
			"SSL_CERT_FILE", "SSL_CERT_DIR":
			env = append(env, kv)
		}
	}
	return append(env, "CODEX_HOME="+home, "HOME="+home, "NO_COLOR=1")
}

// ParseCodexEvents reads `codex exec --json` output (JSONL). lastMessage is the
// content of the -o file, preferred over agent_message events. Exported for tests.
func ParseCodexEvents(stdout []byte, lastMessage, model string) (ProviderResponse, error) {
	resp := ProviderResponse{Model: model, Text: strings.TrimSpace(lastMessage)}
	var agentText, failure string
	sc := bufio.NewScanner(bytes.NewReader(stdout))
	sc.Buffer(make([]byte, 0, 64*1024), 8<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var ev struct {
			Type    string `json:"type"`
			Message string `json:"message"`
			Error   *struct {
				Message string `json:"message"`
			} `json:"error"`
			Item *struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"item"`
			Usage *struct {
				InputTokens       int64 `json:"input_tokens"`
				CachedInputTokens int64 `json:"cached_input_tokens"`
				OutputTokens      int64 `json:"output_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal(line, &ev) != nil {
			continue
		}
		switch ev.Type {
		case "item.completed":
			if ev.Item != nil && ev.Item.Type == "agent_message" {
				agentText = ev.Item.Text
			}
		case "turn.completed":
			if ev.Usage != nil {
				resp.Usage.InputTokens += ev.Usage.InputTokens
				resp.Usage.OutputTokens += ev.Usage.OutputTokens
			}
		case "turn.failed":
			if ev.Error != nil {
				failure = ev.Error.Message
			}
		case "error":
			failure = ev.Message
		}
	}
	if resp.Text == "" {
		resp.Text = strings.TrimSpace(agentText)
	}
	if resp.Text != "" {
		return resp, nil
	}
	if failure == "" {
		failure = "no response"
	}
	lower := strings.ToLower(failure)
	pe := &ProviderError{
		Transient:    strings.Contains(lower, "rate limit") || strings.Contains(lower, "overloaded") || strings.Contains(lower, "timeout") || strings.Contains(lower, "reconnecting"),
		ProviderWide: isAuthError(lower) || strings.Contains(lower, "401") || strings.Contains(lower, "refresh_token") || strings.Contains(lower, "usage limit"),
		Err:          fmt.Errorf("codex: %s", truncate(failure, 500)),
	}
	if pe.ProviderWide {
		pe.Transient = false // retrying a login failure cannot help
	}
	if strings.Contains(lower, "schema") && (strings.Contains(lower, "invalid") || strings.Contains(lower, "strict")) {
		pe.Status, pe.ProviderWide, pe.Transient = statusSchemaRejected, false, false
	}
	return resp, pe
}

// Health checks that the CLI is installed.
func (p *CodexCLI) Health(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, p.cfg.Binary, "--version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("codex CLI unavailable: %v %s", err, truncate(strings.TrimSpace(string(out)), 200))
	}
	return nil
}
