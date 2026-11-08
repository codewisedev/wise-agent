package httpapi_test

import (
	"testing"

	"github.com/google/uuid"

	"wise/apps/api/internal/integrations"
)

func TestSyncArchivesWhatTheSourceDropsAndRestoresIt(t *testing.T) {
	e := setup(t)
	e.login()
	src := &fakeSource{items: []integrations.ExternalTask{
		{ID: "a", Key: "#a", Title: "Mine", Status: "to do", Category: "open"},
		{ID: "b", Key: "#b", Title: "Reassigned later", Status: "to do", Category: "open"},
	}}
	e.sources.WithImporter(func(string, map[string]any, integrations.Credentials) (integrations.Importer, error) { return src, nil })
	pid := e.expect(e.do("POST", "/projects", map[string]string{"name": "Archive " + uuid.NewString()[:6]}), 201)["id"].(string)
	acc := e.expect(e.do("POST", "/accounts", map[string]any{"name": "cu-arch", "kind": "clickup", "secret": "pk_123456789"}), 201)["id"].(string)
	it := e.expect(e.do("POST", "/projects/"+pid+"/integrations", map[string]any{
		"name": "Mine", "kind": "clickup", "accountId": acc, "config": map[string]any{"listId": "123", "assignedToMe": true},
	}), 201)["id"].(string)
	e.expect(e.do("POST", "/integrations/"+it+"/sync", nil), 200)
	manual := e.expect(e.do("POST", "/projects/"+pid+"/tasks", map[string]any{"title": "Wise-only task", "start": false}), 201)

	count := func() int {
		n := 0
		for _, c := range e.expect(e.do("GET", "/projects/"+pid+"/board", nil), 200)["columns"].([]any) {
			n += len(c.(map[string]any)["cards"].([]any))
		}
		return n
	}
	if count() != 3 {
		t.Fatalf("board before: %d", count())
	}

	// "b" is no longer assigned to me: the source stops returning it.
	src.mu.Lock()
	src.items = src.items[:1]
	src.mu.Unlock()
	res := e.expect(e.do("POST", "/integrations/"+it+"/sync", nil), 200)
	if res["archived"].(float64) != 1 || count() != 2 {
		t.Fatalf("archived: %v, board %d", res, count())
	}
	archived := e.expect(e.do("GET", "/tasks?projectId="+pid+"&archived=true", nil), 200)["items"].([]any)
	if len(archived) != 1 || archived[0].(map[string]any)["title"] != "Reassigned later" || archived[0].(map[string]any)["archivedAt"] == nil {
		t.Fatalf("archived list: %v", archived)
	}
	if live := e.expect(e.do("GET", "/tasks?projectId="+pid, nil), 200)["items"].([]any); len(live) != 2 {
		t.Fatalf("live list keeps the Wise task and the assigned one: %d", len(live))
	}
	_ = manual

	// Assigned to me again: restored.
	src.mu.Lock()
	src.items = append(src.items, integrations.ExternalTask{ID: "b", Key: "#b", Title: "Reassigned later", Status: "to do", Category: "open"})
	src.mu.Unlock()
	res = e.expect(e.do("POST", "/integrations/"+it+"/sync", nil), 200)
	if res["restored"].(float64) != 1 || res["archived"].(float64) != 0 || count() != 3 {
		t.Fatalf("restored: %v, board %d", res, count())
	}
}
