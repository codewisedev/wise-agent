package coding

import (
	"encoding/json"
	"fmt"
	"strings"

	"wise/apps/api/internal/executor"
)

// Codex drives `codex exec --json`. Instructions go in the run's global
// AGENTS.md (CODEX_HOME), the report through --output-schema and
// --output-last-message. The container is the security boundary; Codex's own
// sandbox is optional (policy).
type Codex struct{}

func (Codex) ID() string { return "codex" }

const (
	codexHome       = ".codex/"
	codexAgents     = codexHome + "AGENTS.md"
	codexAuth       = codexHome + "auth.json"
	codexSchema     = homeWise + "output-schema.json"
	codexLastOutput = homeWise + "last-message.json"
)

// codexHosts are reached by subscription (ChatGPT login) runs.
var codexHosts = []string{"chatgpt.com", "auth.openai.com", "api.openai.com"}

func (Codex) Build(s Spec) (executor.RunnerRequest, error) {
	if err := s.Validate(); err != nil {
		return executor.RunnerRequest{}, err
	}
	args := []string{"{cli}", "exec",
		"--json", "--color", "never",
		"--skip-git-repo-check", "--ephemeral",
		"--output-schema", "/home/wise/" + codexSchema,
		"-o", "/home/wise/" + codexLastOutput,
		"-c", "check_for_update_on_startup=false",
		"-c", "analytics.enabled=false",
		"-c", `cli_auth_credentials_store="file"`,
	}
	switch s.Sandbox {
	case "", "danger-full-access":
		// Wise's container is the boundary (Codex's Linux sandbox needs kernel
		// features a capability-less container does not grant).
		args = append(args, "--dangerously-bypass-approvals-and-sandbox")
	case "workspace-write":
		args = append(args, "--sandbox", "workspace-write")
	default:
		return executor.RunnerRequest{}, fmt.Errorf("unknown codex sandbox mode %q", s.Sandbox)
	}
	if s.Model != "" {
		args = append(args, "-m", s.Model)
	}
	env := map[string]string{"CODEX_HOME": "/home/wise/.codex", "NO_COLOR": "1"}
	files := map[string]string{codexAgents: s.Instructions, promptFile: s.Prompt(), codexSchema: string(s.OutputSchema)}
	req := executor.RunnerRequest{CLI: "codex", Version: s.CLIVersion, Env: env, Files: files, StdinFile: promptFile,
		Collect: []string{codexLastOutput}, WallClockS: s.Limits.WallClockS, IdleS: s.Limits.IdleS}
	switch s.Credential.Mode {
	case ModeProxy:
		args = append(args, "-c", "model_provider=wise",
			"-c", fmt.Sprintf(`model_providers.wise={ name = "wise", base_url = %q, env_key = "WISE_RUN_TOKEN", wire_api = "responses" }`, modelProxyBaseURL+"/openai/v1"))
		env["WISE_RUN_TOKEN"] = s.Credential.Secret
		req.ModelProxy = true
	case ModeSubscription:
		files[codexAuth] = s.Credential.Secret
		req.GrantHosts = codexHosts
		req.Collect = append(req.Collect, codexAuth) // a refreshed login is kept
	}
	req.Command = append(args, "-") // the prompt on stdin
	return req, nil
}

type codexEvent struct {
	Type    string `json:"type"`
	Message string `json:"message"`
	Error   *struct {
		Message string `json:"message"`
	} `json:"error"`
	Item *struct {
		Type             string `json:"type"`
		Text             string `json:"text"`
		Command          string `json:"command"`
		AggregatedOutput string `json:"aggregated_output"`
		ExitCode         *int   `json:"exit_code"`
		Status           string `json:"status"`
		Changes          []struct {
			Path string `json:"path"`
			Kind string `json:"kind"`
		} `json:"changes"`
		Server string `json:"server"`
		Tool   string `json:"tool"`
	} `json:"item"`
	Usage *struct {
		InputTokens       int64 `json:"input_tokens"`
		CachedInputTokens int64 `json:"cached_input_tokens"`
		OutputTokens      int64 `json:"output_tokens"`
	} `json:"usage"`
}

func (Codex) Parse(line []byte) []Event {
	var ev codexEvent
	if json.Unmarshal(line, &ev) != nil {
		return nil
	}
	switch ev.Type {
	case "item.started":
		if ev.Item != nil && ev.Item.Type == "command_execution" {
			return []Event{{Type: "run.tool_call", Tool: "shell", Command: clip(ev.Item.Command, 1000), Input: clip(ev.Item.Command, 1000)}}
		}
	case "item.completed":
		if ev.Item == nil {
			return nil
		}
		switch ev.Item.Type {
		case "agent_message":
			return []Event{{Type: "run.message", Text: clip(ev.Item.Text, 4000)}}
		case "command_execution":
			return []Event{{Type: "run.command", Command: clip(ev.Item.Command, 1000), ExitCode: ev.Item.ExitCode,
				Output: clip(ev.Item.AggregatedOutput, 2000), IsError: ev.Item.ExitCode != nil && *ev.Item.ExitCode != 0}}
		case "file_change":
			var out []Event
			for _, c := range ev.Item.Changes {
				out = append(out, Event{Type: "run.file_changed", Path: c.Path, Text: c.Kind})
			}
			return out
		case "mcp_tool_call", "web_search":
			return []Event{{Type: "run.warning", Text: "unexpected " + ev.Item.Type + " (MCP and web access are off)"}}
		}
	case "turn.completed":
		if ev.Usage != nil {
			return []Event{{Type: "run.usage", Usage: &Usage{InputTokens: ev.Usage.InputTokens, OutputTokens: ev.Usage.OutputTokens, Turns: 1}}}
		}
	case "turn.failed":
		msg := ""
		if ev.Error != nil {
			msg = ev.Error.Message
		}
		return []Event{{Type: "run.failed", Text: clip(msg, 1000), IsError: true}}
	case "error":
		return []Event{{Type: "run.warning", Text: clip(ev.Message, 1000), IsError: true}}
	}
	return nil
}

func (Codex) Outcome(res executor.RunnerResult, events []Event) Outcome {
	var u Usage
	var failed, lastErr, lastMsg string
	for _, e := range events {
		switch e.Type {
		case "run.usage":
			u.InputTokens += e.Usage.InputTokens
			u.OutputTokens += e.Usage.OutputTokens
			u.Turns += e.Usage.Turns
		case "run.failed":
			failed = e.Text
		case "run.warning":
			if e.IsError {
				lastErr = e.Text
			}
		case "run.message":
			lastMsg = e.Text
		}
	}
	o := Outcome{Usage: u, Files: map[string]string{}}
	if a, ok := res.Collected[codexAuth]; ok {
		o.Files[codexAuth] = a
	}
	report := strings.TrimSpace(res.Collected[codexLastOutput])
	if report == "" {
		report = strings.TrimSpace(lastMsg)
	}
	switch {
	case failed != "":
		o.Class, o.Message = classifyText(failed), clip(failed, 500)
	case report != "" && json.Valid([]byte(report)):
		o.Class, o.Output = Succeeded, json.RawMessage(report)
	case res.IdleOut || res.TimedOut:
		x := exitOutcome(res, "")
		o.Class, o.Message = x.Class, x.Message
	case report != "":
		o.Class, o.Message = Incomplete, "the final message is not a JSON report"
	case lastErr != "":
		o.Class, o.Message = classifyText(lastErr), clip(lastErr, 500)
	default:
		x := exitOutcome(res, "")
		o.Class, o.Message = x.Class, x.Message
	}
	return o
}
