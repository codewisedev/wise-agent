package modelgateway

import (
	"encoding/json"
	"strings"
	"testing"
)

var testTools = []ToolSpec{{Name: "read_file", Description: "read", InputSchema: json.RawMessage(`{"type":"object"}`)}}

func TestParseEnvelope(t *testing.T) {
	cases := []struct {
		name, text string
		structured string
		action     string
		wantErr    string
	}{
		{name: "final in text", text: "```json\n{\"action\":\"final\",\"final\":{\"a\":1}}\n```", action: ActionFinal},
		{name: "structured wins", structured: `{"action":"call_tools","tool_calls":[{"tool":"read_file","arguments":{"path":"x"}}]}`, text: "ignored", action: ActionCallTools},
		{name: "prose around json", text: `Sure! {"thought":"t","action":"final","final":{"ok":true}} hope that helps`, action: ActionFinal},
		{name: "unknown tool", text: `{"action":"call_tools","tool_calls":[{"tool":"rm_rf","arguments":{}}]}`, wantErr: "unknown tool"},
		{name: "final missing", text: `{"action":"final"}`, wantErr: "without"},
		{name: "no json", text: "I cannot do that", wantErr: "no JSON"},
		{name: "bad action", text: `{"action":"dance"}`, wantErr: "unknown action"},
	}
	for _, c := range cases {
		env, err := ParseEnvelope(json.RawMessage(c.structured), c.text, testTools)
		if c.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("%s: err = %v, want %q", c.name, err, c.wantErr)
			}
			continue
		}
		if err != nil || env.Action != c.action {
			t.Errorf("%s: got %+v, %v", c.name, env, err)
		}
	}
}

func TestEnvelopeSchemaWithoutTools(t *testing.T) {
	raw, err := EnvelopeSchema(nil, json.RawMessage(`{"type":"object","required":["x"]}`))
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if strings.Contains(s, "tool_calls") || !strings.Contains(s, `"enum":["final"]`) || !strings.Contains(s, `"required":["x"]`) {
		t.Fatalf("unexpected schema: %s", s)
	}
	if !strings.Contains(ProtocolInstructions(testTools), "read_file") {
		t.Fatal("protocol instructions should list tools")
	}
}
