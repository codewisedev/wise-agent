package httpapi_test

import (
	"context"
	"math"
	"testing"

	"github.com/google/uuid"
)

func TestCostsSplitBillingRuntimeAndEvals(t *testing.T) {
	e := setup(t)
	e.login()
	ctx := context.Background()
	pid := e.expect(e.do("POST", "/projects", map[string]any{"name": "Costly"}), 201)["id"].(string)
	work := e.createRunInProject("product-agent", pid)
	eval := e.createRunInProject("product-agent", pid)
	if _, err := e.db.Pool.Exec(ctx, "UPDATE agent_runs SET purpose = 'eval' WHERE id = $1", eval); err != nil {
		t.Fatal(err)
	}
	call := func(run, agent, alias, provider string, cost float64) {
		if _, err := e.db.Pool.Exec(ctx, `INSERT INTO model_calls (id, run_id, agent_name, alias, provider, model, status, cost_usd, input_tokens, output_tokens)
			VALUES ($1, $2, $3, $4, $5, 'm', 'ok', $6, 100, 50)`, uuid.New(), run, agent, alias, provider, cost); err != nil {
			t.Fatal(err)
		}
	}
	call(work, "product-agent", "reasoning", "deepseek", 0.5)
	call(work, "product-agent", "reasoning", "claude-code", 1.0)
	call(work, "product-agent", "local", "lmstudio", 0)
	call(work, "backend-agent", "coding:codex:subscription", "codex", 0.3)
	call(work, "backend-agent", "coding:claude-code", "claude-code", 0.2) // Runtime Proxy: pay per use
	call(eval, "product-agent", "reasoning", "deepseek", 9)

	r := e.expect(e.do("GET", "/costs?window=7d&groupBy=runtime", nil), 200)
	near := func(a any, b float64) bool { return math.Abs(a.(float64)-b) < 1e-9 }
	bill := r["billing"].(map[string]any)
	if !near(r["total"], 2.0) || !near(r["evalUsd"], 9) || !near(bill["subscription"], 1.3) || !near(bill["api"], 0.7) || !near(bill["local"], 0) {
		t.Fatalf("totals: %v %v %v", r["total"], r["evalUsd"], bill)
	}
	keys := r["keys"].([]any)
	if len(keys) != 3 || keys[0] != "native" {
		t.Fatalf("runtime keys: %v", keys)
	}
	days := r["days"].([]any)
	last := days[len(days)-1].(map[string]any)
	if len(days) != 7 || !near(last["total"], 2.0) || !near(last["by"].(map[string]any)["codex"], 0.3) {
		t.Fatalf("days: %v", last)
	}
	budgets := r["budgets"].([]any)
	found := false
	for _, b := range budgets {
		m := b.(map[string]any)
		if m["agent"] == "product-agent" {
			found = near(m["spent"], 1.5) // the evaluation replay is not counted against the budget
		}
	}
	if !found {
		t.Fatalf("budgets: %v", budgets)
	}
}
