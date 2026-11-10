package coding

import (
	"encoding/json"
	"strconv"
	"strings"

	"wise/apps/api/internal/executor"
)

// ClaudeCode drives `claude -p` (stream-json). Verified on 2.1.319: the
// final `result` event carries the structured output, usage and cost; Glob
// and Grep run through Bash, so the image needs bash and ripgrep.
type ClaudeCode struct{}

func (ClaudeCode) ID() string { return "claude-code" }

// defaultClaudeTools are the built-in tools a coding run gets (everything
// else — scheduling, remote triggers, worktrees, messaging — is off).
var defaultClaudeTools = []string{"Bash", "Read", "Edit", "Write", "NotebookEdit"}

const (
	homeWise          = ".wise/"
	instructionsFile  = homeWise + "instructions.md"
	promptFile        = homeWise + "prompt.md"
	claudeSettings    = ".claude/settings.json"
	modelProxyBaseURL = "http://wise-model-proxy:8095"
)

func (ClaudeCode) Build(s Spec) (executor.RunnerRequest, error) {
	if err := s.Validate(); err != nil {
		return executor.RunnerRequest{}, err
	}
	tools := s.Tools
	if len(tools) == 0 {
		tools = defaultClaudeTools
	}
	settings := map[string]any{
		"permissions": map[string]any{
			"allow": tools,
			// Git is hidden from the runner; these keep a confused agent from trying.
			"deny": []string{"Bash(git commit:*)", "Bash(git push:*)", "Bash(git remote:*)", "Bash(git config:*)", "WebFetch", "WebSearch"},
		},
		"disableAllHooks":            true,
		"includeCoAuthoredBy":        false,
		"cleanupPeriodDays":          1,
		"enableAllProjectMcpServers": false,
	}
	settingsJSON, _ := json.Marshal(settings)
	args := []string{"{cli}", "-p",
		"--output-format", "stream-json", "--verbose",
		"--no-session-persistence",
		"--setting-sources", "user", // only the managed settings in the fresh home, never the repository's
		"--strict-mcp-config",
		"--tools", strings.Join(tools, ","),
		"--permission-mode", "dontAsk",
		"--append-system-prompt-file", "/home/wise/" + instructionsFile,
		"--max-turns", strconv.Itoa(s.Limits.MaxTurns),
		"--json-schema", string(s.OutputSchema),
	}
	if s.Model != "" {
		args = append(args, "--model", s.Model)
	}
	if s.Limits.MaxCostUSD > 0 {
		args = append(args, "--max-budget-usd", strconv.FormatFloat(s.Limits.MaxCostUSD, 'f', 4, 64))
	}
	env := map[string]string{
		"SHELL": "/bin/bash", "USE_BUILTIN_RIPGREP": "0",
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1", "DISABLE_TELEMETRY": "1", "DISABLE_AUTOUPDATER": "1",
		"DISABLE_ERROR_REPORTING": "1", "DISABLE_BUG_COMMAND": "1",
	}
	req := executor.RunnerRequest{CLI: "claude-code", Version: s.CLIVersion, Command: args, Env: env,
		Files:     map[string]string{instructionsFile: s.Instructions, promptFile: s.Prompt(), claudeSettings: string(settingsJSON)},
		StdinFile: promptFile, WallClockS: s.Limits.WallClockS, IdleS: s.Limits.IdleS}
	switch s.Credential.Mode {
	case ModeProxy:
		env["ANTHROPIC_BASE_URL"] = modelProxyBaseURL + "/anthropic"
		env["ANTHROPIC_API_KEY"] = s.Credential.Secret
		req.ModelProxy = true
	case ModeSubscription:
		env["CLAUDE_CODE_OAUTH_TOKEN"] = s.Credential.Secret
		req.GrantHosts = []string{"api.anthropic.com"}
	}
	return req, nil
}

type claudeEvent struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	Message struct {
		Content []struct {
			Type      string          `json:"type"`
			Text      string          `json:"text"`
			Name      string          `json:"name"`
			Input     json.RawMessage `json:"input"`
			Content   json.RawMessage `json:"content"`
			IsError   bool            `json:"is_error"`
			ToolUseID string          `json:"tool_use_id"`
		} `json:"content"`
	} `json:"message"`
	IsError          bool            `json:"is_error"`
	NumTurns         int             `json:"num_turns"`
	TotalCostUSD     float64         `json:"total_cost_usd"`
	Result           string          `json:"result"`
	StructuredOutput json.RawMessage `json:"structured_output"`
	Usage            struct {
		InputTokens              int64 `json:"input_tokens"`
		OutputTokens             int64 `json:"output_tokens"`
		CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	} `json:"usage"`
	Errors []string `json:"errors"`
}

