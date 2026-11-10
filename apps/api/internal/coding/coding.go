// Package coding holds the adapters that let implementation steps delegate
// to external coding CLIs (Claude Code, Codex). Adapters are stateless
// translators: they render a RunSpec into a runner invocation (command,
// environment, files) and normalize the CLI's JSONL event stream. Launching,
// supervision and cleanup belong to the executor; the result is built by Wise
// (output validated against the agent's contract, the diff verified at the
// gate), never taken on trust from the CLI.
package coding

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"wise/apps/api/internal/executor"
	"wise/apps/api/internal/secretguard"
)

// SpecVersion is the RunSpec contract version (packages/contracts/agents/run-spec.schema.json).
const SpecVersion = 1

// Credential modes.
const (
	ModeProxy        = "proxy"        // API key held by the Runtime Proxy; the runner gets a run token
	ModeSubscription = "subscription" // vendor token in the runner (reduced isolation)
)

// Credential tells an adapter how the runner authenticates.
type Credential struct {
	Mode   string `json:"mode"`
	Secret string `json:"-"` // the run token (proxy) or the subscription credential
}

// Limits bound one run.
type Limits struct {
	WallClockS int     `json:"wallClockS"`
	IdleS      int     `json:"idleS"`
	MaxTurns   int     `json:"maxTurns"`
	MaxCostUSD float64 `json:"maxCostUsd"`
}

// Spec is the RunSpec: everything a runtime gets for one run, no secrets in
// its serialized form.
type Spec struct {
	SpecVersion    int             `json:"specVersion"`
	RunID          string          `json:"runId"`
	TaskID         string          `json:"taskId,omitempty"`
	Agent          string          `json:"agent"`
	Runtime        string          `json:"runtime"`
	CLIVersion     string          `json:"cliVersion"`
	Model          string          `json:"model,omitempty"`
	Instructions   string          `json:"instructions"`      // the agent's system prompt with Wise's rules and context
	Input          map[string]any  `json:"input"`             // the agent's input (task, plan, fix feedback, …)
	OutputSchema   json.RawMessage `json:"outputSchema"`      // the agent's output contract
	ProtectedPaths []string        `json:"protectedPaths"`    // never to be changed
	Tools          []string        `json:"tools,omitempty"`   // Claude Code: built-in tools offered
	Sandbox        string          `json:"sandbox,omitempty"` // Codex: danger-full-access | workspace-write
	Limits         Limits          `json:"limits"`
	Credential     Credential      `json:"credential"`
}

// Validate checks a spec before it is rendered.
func (s Spec) Validate() error {
	var errs []string
	if s.SpecVersion != SpecVersion {
		errs = append(errs, fmt.Sprintf("specVersion must be %d", SpecVersion))
	}
	if s.RunID == "" || s.Agent == "" || s.CLIVersion == "" {
		errs = append(errs, "runId, agent and cliVersion are required")
	}
	if strings.TrimSpace(s.Instructions) == "" {
		errs = append(errs, "instructions are required")
	}
	if !json.Valid(s.OutputSchema) {
		errs = append(errs, "outputSchema must be JSON")
	}
	if s.Credential.Mode != ModeProxy && s.Credential.Mode != ModeSubscription {
		errs = append(errs, "credential mode must be proxy or subscription")
	}
	if s.Credential.Secret == "" {
		errs = append(errs, "a credential is required")
	}
	if s.Limits.WallClockS <= 0 || s.Limits.MaxTurns <= 0 {
		errs = append(errs, "limits are required")
	}
	if len(errs) > 0 {
		return fmt.Errorf("run spec: %s", strings.Join(errs, "; "))
	}
	return nil
}

