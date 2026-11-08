package httpapi_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"wise/apps/api/internal/tasks"
)

// boardColumns maps each column's role (or name, without one) to its card titles.
func boardColumns(t *testing.T, e *env, pid string) map[string][]string {
	t.Helper()
	b := e.expect(e.do("GET", "/projects/"+pid+"/board", nil), 200)
	out := map[string][]string{}
	for _, c := range b["columns"].([]any) {
		col := c.(map[string]any)
		ids := []string{}
		for _, card := range col["cards"].([]any) {
			ids = append(ids, card.(map[string]any)["task"].(map[string]any)["title"].(string))
		}
		key, _ := col["role"].(string)
		if key == "" {
			key = col["name"].(string)
		}
		out[key] = ids
	}
	return out
}

// columnIDs maps each column's role (or name) to its id.
func columnIDs(t *testing.T, e *env, pid string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, c := range e.expect(e.do("GET", "/projects/"+pid+"/columns", nil), 200)["items"].([]any) {
		col := c.(map[string]any)
		key, _ := col["role"].(string)
		if key == "" {
			key = col["name"].(string)
		}
		out[key] = col["id"].(string)
	}
	return out
}

func TestBoardCardsMoveByHandAndWithWorkflows(t *testing.T) {
	e := setup(t)
	e.login()
	pid := e.expect(e.do("POST", "/projects", map[string]string{"name": "Board " + uuid.NewString()[:6]}), 201)["id"].(string)
	mk := func(title string, body map[string]any) string {
		body["title"], body["start"] = title, false
		return e.expect(e.do("POST", "/projects/"+pid+"/tasks", body), 201)["task"].(map[string]any)["id"].(string)
	}
	ids := columnIDs(t, e, pid)
	a := mk("Alpha", map[string]any{})
	b := mk("Bravo", map[string]any{"column": ids["todo"], "priority": "high", "labels": []string{"ui", "ui", " api "}})
	c := mk("Charlie", map[string]any{"column": ids["todo"]})

	cols := boardColumns(t, e, pid)
	if len(cols) != 5 || cols["backlog"][0] != "Alpha" || len(cols["todo"]) != 2 || cols["todo"][0] != "Bravo" {
		t.Fatalf("initial board: %v", cols)
	}
	bt := e.expect(e.do("GET", "/tasks/"+b, nil), 200)["task"].(map[string]any)
	if bt["priority"] != "high" || len(bt["labels"].([]any)) != 2 {
		t.Fatalf("priority/labels (deduplicated, trimmed): %v", bt)
	}

	// Reorder within a column by position, and move across columns (appended).
	e.expect(e.do("PATCH", "/tasks/"+c, map[string]any{"position": -1}), 200)
	e.expect(e.do("PATCH", "/tasks/"+a, map[string]any{"column": ids["todo"]}), 200)
	if got := boardColumns(t, e, pid)["todo"]; len(got) != 3 || got[0] != "Charlie" || got[2] != "Alpha" {
		t.Fatalf("reorder/move: %v", got)
	}
	e.expect(e.do("PATCH", "/tasks/"+a, map[string]any{"column": uuid.NewString()}), 400)
	e.expect(e.do("PATCH", "/tasks/"+a, map[string]any{"title": "x"}), 400)
	e.expect(e.do("PATCH", "/tasks/"+a, map[string]any{"title": "Alpha renamed", "description": "d"}), 200)

	// Running moves the card to In progress; review → Review; completion → the template's destination.
	run := e.expect(e.do("POST", "/tasks/"+a+"/run", nil), 202)
	runID := uuid.MustParse(run["id"].(string))
	if got := boardColumns(t, e, pid)["in_progress"]; len(got) != 1 || got[0] != "Alpha renamed" {
		t.Fatalf("started: %v", got)
	}
	ctx := context.Background()
	if err := e.tasks.SetState(ctx, runID, tasks.RunAwaitingApproval, "requirements", nil); err != nil {
		t.Fatal(err)
	}
	b2 := e.expect(e.do("GET", "/projects/"+pid+"/board", nil), 200)
	review := b2["columns"].([]any)[3].(map[string]any)["cards"].([]any)
	if len(review) != 1 || review[0].(map[string]any)["workflowStatus"] != "awaiting_approval" {
		t.Fatalf("review column: %v", review)
	}
	msg := "boom"
	if err := e.tasks.SetState(ctx, runID, tasks.RunAwaitingRetry, "planning", &msg); err != nil {
		t.Fatal(err)
	}
	if got := boardColumns(t, e, pid)["review"]; len(got) != 1 {
		t.Fatalf("a failing step leaves the card where it is: %v", got)
	}
	if err := e.tasks.SetState(ctx, runID, tasks.RunCompleted, "completed", nil); err != nil {
		t.Fatal(err)
	}
	if got := boardColumns(t, e, pid)["done"]; len(got) != 1 || got[0] != "Alpha renamed" {
		t.Fatalf("completed → done: %v", got)
	}
}
