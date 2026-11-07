package modelgateway

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// fakeCodex writes a `codex` script that behaves like `codex exec --json`.
// mode "ok" answers and rotates auth.json; "auth" fails with 401.
func fakeCodex(t *testing.T, mode string) string {
	t.Helper()
	dir := t.TempDir()
	script := `#!/bin/sh
last=""; prev=""
for a in "$@"; do [ "$prev" = "-o" ] && last="$a"; prev="$a"; done
[ -f "$CODEX_HOME/auth.json" ] || { echo '{"type":"error","message":"not logged in"}'; exit 1; }
input=$(cat)
if [ "` + mode + `" = "auth" ]; then
  echo '{"type":"turn.failed","error":{"message":"unexpected status 401 Unauthorized"}}'; exit 1
fi
echo '{"type":"thread.started","thread_id":"t"}'
echo '{"type":"item.completed","item":{"type":"agent_message","text":"ignored when -o exists"}}'
echo '{"type":"turn.completed","usage":{"input_tokens":120,"cached_input_tokens":20,"output_tokens":30}}'
printf '%s' '{"action":"final","final":{"ok":true}}' > "$last"
printf '%s' '{"tokens":{"refresh":"rotated"}}' > "$CODEX_HOME/auth.json"
case "$input" in *secret-env*) env > "$CODEX_HOME/../env.txt";; esac
`
	if err := os.WriteFile(filepath.Join(dir, "codex"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestCodexArgsAreLockedDown(t *testing.T) {
	args := CodexArgs("gpt-5", "/s.json", "/i.md", "/last.txt")
	for _, want := range []string{"--ephemeral", "--ignore-user-config", "--ignore-rules", "--skip-git-repo-check", "read-only", "shell_tool", "unified_exec", "--output-schema", "-"} {
		if !slices.Contains(args, want) {
			t.Errorf("missing %q in %v", want, args)
		}
	}
	if args[len(args)-1] != "-" {
		t.Fatal("prompt must come from stdin")
	}
}

func TestCodexCLICompletesAndRefreshesAuth(t *testing.T) {
	dir := fakeCodex(t, "ok")
	p := NewCodexCLI("cx", ProviderConfig{Binary: filepath.Join(dir, "codex"), Secret: `{"tokens":{"refresh":"old"}}`, Timeout: 10 * time.Second})
	r, err := p.Complete(context.Background(), ProviderRequest{System: "sys", Messages: []Message{{Role: "user", Content: "q"}}, Schema: []byte(`{"type":"object"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if r.Text != `{"action":"final","final":{"ok":true}}` || r.Usage.InputTokens != 120 || r.Usage.OutputTokens != 30 {
		t.Fatalf("response: %+v", r)
	}
	if r.NewSecret != `{"tokens":{"refresh":"rotated"}}` {
		t.Fatalf("refreshed auth.json must be returned, got %q", r.NewSecret)
	}
	if !p.Cloud() {
		t.Fatal("codex is always cloud")
	}
}

func TestCodexCLIAuthFailureIsProviderWide(t *testing.T) {
	dir := fakeCodex(t, "auth")
	p := NewCodexCLI("cx", ProviderConfig{Binary: filepath.Join(dir, "codex"), Secret: `{}`, Timeout: 10 * time.Second})
	_, err := p.Complete(context.Background(), ProviderRequest{Messages: []Message{{Role: "user", Content: "q"}}})
	var pe *ProviderError
	if !errors.As(err, &pe) || !pe.ProviderWide || pe.Transient {
		t.Fatalf("want provider-wide, non-transient error, got %v", err)
	}
	empty := NewCodexCLI("cx", ProviderConfig{Binary: filepath.Join(dir, "codex")})
	if _, err := empty.Complete(context.Background(), ProviderRequest{}); !errors.As(err, &pe) || !pe.ProviderWide {
		t.Fatalf("an account without auth.json must fail clearly: %v", err)
	}
}

func TestCodexEnvHasNoControlPlaneSecrets(t *testing.T) {
	t.Setenv("WISE_DATABASE_URL", "postgres://secret")
	t.Setenv("WISE_SECRETS_KEY", "key")
	env := strings.Join(codexEnv("/tmp/home"), "\n")
	if strings.Contains(env, "WISE_") || !strings.Contains(env, "CODEX_HOME=/tmp/home") {
		t.Fatalf("env leaks or misses CODEX_HOME:\n%s", env)
	}
}

func TestCLIRunnerRoundTrip(t *testing.T) {
	dir := fakeCodex(t, "ok")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	const token = "runner-token-0123456789abcdef0123456789"
	srv := httptest.NewServer(RunnerHandler(token, slog.New(slog.NewTextHandler(io.Discard, nil))))
	defer srv.Close()

	p := NewRemoteCLI("my-codex", "codex-cli", srv.URL, token, ProviderConfig{Secret: `{"tokens":{}}`, Timeout: 30 * time.Second})
	r, err := p.Complete(context.Background(), ProviderRequest{Messages: []Message{{Role: "user", Content: "q"}}})
	if err != nil {
		t.Fatal(err)
	}
	if r.Text == "" || r.NewSecret == "" {
		t.Fatalf("runner response: %+v", r)
	}
	bad := NewRemoteCLI("my-codex", "codex-cli", srv.URL, "wrong-token-0123456789abcdef0123456789", ProviderConfig{Timeout: 5 * time.Second})
	var pe *ProviderError
	if _, err := bad.Complete(context.Background(), ProviderRequest{}); !errors.As(err, &pe) || pe.Status != 401 {
		t.Fatalf("a wrong runner token must be rejected: %v", err)
	}
	unknown := NewRemoteCLI("x", "bash", srv.URL, token, ProviderConfig{Timeout: 5 * time.Second})
	if _, err := unknown.Complete(context.Background(), ProviderRequest{}); err == nil {
		t.Fatal("the runner must only run known CLI kinds")
	}
}
