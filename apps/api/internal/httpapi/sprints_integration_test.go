package httpapi_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"wise/apps/api/internal/integrations"
)

func sprintsByName(t *testing.T, e *env, pid string) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	for _, s := range e.expect(e.do("GET", "/projects/"+pid+"/sprints", nil), 200)["items"].([]any) {
		sp := s.(map[string]any)
		out[sp["name"].(string)] = sp
	}
	return out
}

func TestSprintsCarryOverAndFollowTheSource(t *testing.T) {
	e := setup(t)
	e.login()
	pid := e.expect(e.do("POST", "/projects", map[string]string{"name": "Sprints " + uuid.NewString()[:6]}), 201)["id"].(string)
	day := func(d int) string { return time.Now().AddDate(0, 0, d).Format("2006-01-02") }

	// The first sprint becomes current; dates are validated.
	s1 := e.expect(e.do("POST", "/projects/"+pid+"/sprints", map[string]any{"name": "Sprint 1", "startsOn": day(-20), "endsOn": day(-6)}), 201)
	if s1["current"] != true {
		t.Fatalf("first sprint is current: %v", s1)
	}
	e.expect(e.do("POST", "/projects/"+pid+"/sprints", map[string]any{"name": "Bad", "startsOn": day(5), "endsOn": day(1)}), 400)
	s2 := e.expect(e.do("POST", "/projects/"+pid+"/sprints", map[string]any{"name": "Sprint 2", "startsOn": day(-5), "endsOn": day(8)}), 201)
	s1id, s2id := s1["id"].(string), s2["id"].(string)

	// Tasks created in Wise: one open, one done.
	ids := columnIDs(t, e, pid)
	open := e.expect(e.do("POST", "/projects/"+pid+"/tasks", map[string]any{"title": "Open work", "start": false, "sprintId": s1id}), 201)["task"].(map[string]any)
	done := e.expect(e.do("POST", "/projects/"+pid+"/tasks", map[string]any{"title": "Done work", "start": false, "sprintId": s1id, "column": ids["done"]}), 201)["task"].(map[string]any)
	if open["sprintId"] != s1id {
		t.Fatalf("task sprint: %v", open)
	}
	other := e.expect(e.do("POST", "/projects", map[string]string{"name": "Other " + uuid.NewString()[:6]}), 201)["id"].(string)
	foreign := e.expect(e.do("POST", "/projects/"+other+"/sprints", map[string]any{"name": "X"}), 201)["id"].(string)
	e.expect(e.do("PATCH", "/tasks/"+open["id"].(string), map[string]any{"sprint": foreign}), 400)

	// Sprint 1 has ended and Sprint 2 is running: the date refresh moves on and carries the open task over.
	if err := e.tasks.RefreshSprints(context.Background(), uuid.MustParse(pid), time.Now()); err != nil {
		t.Fatal(err)
	}
	sp := sprintsByName(t, e, pid)
	if sp["Sprint 2"]["current"] != true || sp["Sprint 1"]["current"] != false {
		t.Fatalf("roll-over: %v", sp)
	}
	if got := e.expect(e.do("GET", "/tasks/"+open["id"].(string), nil), 200)["task"].(map[string]any)["sprintId"]; got != s2id {
		t.Fatalf("open task carried over: %v", got)
	}
	if got := e.expect(e.do("GET", "/tasks/"+done["id"].(string), nil), 200)["task"].(map[string]any)["sprintId"]; got != s1id {
		t.Fatalf("done task stays: %v", got)
	}

	// Take a task out of its sprint; start and complete sprints by hand.
	e.expect(e.do("PATCH", "/tasks/"+done["id"].(string), map[string]any{"sprint": "none"}), 200)
	e.expect(e.do("POST", "/sprints/"+s1id+"/start", nil), 204)
	if sprintsByName(t, e, pid)["Sprint 1"]["current"] != true {
		t.Fatal("start makes current")
	}
	e.expect(e.do("POST", "/sprints/"+s1id+"/complete", nil), 204)
	sp = sprintsByName(t, e, pid)
	if sp["Sprint 1"]["closed"] != true || sp["Sprint 2"]["current"] != true {
		t.Fatalf("complete hands over to the running sprint: %v", sp)
	}

	// A source following the tool's sprints creates them and files its tasks.
	start, end := time.Now().AddDate(0, 0, -1), time.Now().AddDate(0, 0, 12)
	s25 := &integrations.Sprint{ID: "list-25", Name: "Sprint 25", StartsOn: &start, EndsOn: &end}
	src := &fakeSource{items: []integrations.ExternalTask{{ID: "c1", Key: "#c1", Title: "From ClickUp", Status: "to do", Category: "open", Sprint: s25}}}
	e.sources.WithImporter(func(string, map[string]any, integrations.Credentials) (integrations.Importer, error) { return src, nil })
	acc := e.expect(e.do("POST", "/accounts", map[string]any{"name": "cu", "kind": "clickup", "secret": "pk_123456789"}), 201)["id"].(string)
	it := e.expect(e.do("POST", "/projects/"+pid+"/integrations", map[string]any{
		"name": "Sprint folder", "kind": "clickup", "accountId": acc, "config": map[string]any{"mode": "folder", "folderId": "123"},
	}), 201)["id"].(string)
	e.expect(e.do("POST", "/integrations/"+it+"/sync", nil), 200)
	sp = sprintsByName(t, e, pid)
	if sp["Sprint 25"] == nil || sp["Sprint 25"]["sourceName"] != "Sprint folder" || sp["Sprint 25"]["taskCount"].(float64) != 1 {
		t.Fatalf("followed sprint: %v", sp["Sprint 25"])
	}

	// The tool moves the task to the next sprint list: Wise follows.
	s26 := &integrations.Sprint{ID: "list-26", Name: "Sprint 26"}
	src.mu.Lock()
	src.items[0].Sprint = s26
	src.mu.Unlock()
	if res := e.expect(e.do("POST", "/integrations/"+it+"/sync", nil), 200); res["updated"].(float64) != 1 {
		t.Fatalf("sprint change counts as an update: %v", res)
	}
	sp = sprintsByName(t, e, pid)
	if sp["Sprint 26"]["taskCount"].(float64) != 1 || sp["Sprint 25"]["taskCount"].(float64) != 0 {
		t.Fatalf("task follows the source's sprint: 25=%v 26=%v", sp["Sprint 25"], sp["Sprint 26"])
	}

	// Deleting a sprint keeps its tasks.
	e.expect(e.do("DELETE", "/sprints/"+s2id, nil), 204)
	if got := e.expect(e.do("GET", "/tasks/"+open["id"].(string), nil), 200)["task"].(map[string]any)["sprintId"]; got != nil {
		t.Fatalf("task without sprint after delete: %v", got)
	}
}
