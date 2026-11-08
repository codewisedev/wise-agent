package httpapi_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"wise/apps/api/internal/tasks"
)

func TestBoardColumnsAreCustomAndWorkflowsFollowRoles(t *testing.T) {
	e := setup(t)
	e.login()
	pid := e.expect(e.do("POST", "/projects", map[string]string{"name": "Cols " + uuid.NewString()[:6]}), 201)["id"].(string)
	ids := columnIDs(t, e, pid)
	if len(ids) != 5 {
		t.Fatalf("new projects get the default columns: %v", ids)
	}

	// Add a QA list that takes the review role from "Review".
	qa := e.expect(e.do("POST", "/projects/"+pid+"/columns", map[string]any{"name": "QA", "role": "review", "color": "#FF0000", "wipLimit": 2}), 201)
	if qa["role"] != "review" || qa["color"] != "#ff0000" || qa["wipLimit"].(float64) != 2 {
		t.Fatalf("created: %v", qa)
	}
	if got := columnIDs(t, e, pid); got["Review"] == "" || got["review"] != qa["id"] {
		t.Fatalf("the role moves to the new column: %v", got)
	}
	e.expect(e.do("POST", "/projects/"+pid+"/columns", map[string]any{"name": "qa"}), 409) // duplicate name
	e.expect(e.do("POST", "/projects/"+pid+"/columns", map[string]any{"name": "X", "role": "boss"}), 400)
	e.expect(e.do("POST", "/projects/"+pid+"/columns", map[string]any{"name": "X", "color": "red"}), 400)

	// Rename and move QA to the front.
	e.expect(e.do("PUT", "/columns/"+qa["id"].(string), map[string]any{"name": "Quality", "role": "review", "position": 0}), 200)
	board := e.expect(e.do("GET", "/projects/"+pid+"/board", nil), 200)["columns"].([]any)
	if first := board[0].(map[string]any); first["name"] != "Quality" || len(board) != 6 {
		t.Fatalf("order: %v", first)
	}

	// A run waiting for review moves its card into the review-role column.
	task := e.expect(e.do("POST", "/projects/"+pid+"/tasks", map[string]any{"title": "Ship it", "start": false}), 201)["task"].(map[string]any)["id"].(string)
	run := e.expect(e.do("POST", "/tasks/"+task+"/run", nil), 202)
	runID := uuid.MustParse(run["id"].(string))
	ctx := context.Background()
	if err := e.tasks.SetState(ctx, runID, tasks.RunAwaitingApproval, "requirements", nil); err != nil {
		t.Fatal(err)
	}
	if got := boardColumns(t, e, pid)["review"]; len(got) != 1 || got[0] != "Ship it" {
		t.Fatalf("review role column: %v", boardColumns(t, e, pid))
	}

	// Without a done column the completed card stays where it is.
	e.expect(e.do("DELETE", "/columns/"+ids["done"], nil), 204) // empty: no moveTo needed
	if err := e.tasks.SetState(ctx, runID, tasks.RunCompleted, "completed", nil); err != nil {
		t.Fatal(err)
	}
	if got := boardColumns(t, e, pid)["review"]; len(got) != 1 {
		t.Fatalf("no done column: card stays: %v", boardColumns(t, e, pid))
	}

	// Deleting a column with cards needs a destination; cards and mappings follow it.
	qaID := qa["id"].(string)
	e.expect(e.do("DELETE", "/columns/"+qaID, nil), 409)
	e.expect(e.do("DELETE", "/columns/"+qaID+"?moveTo="+qaID, nil), 400)
	e.expect(e.do("DELETE", "/columns/"+qaID+"?moveTo="+ids["backlog"], nil), 204)
	if got := boardColumns(t, e, pid)["backlog"]; len(got) != 1 || got[0] != "Ship it" {
		t.Fatalf("cards moved: %v", boardColumns(t, e, pid))
	}

	// Another project's column is not a valid target.
	other := e.expect(e.do("POST", "/projects", map[string]string{"name": "Other " + uuid.NewString()[:6]}), 201)["id"].(string)
	foreign := columnIDs(t, e, other)["backlog"]
	e.expect(e.do("PATCH", "/tasks/"+task, map[string]any{"column": foreign}), 400)
	e.expect(e.do("DELETE", "/columns/"+ids["todo"]+"?moveTo="+foreign, nil), 400)
}