func (ClaudeCode) Parse(line []byte) []Event {
	var ev claudeEvent
	if json.Unmarshal(line, &ev) != nil {
		return nil
	}
	var out []Event
	switch ev.Type {
	case "assistant":
		for _, c := range ev.Message.Content {
			switch c.Type {
			case "text":
				if strings.TrimSpace(c.Text) != "" {
					out = append(out, Event{Type: "run.message", Text: clip(c.Text, 4000)})
				}
			case "tool_use":
				e := Event{Type: "run.tool_call", Tool: c.Name, Input: summarize(json.RawMessage(c.Input), 1000)}
				var in struct {
					Command  string `json:"command"`
					FilePath string `json:"file_path"`
				}
				_ = json.Unmarshal(c.Input, &in)
				if c.Name == "Bash" {
					e.Command = clip(in.Command, 1000)
				}
				out = append(out, e)
				if (c.Name == "Edit" || c.Name == "Write" || c.Name == "NotebookEdit") && in.FilePath != "" {
					out = append(out, Event{Type: "run.file_changed", Tool: c.Name, Path: in.FilePath})
				}
			}
		}
	case "user":
		for _, c := range ev.Message.Content {
			if c.Type == "tool_result" {
				var text string
				if json.Unmarshal(c.Content, &text) != nil {
					var parts []struct {
						Text string `json:"text"`
					}
					_ = json.Unmarshal(c.Content, &parts)
					var b strings.Builder
					for _, p := range parts {
						b.WriteString(p.Text)
					}
					text = b.String()
				}
				out = append(out, Event{Type: "run.tool_result", Output: clip(text, 2000), IsError: c.IsError})
			}
		}
	case "result":
		u := &Usage{InputTokens: ev.Usage.InputTokens + ev.Usage.CacheCreationInputTokens + ev.Usage.CacheReadInputTokens,
			OutputTokens: ev.Usage.OutputTokens, CostUSD: ev.TotalCostUSD, Turns: ev.NumTurns}
		out = append(out, Event{Type: "run.usage", Usage: u})
		e := Event{Type: "run.completed", Text: clip(ev.Result, 2000), Usage: u,
			Raw: map[string]any{"subtype": ev.Subtype, "structured_output": ev.StructuredOutput, "errors": ev.Errors}}
		if ev.IsError || ev.Subtype != "success" {
			e.Type, e.IsError = "run.failed", true
		}
		out = append(out, e)
	}
	return out
}

func (ClaudeCode) Outcome(res executor.RunnerResult, events []Event) Outcome {
	var final *Event
	for i := range events {
		if events[i].Type == "run.completed" || events[i].Type == "run.failed" {
			final = &events[i]
		}
	}
	if final == nil {
		last := ""
		for i := len(events) - 1; i >= 0 && last == ""; i-- {
			if events[i].IsError {
				last = events[i].Output + events[i].Text
			}
		}
		return exitOutcome(res, last)
	}
	o := Outcome{Usage: *final.Usage}
	sub, _ := final.Raw["subtype"].(string)
	so, _ := final.Raw["structured_output"].(json.RawMessage)
	switch {
	case final.Type == "run.completed" && len(so) > 0 && string(so) != "null":
		o.Class, o.Output = Succeeded, so
	case sub == "error_max_turns":
		o.Class, o.Message = Incomplete, "the run used all its turns before finishing"
	case sub == "error_max_budget_usd" || strings.Contains(sub, "budget"):
		o.Class, o.Message = BudgetExceeded, "the run reached its cost limit"
	case final.Type == "run.completed":
		o.Class, o.Message = Incomplete, "the run finished without a report matching the output schema"
	default:
		msg := final.Text
		if errs, ok := final.Raw["errors"].([]string); ok && len(errs) > 0 {
			msg = strings.Join(errs, "; ")
		}
		if msg == "" {
			msg = sub
		}
		o.Class, o.Message = classifyText(msg), clip(msg, 500)
	}
	return o
}