// Prompt renders the task for the CLI: what to do, the approved inputs, the
// rules that are enforced outside the agent, and how to finish.
func (s Spec) Prompt() string {
	var b strings.Builder
	b.WriteString("You are working in /workspace, a private copy of the repository on the task's branch.\n\n")
	if req, ok := s.Input["request"].(string); ok {
		b.WriteString("## Task\n" + req + "\n\n")
	}
	if fb, ok := s.Input["fix_feedback"].(string); ok && fb != "" {
		b.WriteString("## Fix these first\nThe quality gate found these problems in your previous work (still in the workspace):\n" + fb + "\n\n")
	}
	if fb, ok := s.Input["feedback"].(string); ok && fb != "" {
		b.WriteString("## Reviewer feedback\n" + fb + "\n\n")
	}
	keys := make([]string, 0, len(s.Input))
	for k := range s.Input {
		if k != "request" && k != "fix_feedback" && k != "feedback" {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	for _, k := range keys {
		v, _ := json.MarshalIndent(s.Input[k], "", "  ")
		b.WriteString("## " + k + "\n```json\n" + string(v) + "\n```\n\n")
	}
	b.WriteString("## Rules (enforced by Wise, outside this session)\n")
	b.WriteString("- Work only inside /workspace. Do not commit, push or create branches: Wise commits your work after review. There is no git metadata here.\n")
	b.WriteString("- Never write secrets, tokens or credentials into files.\n")
	if len(s.ProtectedPaths) > 0 {
		b.WriteString("- Do not change these paths (changes fail the quality gate): " + strings.Join(s.ProtectedPaths, ", ") + "\n")
	}
	b.WriteString("- Only package registries are reachable on the network.\n")
	b.WriteString("- Run the repository's checks (quality_commands, if given) before finishing and fix what fails.\n\n")
	b.WriteString("## Finish\nWhen done, reply with your report as JSON matching the required output schema.\n")
	return b.String()
}

// Event is a normalized runtime event (shown in the run timeline).
type Event struct {
	Type     string         `json:"type"` // run.message | run.tool_call | run.tool_result | run.command | run.file_changed | run.usage | run.warning | run.completed | run.failed
	Text     string         `json:"text,omitempty"`
	Tool     string         `json:"tool,omitempty"`
	Input    string         `json:"input,omitempty"`
	Output   string         `json:"output,omitempty"`
	IsError  bool           `json:"isError,omitempty"`
	Command  string         `json:"command,omitempty"`
	ExitCode *int           `json:"exitCode,omitempty"`
	Path     string         `json:"path,omitempty"`
	Usage    *Usage         `json:"usage,omitempty"`
	Raw      map[string]any `json:"-"`
}

// Usage is tokens and cost.
type Usage struct {
	InputTokens  int64   `json:"inputTokens"`
	OutputTokens int64   `json:"outputTokens"`
	CostUSD      float64 `json:"costUsd"`
	Turns        int     `json:"turns,omitempty"`
}

// Outcome classes (implementation-plan §9.6).
const (
	Succeeded       = "succeeded"
	AuthError       = "auth_error"
	RateLimited     = "rate_limited"
	BudgetExceeded  = "budget_exceeded"
	Timeout         = "timeout"
	PolicyViolation = "policy_violation"
	RuntimeCrash    = "runtime_crash"
	Incomplete      = "incomplete"
)

// Retryable reports whether an outcome may be retried automatically.
func Retryable(class string) bool { return class == RateLimited || class == RuntimeCrash }

// Outcome is a finished run as Wise sees it.
type Outcome struct {
	Class   string            `json:"class"`
	Message string            `json:"message,omitempty"`
	Output  json.RawMessage   `json:"output,omitempty"` // the agent's report (unvalidated)
	Usage   Usage             `json:"usage"`
	Files   map[string]string `json:"-"` // collected files (e.g. a refreshed credential)
}

// Adapter renders specs and reads a runtime's events.
type Adapter interface {
	ID() string
	Build(spec Spec) (executor.RunnerRequest, error)
	Parse(line []byte) []Event
	Outcome(res executor.RunnerResult, events []Event) Outcome
}

// For returns the adapter of a runtime.
func For(runtime string) (Adapter, bool) {
	switch runtime {
	case "claude-code":
		return ClaudeCode{}, true
	case "codex":
		return Codex{}, true
	}
	return nil, false
}

// ParseAll normalizes a whole stream (lines that are not JSON are ignored).
func ParseAll(a Adapter, stream string) []Event {
	var out []Event
	for _, line := range strings.Split(stream, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] != '{' {
			continue
		}
		out = append(out, a.Parse([]byte(line))...)
	}
	return out
}

func clip(s string, n int) string {
	s = secretguard.Redact(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func summarize(v any, n int) string {
	if s, ok := v.(string); ok {
		return clip(s, n)
	}
	b, _ := json.Marshal(v)
	return clip(string(b), n)
}

// classifyText maps an error message to an outcome class.
func classifyText(msg string) string {
	l := strings.ToLower(msg)
	switch {
	case strings.Contains(l, "rate limit") || strings.Contains(l, "429") || strings.Contains(l, "overloaded") || strings.Contains(l, "usage limit"):
		return RateLimited
	case strings.Contains(l, "authentication") || strings.Contains(l, "invalid x-api-key") || strings.Contains(l, "401") ||
		strings.Contains(l, "unauthorized") || strings.Contains(l, "oauth") || strings.Contains(l, "login") || strings.Contains(l, "refresh_token"):
		return AuthError
	case strings.Contains(l, "budget"):
		return BudgetExceeded
	}
	return RuntimeCrash
}

// exitOutcome classifies a run that produced no final event.
func exitOutcome(res executor.RunnerResult, last string) Outcome {
	switch {
	case res.IdleOut:
		return Outcome{Class: Timeout, Message: "the CLI produced no output for too long"}
	case res.TimedOut:
		return Outcome{Class: Timeout, Message: "the run hit its wall-clock limit"}
	case last != "":
		return Outcome{Class: classifyText(last), Message: clip(last, 500)}
	}
	return Outcome{Class: RuntimeCrash, Message: fmt.Sprintf("the CLI exited with %d without a result", res.ExitCode)}
}
