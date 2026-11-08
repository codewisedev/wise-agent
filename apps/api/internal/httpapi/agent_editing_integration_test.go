package httpapi_test

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"wise/apps/api/internal/agents"
)

func TestAgentsEditCreateResetAndVersions(t *testing.T) {
	e := setup(t)
	e.login()

	if tools := e.expect(e.do("GET", "/tools", nil), 200)["items"].([]any); len(tools) != 4 {
		t.Fatalf("tools: %v", tools)
	}
	detail := e.expect(e.do("GET", "/agents/product-agent", nil), 200)
	if detail["source"] != "shipped" || detail["shippedPrompt"] != detail["prompt"] {
		t.Fatalf("shipped agent: source=%v", detail["source"])
	}
	def := detail["definition"].(map[string]any)

	// Edit the prompt: new runs (and the runtime's claim) get it.
	edited := e.expect(e.do("PUT", "/agents/product-agent", map[string]any{"definition": def, "prompt": "You are a terse analyst."}), 200)
	if edited["source"] != "edited" || edited["prompt"] != "You are a terse analyst." {
		t.Fatalf("edited: %v %v", edited["source"], edited["prompt"])
	}
	e.createRun("product-agent", map[string]any{"request": "Build a thing"})
	claim := e.expect(e.runtimeDo(runtimeToken, "POST", "/runtime/v1/runs/claim", map[string]string{"runtimeId": "rt-edit"}), 200)
	if claim["prompt"] != "You are a terse analyst." {
		t.Fatalf("the runtime must receive the edited prompt, got %q", claim["prompt"])
	}

	// A restart (shipped definitions synced again) keeps the panel edit.
	catalog, err := agents.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := agents.Sync(context.Background(), e.db, catalog, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	if got := e.expect(e.do("GET", "/agents/product-agent", nil), 200)["prompt"]; got != "You are a terse analyst." {
		t.Fatal("startup sync must not overwrite a panel edit")
	}

	// Validation: unknown tool, rename, missing prompt for an enabled agent.
	bad := clone(def)
	bad["tools"] = []any{"rm_rf"}
	e.expect(e.do("PUT", "/agents/product-agent", map[string]any{"definition": bad, "prompt": "x"}), 400)
	renamed := clone(def)
	renamed["name"] = "other-agent"
	e.expect(e.do("PUT", "/agents/product-agent", map[string]any{"definition": renamed, "prompt": "x"}), 400)
	e.expect(e.do("PUT", "/agents/product-agent", map[string]any{"definition": def, "prompt": "  "}), 400)

	// Versions and restore; reset to the shipped default.
	versions := e.expect(e.do("GET", "/agents/product-agent/versions", nil), 200)["items"].([]any)
	if len(versions) < 2 || versions[0].(map[string]any)["current"] != true {
		t.Fatalf("versions: %v", versions)
	}
	reset := e.expect(e.do("POST", "/agents/product-agent/reset", nil), 200)
	if reset["source"] != "shipped" || reset["prompt"] == "You are a terse analyst." {
		t.Fatalf("reset: %v", reset["source"])
	}
	editedID := versions[0].(map[string]any)["id"].(string)
	if r := e.expect(e.do("POST", "/agents/product-agent/versions/"+editedID+"/restore", nil), 200); r["prompt"] != "You are a terse analyst." {
		t.Fatal("restore brings back the edited version")
	}

	// Create a new agent and use it in a workflow.
	custom := clone(def)
	custom["name"] = "risk-agent"
	custom["description"] = "Lists delivery risks."
	custom["produces"] = "risk_list"
	delete(custom, "prompt")
	created := e.expect(e.do("POST", "/agents", map[string]any{"definition": custom, "prompt": "List the risks."}), 201)
	if created["source"] != "custom" {
		t.Fatalf("created: %v", created["source"])
	}
	e.expect(e.do("POST", "/agents", map[string]any{"definition": custom, "prompt": "again"}), 409)
	dup := e.expect(e.do("POST", "/agents/risk-agent/duplicate", map[string]any{"name": "risk-v2-agent"}), 201)
	if dup["prompt"] != "List the risks." {
		t.Fatal("duplicate copies the prompt")
	}
	tmpl := e.expect(e.do("POST", "/workflow-templates", map[string]any{"name": "Risks", "definition": map[string]any{
		"steps": []any{map[string]any{"id": "risks", "agent": "risk-agent"}}}}), 201)
	e.expect(e.do("DELETE", "/agents/risk-agent", nil), 409) // used by a template
	e.expect(e.do("DELETE", "/workflow-templates/"+tmpl["id"].(string), nil), 204)
	e.expect(e.do("DELETE", "/agents/risk-agent", nil), 204)
	e.expect(e.do("GET", "/agents/risk-agent", nil), 404)
	e.expect(e.do("DELETE", "/agents/product-agent", nil), 409)     // shipped: disable instead
	e.expect(e.do("POST", "/agents/risk-v2-agent/reset", nil), 409) // no shipped default

	list := e.expect(e.do("GET", "/agents", nil), 200)["items"].([]any)
	var names []string
	for _, a := range list {
		names = append(names, a.(map[string]any)["name"].(string))
	}
	if !strings.Contains(strings.Join(names, ","), "risk-v2-agent") || contains(names, "risk-agent") {
		t.Fatalf("list: %v", names)
	}

	// API tokens cannot change agents.
	secret := e.expect(e.do("POST", "/auth/tokens", map[string]string{"name": "auto"}), 201)["secret"].(string)
	e.expect(e.do("POST", "/auth/logout", nil), 204)
	e.csrf, e.bearer = "", secret
	e.expect(e.do("PUT", "/agents/product-agent", map[string]any{"definition": def, "prompt": "x y z"}), 403)
}

func clone(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
