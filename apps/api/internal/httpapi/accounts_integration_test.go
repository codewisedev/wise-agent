package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeOpenAI is an OpenAI-compatible server that records the bearer token it saw.
func fakeOpenAI(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var auths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auths = append(auths, r.Header.Get("Authorization"))
		mu.Unlock()
		switch r.URL.Path {
		case "/v1/models":
			_, _ = w.Write([]byte(`{"data":[{"id":"deepseek-chat"}]}`))
		case "/v1/chat/completions":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			final, _ := json.Marshal(map[string]any{"action": "final", "final": validProductOutput})
			resp, _ := json.Marshal(map[string]any{
				"model":   body["model"],
				"choices": []any{map[string]any{"message": map[string]any{"content": string(final)}}},
				"usage":   map[string]any{"prompt_tokens": 12, "completion_tokens": 8},
			})
			_, _ = w.Write(resp)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), auths...) }
}

func TestAccountsLifecycleAndSecrecy(t *testing.T) {
	e := setup(t)
	e.login()
	srv, auths := fakeOpenAI(t)

	// Validation: unknown kind, bad URL, missing secret for CLI kinds, codex secret must be JSON.
	e.expect(e.do("POST", "/accounts", map[string]any{"name": "x", "kind": "nope"}), 400)
	e.expect(e.do("POST", "/accounts", map[string]any{"name": "ds", "kind": "openai_compatible", "baseUrl": "ftp://x"}), 400)
	e.expect(e.do("POST", "/accounts", map[string]any{"name": "cc", "kind": "claude_code"}), 400)
	e.expect(e.do("POST", "/accounts", map[string]any{"name": "cx", "kind": "codex_cli", "secret": "not json"}), 400)
	e.expect(e.do("POST", "/accounts", map[string]any{"name": "scripted", "kind": "openai_compatible", "baseUrl": srv.URL}), 400) // built-in name

	acct := e.expect(e.do("POST", "/accounts", map[string]any{
		"name": "deepseek-main", "kind": "openai_compatible", "baseUrl": srv.URL + "/v1",
		"defaultModel": "deepseek-chat", "secret": "sk-deepseek-super-secret-1234",
	}), 201)
	if acct["hasSecret"] != true || acct["secretHint"] != "…1234" || acct["cloud"] != false {
		t.Fatalf("account: %v", acct) // httptest listens on 127.0.0.1 → local, not cloud
	}
	raw, _ := json.Marshal(e.expect(e.do("GET", "/accounts", nil), 200))
	if strings.Contains(string(raw), "super-secret") {
		t.Fatal("the secret must never be returned")
	}
	var stored string
	_ = e.db.Pool.QueryRow(context.Background(), `SELECT secret_ciphertext FROM accounts WHERE name = 'deepseek-main'`).Scan(&stored)
	if !strings.HasPrefix(stored, "v1:") || strings.Contains(stored, "super-secret") {
		t.Fatalf("secret must be encrypted at rest: %q", stored)
	}
	e.expect(e.do("POST", "/accounts", map[string]any{"name": "deepseek-main", "kind": "openai_compatible", "baseUrl": srv.URL}), 409)

	if res := e.expect(e.do("POST", "/accounts/"+acct["id"].(string)+"/test", nil), 200); res["ok"] != true {
		t.Fatalf("test: %v", res)
	}
	if got := auths(); len(got) == 0 || got[len(got)-1] != "Bearer sk-deepseek-super-secret-1234" {
		t.Fatalf("the gateway must send the account's key: %v", got)
	}

	// A run pinned to the account uses it for every turn, and the choice is validated up front.
	e.expect(e.do("POST", "/agents/product-agent/runs", map[string]any{"input": map[string]any{"request": "Build a thing"}, "account": "nope"}), 400)
	run := e.expect(e.do("POST", "/agents/product-agent/runs", map[string]any{
		"input": map[string]any{"request": "Build a thing"}, "account": "deepseek-main",
	}), 202)
	runID, token := e.claim()
	if runID != run["id"] {
		t.Fatalf("claimed %s, want %s", runID, run["id"])
	}
	turn := e.expect(e.runtimeDo(token, "POST", "/runtime/v1/runs/"+runID+"/model-turn",
		map[string]any{"messages": []map[string]string{{"role": "user", "content": "go"}}}), 200)
	if turn["provider"] != "deepseek-main" || turn["model"] != "deepseek-chat" {
		t.Fatalf("turn must use the pinned account: %v", turn)
	}

	// Rotating the secret takes effect immediately; disabling removes the account from routing.
	id := acct["id"].(string)
	e.expect(e.do("PATCH", "/accounts/"+id, map[string]any{"secret": "sk-rotated-value-9999"}), 200)
	e.expect(e.do("POST", "/accounts/"+id+"/test", nil), 200)
	if got := auths(); got[len(got)-1] != "Bearer sk-rotated-value-9999" {
		t.Fatalf("rotated key not used: %v", got[len(got)-1])
	}
	e.expect(e.do("PATCH", "/accounts/"+id, map[string]any{"enabled": false}), 200)
	e.expect(e.do("POST", "/agents/product-agent/runs", map[string]any{"input": map[string]any{"request": "x y z"}, "account": "deepseek-main"}), 400)

	// The stored key never follows the account to another host, and the cloud
	// classification cannot be relaxed by the user.
	e.expect(e.do("PATCH", "/accounts/"+id, map[string]any{"baseUrl": "https://attacker.example/v1"}), 400)
	moved := e.expect(e.do("PATCH", "/accounts/"+id, map[string]any{"baseUrl": "https://api.deepseek.com/v1", "secret": "sk-new-key-for-new-host"}), 200)
	if moved["cloud"] != true {
		t.Fatalf("a public endpoint is cloud: %v", moved)
	}
	remote := e.expect(e.do("POST", "/accounts", map[string]any{
		"name": "sneaky", "kind": "openai_compatible", "baseUrl": "https://api.openai.com/v1", "cloud": false, "secret": "sk-x-123456789012",
	}), 201)
	if remote["cloud"] != true {
		t.Fatalf("cloud:false must not override a public URL: %v", remote)
	}
	local := e.expect(e.do("POST", "/accounts", map[string]any{
		"name": "strict-local", "kind": "openai_compatible", "baseUrl": "http://127.0.0.1:1234/v1", "cloud": true,
	}), 201)
	if local["cloud"] != true {
		t.Fatalf("marking a local endpoint as cloud (stricter) is allowed: %v", local)
	}

	// API tokens cannot manage accounts.
	secret := e.expect(e.do("POST", "/auth/tokens", map[string]string{"name": "auto"}), 201)["secret"].(string)
	e.expect(e.do("POST", "/auth/logout", nil), 204)
	e.csrf, e.bearer = "", secret
	e.expect(e.do("GET", "/accounts", nil), 200)
	e.expect(e.do("POST", "/accounts", map[string]any{"name": "t", "kind": "openai_compatible", "baseUrl": srv.URL}), 403)
	e.expect(e.do("DELETE", "/accounts/"+id, nil), 403)
}
