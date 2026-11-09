package agents

import (
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
	if len(cat.Agents) != 13 {
		t.Fatalf("want 13 agents, got %d", len(cat.Agents))
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
	want := []string{"product-agent", "requirements-agent", "planner-agent", "architect-agent", "memory-agent", "profile-agent"}
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
