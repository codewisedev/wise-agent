package modelgateway

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestClaudeCodeArgs(t *testing.T) {
	p := NewClaudeCode("cc", ProviderConfig{Binary: "claude"})
	args := p.Args(ProviderRequest{Model: "sonnet", System: "sys", Schema: json.RawMessage(`{"type":"object"}`), MaxCostUSD: 0.5})
	for _, want := range []string{"-p", "--no-session-persistence", "--strict-mcp-config", "--json-schema", "--max-budget-usd"} {
		if !slices.Contains(args, want) {
			t.Errorf("missing %s in %v", want, args)
		}
	}
	if slices.Contains(args, "--bare") {
		t.Error("--bare disables subscription auth and must not be used")
	}
	i := slices.Index(args, "--tools")
	if i < 0 || args[i+1] != "" {
		t.Errorf("tools must be disabled: %v", args)
	}
	i = slices.Index(args, "--setting-sources")
	if i < 0 || args[i+1] != "project" {
		t.Errorf("user settings must not load: %v", args)
	}
}

func TestParseClaudeResult(t *testing.T) {
	ok := `{"type":"result","subtype":"success","is_error":false,"result":"{\"action\":\"final\",\"final\":{}}",
	        "structured_output":{"action":"final","final":{"a":1}},"total_cost_usd":0.0123,
	        "usage":{"input_tokens":10,"output_tokens":20,"cache_read_input_tokens":5,"cache_creation_input_tokens":0}}`
	r, err := ParseClaudeResult([]byte(ok), "sonnet")
	if err != nil || r.CostUSD != 0.0123 || r.Usage.InputTokens != 15 || r.Usage.OutputTokens != 20 || len(r.JSON) == 0 {
		t.Fatalf("parse ok: %+v %v", r, err)
	}
	_, err = ParseClaudeResult([]byte(`{"type":"result","subtype":"error_during_execution","is_error":true,"result":"API Error: Overloaded"}`), "sonnet")
	if err == nil || !transient(err) {
		t.Fatalf("overloaded should be a transient error: %v", err)
	}
	_, err = ParseClaudeResult([]byte(`{"is_error":true,"result":"Invalid API key · Please run /login"}`), "sonnet")
	if err == nil || transient(err) || !providerWide(err) {
		t.Fatalf("auth error should be permanent and provider-wide: %v", err)
	}
	_, err = ParseClaudeResult([]byte(`{"is_error":true,"subtype":"error_max_budget_usd","result":"Exceeded budget"}`), "sonnet")
	if err == nil || providerWide(err) {
		t.Fatalf("budget errors are specific to one request: %v", err)
	}
}

func TestClaudeCodeEnvExcludesSecrets(t *testing.T) {
	t.Setenv("WISE_DATABASE_URL", "postgres://wise:secret@db/wise")
	t.Setenv("WISE_RUNTIME_TOKEN", "runtime-secret")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "oauth")
	env := strings.Join(NewClaudeCode("cc", ProviderConfig{}).Env(), "\n")
	if strings.Contains(env, "secret") {
		t.Fatalf("control-plane secrets leaked to the CLI env")
	}
	for _, want := range []string{"PATH=", "CLAUDE_CODE_OAUTH_TOKEN=oauth", "CLAUDE_CONFIG_DIR="} {
		if !strings.Contains(env, want) {
			t.Errorf("env missing %s", want)
		}
	}
}

func TestTranscript(t *testing.T) {
	if Transcript([]Message{{Role: "user", Content: "hi"}}) != "hi" {
		t.Fatal("single user message should pass through")
	}
	tr := Transcript([]Message{{Role: "user", Content: "q"}, {Role: "assistant", Content: "a"}, {Role: "tool", Name: "read_file", Content: "r"}})
	for _, want := range []string{"<user>", "<assistant>", `<tool_result name="read_file">`} {
		if !strings.Contains(tr, want) {
			t.Errorf("transcript missing %s", want)
		}
	}
}

// A fake `claude` executable checks the isolation flags, stdin and cwd.
func TestClaudeCodeCompleteWithFakeBinary(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "claude")
	body := `#!/bin/sh
input=$(cat)
case "$input" in *hello*) ;; *) echo '{"is_error":true,"result":"stdin missing"}'; exit 1;; esac
case "$(pwd)" in *wise-claude-*) ;; *) echo '{"is_error":true,"result":"not in temp dir"}'; exit 1;; esac
echo '{"type":"result","subtype":"success","is_error":false,"result":"","structured_output":{"action":"final","final":{"ok":true}},"total_cost_usd":0.01,"usage":{"input_tokens":3,"output_tokens":4}}'
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	p := NewClaudeCode("cc", ProviderConfig{Binary: script, Timeout: 10 * time.Second})
	r, err := p.Complete(context.Background(), ProviderRequest{Model: "sonnet", System: "s", Messages: []Message{{Role: "user", Content: "hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	env, err := ParseEnvelope(r.JSON, r.Text, nil)
	if err != nil || env.Action != ActionFinal || r.CostUSD != 0.01 {
		t.Fatalf("unexpected: %+v %+v %v", r, env, err)
	}
	missing := NewClaudeCode("cc", ProviderConfig{Binary: filepath.Join(dir, "nope"), Timeout: time.Second})
	if _, err := missing.Complete(context.Background(), ProviderRequest{Messages: []Message{{Role: "user", Content: "x"}}}); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing binary: %v", err)
	}
}
