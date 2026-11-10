package coding

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"wise/apps/api/internal/executor"
)

func spec(runtime, mode string) Spec {
	return Spec{SpecVersion: SpecVersion, RunID: "r1", Agent: "backend-agent", Runtime: runtime, CLIVersion: "2.1.319", Model: "sonnet",
		Instructions: "You are the Backend Agent.", Input: map[string]any{"request": "Add farewell()", "plan": map[string]any{"steps": []string{"S-1"}},
			"fix_feedback": "test failed: expected Goodbye"},
		OutputSchema:   json.RawMessage(`{"type":"object","required":["summary"],"properties":{"summary":{"type":"string"}}}`),
		ProtectedPaths: []string{".github/workflows/**"}, Limits: Limits{WallClockS: 1800, IdleS: 300, MaxTurns: 60, MaxCostUSD: 3},
		Credential: Credential{Mode: mode, Secret: "secret-value"}}
}

func has(args []string, pair ...string) bool {
	for i := 0; i+len(pair) <= len(args); i++ {
		if slices.Equal(args[i:i+len(pair)], pair) {
			return true
		}
	}
	return false
}

func TestClaudeInvocation(t *testing.T) {
	req, err := ClaudeCode{}.Build(spec("claude-code", ModeSubscription))
	if err != nil {
		t.Fatal(err)
	}
	a := req.Command
	for _, want := range [][]string{{"--output-format", "stream-json"}, {"--setting-sources", "user"}, {"--strict-mcp-config"},
		{"--tools", "Bash,Read,Edit,Write,NotebookEdit"}, {"--permission-mode", "dontAsk"}, {"--max-turns", "60"}, {"--model", "sonnet"},
		{"--max-budget-usd", "3.0000"}, {"--no-session-persistence"}} {
		if !has(a, want...) {
			t.Errorf("args lack %v: %v", want, a)
		}
	}
	if req.Env["CLAUDE_CODE_OAUTH_TOKEN"] != "secret-value" || req.Env["SHELL"] != "/bin/bash" || req.Env["ANTHROPIC_API_KEY"] != "" ||
		!slices.Equal(req.GrantHosts, []string{"api.anthropic.com"}) || req.ModelProxy {
		t.Fatalf("subscription env: %+v", req)
	}
	if req.StdinFile != promptFile || !strings.Contains(req.Files[promptFile], "test failed: expected Goodbye") ||
		!strings.Contains(req.Files[promptFile], ".github/workflows/**") || req.Files[instructionsFile] != "You are the Backend Agent." {
		t.Fatalf("files: %v", req.Files)
	}
	var settings map[string]any
	if json.Unmarshal([]byte(req.Files[claudeSettings]), &settings) != nil || settings["disableAllHooks"] != true {
		t.Fatalf("settings: %s", req.Files[claudeSettings])
	}
	// No secret on the command line.
	if strings.Contains(strings.Join(a, " "), "secret-value") {
		t.Fatal("credential on the command line")
	}

	proxy, _ := ClaudeCode{}.Build(spec("claude-code", ModeProxy))
	if proxy.Env["ANTHROPIC_BASE_URL"] != "http://wise-model-proxy:8095/anthropic" || proxy.Env["ANTHROPIC_API_KEY"] != "secret-value" ||
		proxy.Env["CLAUDE_CODE_OAUTH_TOKEN"] != "" || !proxy.ModelProxy || len(proxy.GrantHosts) != 0 {
		t.Fatalf("proxy env: %+v", proxy)
	}
	bad := spec("claude-code", "plaintext")
	if _, err := (ClaudeCode{}).Build(bad); err == nil {
		t.Fatal("unknown credential mode accepted")
	}
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Recorded from Claude Code 2.1.319 in a Wise runner (subscription mode).
func TestClaudeRecordedSuccess(t *testing.T) {
	a := ClaudeCode{}
	evs := ParseAll(a, fixture(t, "claude-2.1.319-success.jsonl"))
	var calls, results, msgs int
	for _, e := range evs {
		switch e.Type {
		case "run.tool_call":
			calls++
		case "run.tool_result":
			results++
		case "run.message":
			msgs++
		}
	}
	if calls == 0 || results == 0 {
		t.Fatalf("events: %+v", evs)
	}
	o := a.Outcome(executor.RunnerResult{ExitCode: 0}, evs)
	var out struct {
		Files  []string `json:"files"`
		Answer string   `json:"answer"`
	}
	if o.Class != Succeeded || json.Unmarshal(o.Output, &out) != nil || len(out.Files) != 1 || o.Usage.CostUSD <= 0 || o.Usage.Turns != 5 {
		t.Fatalf("outcome: %+v", o)
	}
}

func TestClaudeRecordedMaxTurns(t *testing.T) {
	a := ClaudeCode{}
	o := a.Outcome(executor.RunnerResult{ExitCode: 1}, ParseAll(a, fixture(t, "claude-2.1.319-max-turns.jsonl")))
	if o.Class != Incomplete || !strings.Contains(o.Message, "turns") {
		t.Fatalf("outcome: %+v", o)
	}
}

func TestClaudeFailures(t *testing.T) {
	a := ClaudeCode{}
	cases := map[string]string{
		`{"type":"result","subtype":"error_during_execution","is_error":true,"result":"API Error: 401 {\"type\":\"error\",\"error\":{\"type\":\"authentication_error\"}}"}`: AuthError,
		`{"type":"result","subtype":"error_during_execution","is_error":true,"result":"API Error: 429 rate limit exceeded"}`:                                                RateLimited,
		`{"type":"result","subtype":"error_max_budget_usd","is_error":true}`:                                                                                                BudgetExceeded,
		`{"type":"result","subtype":"success","is_error":false,"result":"done","structured_output":null}`:                                                                   Incomplete,
	}
	for line, want := range cases {
		if o := a.Outcome(executor.RunnerResult{}, ParseAll(a, line)); o.Class != want {
			t.Errorf("%s → %s, want %s (%s)", line[:60], o.Class, want, o.Message)
		}
	}
	if o := a.Outcome(executor.RunnerResult{IdleOut: true}, nil); o.Class != Timeout {
		t.Errorf("idle: %+v", o)
	}
	if o := a.Outcome(executor.RunnerResult{ExitCode: 137}, nil); o.Class != RuntimeCrash {
		t.Errorf("crash: %+v", o)
	}
	// Tokens in tool output are redacted in events.
	line := `{"type":"user","message":{"content":[{"type":"tool_result","content":"key sk-ant-abcdefghijklmnopqrstuvwxyz0123"}]}}`
	if evs := a.Parse([]byte(line)); len(evs) != 1 || strings.Contains(evs[0].Output, "abcdefghij") {
		t.Errorf("redaction: %+v", evs)
	}
}

func TestCodexInvocationAndEvents(t *testing.T) {
	req, err := Codex{}.Build(spec("codex", ModeSubscription))
	if err != nil {
		t.Fatal(err)
	}
	a := req.Command
	if a[1] != "exec" || a[len(a)-1] != "-" || !has(a, "--json") || !has(a, "--dangerously-bypass-approvals-and-sandbox") ||
		!has(a, "-c", "check_for_update_on_startup=false") || !has(a, "-m", "sonnet") {
		t.Fatalf("args: %v", a)
	}
	if req.Files[codexAuth] != "secret-value" || req.Files[codexAgents] != "You are the Backend Agent." || !slices.Contains(req.Collect, codexAuth) ||
		!slices.Contains(req.GrantHosts, "chatgpt.com") || req.Env["CODEX_HOME"] != "/home/wise/.codex" {
		t.Fatalf("subscription: %+v", req)
	}
	proxy, _ := Codex{}.Build(spec("codex", ModeProxy))
	if proxy.Env["WISE_RUN_TOKEN"] != "secret-value" || !proxy.ModelProxy || proxy.Files[codexAuth] != "" ||
		!strings.Contains(strings.Join(proxy.Command, " "), `base_url = "http://wise-model-proxy:8095/openai/v1"`) {
		t.Fatalf("proxy: %+v", proxy)
	}
	ws := spec("codex", ModeProxy)
	ws.Sandbox = "workspace-write"
	if r, _ := (Codex{}).Build(ws); !has(r.Command, "--sandbox", "workspace-write") {
		t.Fatal("workspace-write sandbox")
	}

	stream := strings.Join([]string{
		`{"type":"thread.started","thread_id":"t"}`,
		`{"type":"turn.started"}`,
		`{"type":"item.started","item":{"id":"1","type":"command_execution","command":"npm test","status":"in_progress"}}`,
		`{"type":"item.completed","item":{"id":"1","type":"command_execution","command":"npm test","aggregated_output":"ok 1","exit_code":0,"status":"completed"}}`,
		`{"type":"item.completed","item":{"id":"2","type":"file_change","changes":[{"path":"src/greet.js","kind":"update"}],"status":"completed"}}`,
		`{"type":"item.completed","item":{"id":"3","type":"agent_message","text":"{\"summary\":\"done\"}"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":1200,"cached_input_tokens":100,"output_tokens":300}}`,
	}, "\n")
	evs := ParseAll(Codex{}, stream)
	if len(evs) != 5 || evs[1].Type != "run.command" || evs[2].Path != "src/greet.js" {
		t.Fatalf("events: %+v", evs)
	}
	o := Codex{}.Outcome(executor.RunnerResult{Collected: map[string]string{codexLastOutput: `{"summary":"from file"}`, codexAuth: `{"tokens":{}}`}}, evs)
	if o.Class != Succeeded || string(o.Output) != `{"summary":"from file"}` || o.Usage.InputTokens != 1200 || o.Files[codexAuth] == "" {
		t.Fatalf("outcome: %+v", o)
	}
	failed := ParseAll(Codex{}, `{"type":"turn.failed","error":{"message":"You've hit your usage limit"}}`)
	if o := (Codex{}).Outcome(executor.RunnerResult{}, failed); o.Class != RateLimited {
		t.Fatalf("usage limit: %+v", o)
	}
}

func TestSpecValidation(t *testing.T) {
	s := spec("claude-code", ModeProxy)
	s.SpecVersion = 2
	s.Instructions = ""
	s.Credential.Secret = ""
	err := s.Validate()
	if err == nil || !strings.Contains(err.Error(), "specVersion") || !strings.Contains(err.Error(), "instructions") || !strings.Contains(err.Error(), "credential") {
		t.Fatalf("validation: %v", err)
	}
	b, _ := json.Marshal(spec("claude-code", ModeProxy))
	if strings.Contains(string(b), "secret-value") {
		t.Fatal("a serialized spec must not carry the credential")
	}
}

// The serialized spec satisfies the published contract.
func TestSpecMatchesContract(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "packages", "contracts", "agents", "run-spec.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("run-spec.schema.json", doc); err != nil {
		t.Fatal(err)
	}
	sch, err := c.Compile("run-spec.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	s := spec("claude-code", ModeSubscription)
	b, _ := json.Marshal(s)
	v, _ := jsonschema.UnmarshalJSON(bytes.NewReader(b))
	if err := sch.Validate(v); err != nil {
		t.Fatalf("spec does not match the contract: %v", err)
	}
}
