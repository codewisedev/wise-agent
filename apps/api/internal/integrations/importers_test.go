package integrations

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestJiraPagesAndFlattensADF(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != "me@x.io" || p != "tok" {
			w.WriteHeader(401)
			return
		}
		if r.URL.Path != "/rest/api/3/search/jql" || !strings.Contains(r.URL.Query().Get("jql"), "project = ABC") {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("nextPageToken") == "" {
			_, _ = w.Write([]byte(`{"issues":[{"id":"10001","key":"ABC-1","fields":{"summary":"Login page","labels":["ui"],
			  "status":{"name":"In Progress","statusCategory":{"key":"indeterminate"}},"priority":{"name":"Highest"},
			  "description":{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"Users sign in."}]}]}}}],
			  "nextPageToken":"p2","isLast":false}`))
			return
		}
		_, _ = w.Write([]byte(`{"issues":[{"id":"10002","key":"ABC-2","fields":{"summary":"Done thing","labels":[],
		  "status":{"name":"Done","statusCategory":{"key":"done"}},"priority":null,"description":null}}],"isLast":true}`))
	}))
	defer srv.Close()
	imp, err := New("jira", map[string]any{"jql": "project = ABC"}, Credentials{BaseURL: srv.URL, Secret: "tok", Meta: map[string]any{"email": "me@x.io"}})
	if err != nil {
		t.Fatal(err)
	}
	items, err := imp.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Key != "ABC-1" || items[0].Description != "Users sign in." || items[0].Priority != "urgent" ||
		items[0].URL != srv.URL+"/browse/ABC-1" || items[1].Category != "done" {
		t.Fatalf("items: %+v", items)
	}
	if DefaultColumn("jira", items[0]) != "in_progress" || DefaultColumn("jira", items[1]) != "done" {
		t.Fatal("jira status categories map to columns")
	}
	bad, _ := New("jira", map[string]any{"jql": "project = ABC"}, Credentials{BaseURL: srv.URL, Secret: "wrong", Meta: map[string]any{"email": "me@x.io"}})
	if _, err := bad.Fetch(context.Background()); !errors.Is(err, ErrAuth) {
		t.Fatalf("want ErrAuth, got %v", err)
	}
}

func TestClickUpPages(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "pk_1" || r.URL.Path != "/list/L1/task" {
			w.WriteHeader(401)
			return
		}
		if r.URL.Query().Get("page") == "0" {
			_, _ = w.Write([]byte(`{"tasks":[{"id":"a1","custom_id":"DEV-7","name":"Fix bug","text_content":"crash","url":"https://app.clickup.com/t/a1",
			  "status":{"status":"in review","type":"custom"},"tags":[{"name":"bug"}],"priority":{"priority":"high"}}],"last_page":false}`))
			return
		}
		_, _ = w.Write([]byte(`{"tasks":[{"id":"a2","name":"Ship","status":{"status":"complete","type":"closed"},"tags":[]}],"last_page":true}`))
	}))
	defer srv.Close()
	old := ClickUpAPI
	ClickUpAPI = srv.URL
	defer func() { ClickUpAPI = old }()
	imp, err := New("clickup", map[string]any{"listId": "L1"}, Credentials{Secret: "pk_1"})
	if err != nil {
		t.Fatal(err)
	}
	items, err := imp.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Key != "DEV-7" || items[0].Labels[0] != "bug" || items[0].Priority != "high" || items[1].Key != "#a2" {
		t.Fatalf("items: %+v", items)
	}
	if DefaultColumn("clickup", items[0]) != "in_progress" || DefaultColumn("clickup", items[1]) != "done" {
		t.Fatal("clickup status types map to columns")
	}
}

func TestTrelloFiltersLists(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Authorization"), `oauth_token="tok"`) {
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/boards/B1/lists":
			_, _ = w.Write([]byte(`[{"id":"l1","name":"To Do"},{"id":"l2","name":"Doing"},{"id":"l3","name":"Done"}]`))
		case "/boards/B1/cards/open":
			_, _ = w.Write([]byte(`[{"id":"c1","shortLink":"x1","name":"Card one","desc":"d","idList":"l1","url":"https://trello.com/c/x1","labels":[{"name":"ui"},{"name":""}]},
			  {"id":"c2","shortLink":"x2","name":"Card two","desc":"","idList":"l2","url":"https://trello.com/c/x2","labels":[]},
			  {"id":"c3","shortLink":"x3","name":"Old","desc":"","idList":"archived","url":"","labels":[]}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	old := TrelloAPI
	TrelloAPI = srv.URL
	defer func() { TrelloAPI = old }()
	imp, err := New("trello", map[string]any{"boardId": "B1", "lists": []any{"Doing", "To Do"}}, Credentials{Secret: "tok", Meta: map[string]any{"apiKey": "key12345"}})
	if err != nil {
		t.Fatal(err)
	}
	items, err := imp.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || len(items[0].Labels) != 1 || items[1].Status != "Doing" {
		t.Fatalf("items: %+v", items)
	}
	if DefaultColumn("trello", items[1]) != "in_progress" || DefaultColumn("trello", ExternalTask{Status: "Code Review"}) != "review" {
		t.Fatal("trello list names map to columns")
	}
}

func TestNewValidatesConfig(t *testing.T) {
	cred := Credentials{BaseURL: "https://x.atlassian.net", Secret: "s", Meta: map[string]any{"email": "a@b", "apiKey": "k"}}
	for kind, cfg := range map[string]map[string]any{"jira": {}, "clickup": {}, "trello": {}, "github": {}} {
		if _, err := New(kind, cfg, cred); err == nil {
			t.Errorf("%s with empty config must fail", kind)
		}
	}
}
