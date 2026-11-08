package httpapi_test

import (
	"testing"
)

func TestSettingsEditValidateVersionResetAndApplyLive(t *testing.T) {
	e := setup(t)
	e.login()
	srv, _ := fakeOpenAI(t)

	list := e.expect(e.do("GET", "/settings", nil), 200)["items"].([]any)
	keys := map[string]map[string]any{}
	for _, it := range list {
		m := it.(map[string]any)
		keys[m["key"].(string)] = m
	}
	for _, k := range []string{"models", "model_policy", "workflow_defaults", "board_defaults"} {
		if keys[k] == nil || keys[k]["default"] == nil {
			t.Fatalf("setting %s missing or without default: %v", k, keys[k])
		}
	}
	models := keys["models"]
	ver := models["version"].(float64)

	// Invalid values are rejected with the reason.
	bad := map[string]any{"aliases": map[string]any{"reasoning": map[string]any{"targets": []any{map[string]any{"provider": "nope", "model": "x"}}}}}
	e.expect(e.do("PUT", "/settings/models", map[string]any{"value": bad, "version": ver}), 400)
	e.expect(e.do("PUT", "/settings/model_policy", map[string]any{"value": map[string]any{"max_retries": 99}, "version": 1}), 400)
	e.expect(e.do("PUT", "/settings/board_defaults", map[string]any{"value": map[string]any{"columns": []any{
		map[string]any{"name": "A", "role": "done"}, map[string]any{"name": "B", "role": "done"}}}, "version": 1}), 400)
	e.expect(e.do("PUT", "/settings/nope", map[string]any{"value": map[string]any{}, "version": 1}), 404)

	// Route "reasoning" to a stored account: the next run uses it, no restart.
	e.expect(e.do("POST", "/accounts", map[string]any{
		"name": "deepseek-live", "kind": "openai_compatible", "baseUrl": srv.URL + "/v1", "defaultModel": "deepseek-chat", "secret": "sk-live-123456789",
	}), 201)
	next := map[string]any{"aliases": map[string]any{
		"reasoning": map[string]any{"targets": []any{map[string]any{"provider": "deepseek-live", "model": "deepseek-chat"}}},
		"local":     map[string]any{"targets": []any{map[string]any{"provider": "scripted", "model": "test-model"}}},
	}}
	saved := e.expect(e.do("PUT", "/settings/models", map[string]any{"value": next, "version": ver}), 200)
	if saved["version"].(float64) != ver+1 {
		t.Fatalf("version: %v", saved)
	}
	e.expect(e.do("PUT", "/settings/models", map[string]any{"value": next, "version": ver}), 409) // stale editor

	e.createRun("product-agent", map[string]any{"request": "Build a thing"})
	runID, token := e.claim()
	turn := e.expect(e.runtimeDo(token, "POST", "/runtime/v1/runs/"+runID+"/model-turn",
		map[string]any{"messages": []map[string]string{{"role": "user", "content": "go"}}}), 200)
	if turn["provider"] != "deepseek-live" || turn["alias"] != "reasoning" {
		t.Fatalf("the edited alias must apply live: %v", turn)
	}

	hist := e.expect(e.do("GET", "/settings/models/history", nil), 200)["items"].([]any)
	if len(hist) != 1 || hist[0].(map[string]any)["version"].(float64) != ver {
		t.Fatalf("history: %v", hist)
	}

	// Workflow defaults apply to new plans immediately.
	wd := keys["workflow_defaults"]
	e.expect(e.do("PUT", "/settings/workflow_defaults", map[string]any{"value": map[string]any{"max_revisions": 2}, "version": wd["version"]}), 200)
	prev := e.expect(e.do("POST", "/workflow-templates/preview", map[string]any{"steps": []any{map[string]any{"id": "a", "agent": "product-agent"}}}), 200)
	if prev["plan"].(map[string]any)["maxRevisions"].(float64) != 2 {
		t.Fatalf("workflow defaults not applied: %v", prev)
	}

	// Reset restores the shipped default (and routing with it).
	reset := e.expect(e.do("POST", "/settings/models/reset", nil), 200)
	if reset["version"].(float64) != ver+2 {
		t.Fatalf("reset: %v", reset)
	}
	if len(e.expect(e.do("GET", "/settings/models/history", nil), 200)["items"].([]any)) != 2 {
		t.Fatal("reset keeps the previous value in the history")
	}

	// API tokens cannot change settings.
	secret := e.expect(e.do("POST", "/auth/tokens", map[string]string{"name": "auto"}), 201)["secret"].(string)
	e.expect(e.do("POST", "/auth/logout", nil), 204)
	e.csrf, e.bearer = "", secret
	e.expect(e.do("GET", "/settings", nil), 200)
	e.expect(e.do("POST", "/settings/models/reset", nil), 403)
}
