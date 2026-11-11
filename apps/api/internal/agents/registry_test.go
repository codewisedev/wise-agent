package agents

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"testing/fstest"

	agentdefs "wise/packages/agent-definitions"
	"wise/packages/prompts"
)

func TestEmbeddedCatalogIsValid(t *testing.T) {
	cat, err := LoadFS(agentdefs.FS, prompts.FS)
	if err != nil {
		t.Fatal(err)
	}
	if len(cat.Agents) != 14 {
		t.Fatalf("want 14 agents, got %d", len(cat.Agents))
	}
	enabled := map[string]bool{}
	for _, a := range cat.Agents {
		if a.Def.Enabled {
			enabled[a.Def.Name] = true
			if a.Prompt == "" {
				t.Errorf("%s: enabled without prompt", a.Def.Name)
			}
		}
	}
	want := []string{"product-agent", "requirements-agent", "planner-agent", "architect-agent", "memory-agent", "profile-agent",
		"backend-agent", "frontend-agent", "qa-agent", "reviewer-agent", "security-agent", "improvement-agent", "eval-judge-agent", "documentation-agent"}
	for _, name := range want {
		if !enabled[name] {
			t.Errorf("%s should be enabled", name)
		}
	}
	if len(enabled) != len(want) {
		t.Fatalf("unexpected enabled agents: %v", enabled)
	}
}

func TestLoadRejectsBadDefinitions(t *testing.T) {
	tools := []byte("- name: t1\n  description: d\n  side_effects: read\n  permissions_required: []\n  input_schema: {type: object}\n")
	base := `name: x-agent
version: 1.0.0
description: d
enabled: false
responsibilities: [r]
input_schema: {type: object}
output_schema: {type: object}
tools: [%s]
permissions: []
model_config: {alias: local}
execution_limits: {max_turns: 1, max_tool_calls: 0, timeout_s: 1}
error_handling: {max_attempts: 1, invalid_output_retries: 0}
evaluation: {criteria: []}
`
	cases := map[string]string{
		"unknown tool":      strings.Replace(base, "%s", "nope", 1),
		"bad name":          strings.Replace(strings.Replace(base, "x-agent", "X", 1), "%s", "", 1),
		"enabled no prompt": strings.Replace(strings.Replace(base, "enabled: false", "enabled: true", 1), "%s", "", 1),
		"missing field":     strings.Replace(strings.Replace(base, "evaluation: {criteria: []}\n", "", 1), "%s", "", 1),
	}
	for name, def := range cases {
		fsys := fstest.MapFS{"tools.yaml": {Data: tools}, "agents/x-agent.yaml": {Data: []byte(def)}}
		if _, err := LoadFS(fsys, fstest.MapFS{}); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	ok := fstest.MapFS{"tools.yaml": {Data: tools}, "agents/x-agent.yaml": {Data: []byte(strings.Replace(base, "%s", "t1", 1))}}
	if _, err := LoadFS(ok, fstest.MapFS{}); err != nil {
		t.Fatalf("valid definition rejected: %v", err)
	}
}

// jsonSchemaKeywords are the keywords agent schemas may use. Anything else is
// usually a YAML slip (an unquoted description with a comma inside a flow
// map becomes a key), which strict validators such as Claude Code's reject.
var jsonSchemaKeywords = map[string]bool{
	"type": true, "properties": true, "required": true, "additionalProperties": true, "items": true, "enum": true,
	"description": true, "minLength": true, "maxLength": true, "minimum": true, "maximum": true, "minItems": true,
	"maxItems": true, "pattern": true, "format": true, "default": true, "const": true, "oneOf": true, "anyOf": true,
	"allOf": true, "not": true, "title": true, "uniqueItems": true, "nullable": true, "$ref": true, "$defs": true,
	"exclusiveMinimum": true, "exclusiveMaximum": true, "examples": true, "patternProperties": true,
}

func unknownKeywords(node any, path string, out *[]string) {
	switch v := node.(type) {
	case map[string]any:
		for k, child := range v {
			if !jsonSchemaKeywords[k] {
				*out = append(*out, path+"."+k)
			}
			switch k {
			case "properties", "$defs", "patternProperties":
				if m, ok := child.(map[string]any); ok {
					for name, sub := range m {
						unknownKeywords(sub, path+"."+k+"."+name, out)
					}
				}
			case "enum", "required", "default", "const", "examples":
			default:
				unknownKeywords(child, path+"."+k, out)
			}
		}
	case []any:
		for i, child := range v {
			unknownKeywords(child, fmt.Sprintf("%s[%d]", path, i), out)
		}
	}
}

func TestAgentSchemasUseOnlyKnownKeywords(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range c.Agents {
		for name, raw := range map[string]json.RawMessage{"input_schema": a.Def.InputSchema, "output_schema": a.Def.OutputSchema} {
			var s any
			if err := json.Unmarshal(raw, &s); err != nil {
				t.Fatalf("%s %s: %v", a.Def.Name, name, err)
			}
			var bad []string
			unknownKeywords(s, name, &bad)
			if len(bad) > 0 {
				t.Errorf("%s: unknown schema keywords (unquoted YAML description with a comma?): %v", a.Def.Name, bad)
			}
		}
	}
}
