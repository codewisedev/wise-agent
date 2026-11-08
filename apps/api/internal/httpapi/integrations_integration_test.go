package httpapi_test

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"

	"wise/apps/api/internal/integrations"
)

type fakeSource struct {
	mu    sync.Mutex
	items []integrations.ExternalTask
}

func (f *fakeSource) Fetch(context.Context) ([]integrations.ExternalTask, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]integrations.ExternalTask(nil), f.items...), nil
}

func TestTaskSourceImportIsIdempotentAndFollowsTheSource(t *testing.T) {
	e := setup(t)
	e.login()
	src := &fakeSource{items: []integrations.ExternalTask{
		{ID: "10001", Key: "ABC-1", Title: "Login page", Description: "Users sign in.", Status: "In Progress", Category: "indeterminate",
			URL: "https://acme.atlassian.net/browse/ABC-1", Labels: []string{"ui"}, Priority: "high"},
		{ID: "10002", Key: "ABC-2", Title: "QA", Status: "Ready for QA", Category: "indeterminate"},
	}}
	var gotCred integrations.Credentials
	e.sources.WithImporter(func(kind string, cfg map[string]any, cred integrations.Credentials) (integrations.Importer, error) {
		gotCred = cred
		return src, nil
	})

	pid := e.expect(e.do("POST", "/projects", map[string]string{"name": "Sources " + uuid.NewString()[:6]}), 201)["id"].(string)
	jira := e.expect(e.do("POST", "/accounts", map[string]any{
		"name": "acme-jira", "kind": "jira", "baseUrl": "https://acme.atlassian.net", "metadata": map[string]any{"email": "me@acme.io"},
		"secret": "atlassian-token-123456",
	}), 201)["id"].(string)
	trello := e.expect(e.do("POST", "/accounts", map[string]any{
		"name": "my-trello", "kind": "trello", "metadata": map[string]any{"apiKey": "key12345678"}, "secret": "trello-token-123456",
	}), 201)["id"].(string)

	// Validation: account kind must match; config needs a JQL; columns must exist.
	e.expect(e.do("POST", "/projects/"+pid+"/integrations", map[string]any{"name": "x", "kind": "jira", "accountId": trello, "config": map[string]any{"jql": "a"}}), 400)
	e.expect(e.do("POST", "/projects/"+pid+"/integrations", map[string]any{"name": "x", "kind": "jira", "accountId": jira}), 400)
	e.expect(e.do("POST", "/projects/"+pid+"/integrations", map[string]any{"name": "x", "kind": "jira", "accountId": jira,
		"config": map[string]any{"jql": "a"}, "statusMap": map[string]any{"ready for qa": uuid.NewString()}}), 400)

	quick := e.expect(e.do("GET", "/workflow-templates", nil), 200)["items"].([]any)
	var quickID string
	for _, tm := range quick {
		if tm.(map[string]any)["slug"] == "quick-analysis" {
			quickID = tm.(map[string]any)["id"].(string)
		}
	}
	it := e.expect(e.do("POST", "/projects/"+pid+"/integrations", map[string]any{
		"name": "ABC board", "kind": "jira", "accountId": jira, "config": map[string]any{"jql": "project = ABC"},
		"statusMap": map[string]any{"Ready for QA": columnIDs(t, e, pid)["review"]}, "defaultTemplateId": quickID, "autoStart": false,
	}), 201)
	iid := it["id"].(string)

	res := e.expect(e.do("POST", "/integrations/"+iid+"/sync", nil), 200)
	if res["created"].(float64) != 2 || res["total"].(float64) != 2 {
		t.Fatalf("first sync: %v", res)
	}
	if gotCred.Secret != "atlassian-token-123456" || gotCred.Meta["email"] != "me@acme.io" {
		t.Fatalf("importer must get the decrypted account credentials: %+v", gotCred)
	}
	cols := boardColumns(t, e, pid)
	if len(cols["in_progress"]) != 1 || cols["in_progress"][0] != "Login page" || len(cols["review"]) != 1 {
		t.Fatalf("status mapping (default + status map): %v", cols)
	}
	b := e.expect(e.do("GET", "/projects/"+pid+"/board", nil), 200)
	card := b["columns"].([]any)[2].(map[string]any)["cards"].([]any)[0].(map[string]any)["task"].(map[string]any)
	if card["source"] != "jira" || card["externalUrl"] != "https://acme.atlassian.net/browse/ABC-1" || card["priority"] != "high" || card["templateId"] != quickID {
		t.Fatalf("imported card: %v", card)
	}

	// Re-sync: nothing changes, nothing duplicates.
	if res := e.expect(e.do("POST", "/integrations/"+iid+"/sync", nil), 200); res["unchanged"].(float64) != 2 || res["created"].(float64) != 0 {
		t.Fatalf("idempotent re-sync: %v", res)
	}

	// The source moves ABC-1 to Done and renames it: the card follows.
	src.mu.Lock()
	src.items[0].Status, src.items[0].Category, src.items[0].Title = "Done", "done", "Login page v2"
	src.mu.Unlock()
	if res := e.expect(e.do("POST", "/integrations/"+iid+"/sync", nil), 200); res["updated"].(float64) != 1 {
		t.Fatalf("update sync: %v", res)
	}
	if cols := boardColumns(t, e, pid); len(cols["done"]) != 1 || cols["done"][0] != "Login page v2" {
		t.Fatalf("source status change moves the card: %v", cols)
	}

	// Auto-start runs the integration's template on newly imported tasks.
	e.expect(e.do("PUT", "/integrations/"+iid, map[string]any{
		"name": "ABC board", "kind": "jira", "accountId": jira, "config": map[string]any{"jql": "project = ABC"},
		"defaultTemplateId": quickID, "autoStart": true,
	}), 200)
	src.mu.Lock()
	src.items = append(src.items, integrations.ExternalTask{ID: "10003", Key: "ABC-3", Title: "New feature", Status: "To Do", Category: "new"})
	src.mu.Unlock()
	if res := e.expect(e.do("POST", "/integrations/"+iid+"/sync", nil), 200); res["created"].(float64) != 1 || res["started"].(float64) != 1 {
		t.Fatalf("auto start: %v", res)
	}
	if cols := boardColumns(t, e, pid); len(cols["in_progress"]) != 1 || cols["in_progress"][0] != "New feature" {
		t.Fatalf("auto-started card is in progress: %v", cols)
	}
	list := e.expect(e.do("GET", "/projects/"+pid+"/integrations", nil), 200)["items"].([]any)
	if list[0].(map[string]any)["lastResult"].(map[string]any)["started"].(float64) != 1 {
		t.Fatalf("last result recorded: %v", list)
	}

	// Deleting the source keeps its tasks; tokens cannot manage sources.
	secret := e.expect(e.do("POST", "/auth/tokens", map[string]string{"name": "auto"}), 201)["secret"].(string)
	e.expect(e.do("DELETE", "/integrations/"+iid, nil), 204)
	if cols := boardColumns(t, e, pid); len(cols["done"]) != 1 {
		t.Fatal("imported tasks survive deleting the source")
	}
	e.expect(e.do("POST", "/auth/logout", nil), 204)
	e.csrf, e.bearer = "", secret
	e.expect(e.do("POST", "/projects/"+pid+"/integrations", map[string]any{"name": "x", "kind": "jira", "accountId": jira, "config": map[string]any{"jql": "a"}}), 403)
}
