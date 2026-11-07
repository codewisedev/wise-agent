package modelgateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// The gateway speaks one agent-turn protocol to every provider: the model
// returns a JSON envelope that either requests tool calls or delivers the
// final output. Providers enforce it with structured output (LM Studio's
// response_format, Claude Code's --json-schema), so tool use works the same
// across models with and without native function calling.

const (
	ActionCallTools = "call_tools"
	ActionFinal     = "final"
	ActionInvalid   = "invalid"
)

// Envelope is the parsed model turn.
type Envelope struct {
	Thought   string          `json:"thought,omitempty"`
	Action    string          `json:"action"`
	ToolCalls []ToolCall      `json:"tool_calls,omitempty"`
	Final     json.RawMessage `json:"final,omitempty"`
}

// EnvelopeSchema builds the JSON Schema for one turn.
func EnvelopeSchema(tools []ToolSpec, output json.RawMessage) (json.RawMessage, error) {
	if len(output) == 0 {
		output = json.RawMessage(`{"type":"object"}`)
	}
	actions := []string{ActionFinal}
	props := map[string]any{
		"thought": map[string]any{"type": "string", "description": "One or two sentences of reasoning for this step."},
		"final":   json.RawMessage(output),
	}
	if len(tools) > 0 {
		actions = []string{ActionCallTools, ActionFinal}
		names := make([]string, len(tools))
		for i, t := range tools {
			names[i] = t.Name
		}
		props["tool_calls"] = map[string]any{
			"type": "array",
			"items": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []string{"tool", "arguments"},
				"properties": map[string]any{
					"tool":      map[string]any{"enum": names},
					"arguments": map[string]any{"type": "object"},
				},
			},
		}
	}
	props["action"] = map[string]any{"enum": actions}
	return json.Marshal(map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"action"},
		"properties":           props,
	})
}

// ProtocolInstructions explains the envelope to the model; appended to the system prompt.
func ProtocolInstructions(tools []ToolSpec) string {
	var b strings.Builder
	b.WriteString("\n\n# Response protocol\nRespond with a single JSON object and nothing else.\n")
	if len(tools) > 0 {
		b.WriteString(`- To use tools: {"thought": "...", "action": "call_tools", "tool_calls": [{"tool": "<name>", "arguments": {...}}]}` + "\n")
		b.WriteString("  Tool results arrive in the next message. Call several independent tools in one turn when possible.\n")
	}
	b.WriteString(`- When done: {"thought": "...", "action": "final", "final": <output matching the required output schema>}` + "\n")
	if len(tools) > 0 {
		b.WriteString("\n# Tools\n")
		for _, t := range tools {
			fmt.Fprintf(&b, "## %s\n%s\nArguments schema: %s\n\n", t.Name, t.Description, compact(t.InputSchema))
		}
	}
	return b.String()
}

// ParseEnvelope extracts the envelope from structured output or free text.
func ParseEnvelope(structured json.RawMessage, text string, tools []ToolSpec) (Envelope, error) {
	raw := structured
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		raw = json.RawMessage(extractJSONObject(text))
	}
	if len(raw) == 0 {
		return Envelope{}, errors.New("response contains no JSON object")
	}
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return Envelope{}, fmt.Errorf("response is not a valid envelope: %w", err)
	}
	allowed := map[string]bool{}
	for _, t := range tools {
		allowed[t.Name] = true
	}
	switch env.Action {
	case ActionFinal:
		if len(bytes.TrimSpace(env.Final)) == 0 || string(bytes.TrimSpace(env.Final)) == "null" {
			return Envelope{}, errors.New(`action "final" without a "final" value`)
		}
	case ActionCallTools:
		if len(env.ToolCalls) == 0 {
			return Envelope{}, errors.New(`action "call_tools" without tool_calls`)
		}
		for i, c := range env.ToolCalls {
			if !allowed[c.Tool] {
				return Envelope{}, fmt.Errorf("tool_calls[%d]: unknown tool %q", i, c.Tool)
			}
			if len(bytes.TrimSpace(c.Arguments)) == 0 || string(bytes.TrimSpace(c.Arguments)) == "null" {
				env.ToolCalls[i].Arguments = json.RawMessage(`{}`)
			}
		}
	default:
		return Envelope{}, fmt.Errorf("unknown action %q", env.Action)
	}
	return env, nil
}

// extractJSONObject returns the outermost {...} in s, tolerating code fences and prose.
func extractJSONObject(s string) string {
	start := strings.IndexByte(s, '{')
	end := strings.LastIndexByte(s, '}')
	if start < 0 || end <= start {
		return ""
	}
	return s[start : end+1]
}

func compact(raw json.RawMessage) string {
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		return string(raw)
	}
	return b.String()
}
