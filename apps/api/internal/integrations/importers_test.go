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

func TestClickUpResolvesViewsAndFiltersToMe(t *testing.T) {
	var taskQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/view/6-1200310000008590-1":
			_, _ = w.Write([]byte(`{"view":{"id":"6-1200310000008590-1","parent":{"id":"901200310","type":6}}}`))
		case "/user":
			_, _ = w.Write([]byte(`{"user":{"id":4242}}`))
		case "/list/901200310/task":
			taskQuery = r.URL.RawQuery
			_, _ = w.Write([]byte(`{"tasks":[{"id":"t1","name":"Mine","status":{"status":"to do","type":"open"}}],"last_page":true}`))
		default:
			http.Error(w, `{"err":"validateListIDEx List ID invalid"}`, 400)
		}
	}))
	defer srv.Close()
	old := ClickUpAPI
	ClickUpAPI = srv.URL
	defer func() { ClickUpAPI = old }()

	imp, err := New("clickup", map[string]any{"listId": "https://app.clickup.com/9015912090/v/l/6-1200310000008590-1", "assignedToMe": true}, Credentials{Secret: "pk_x"})
	if err != nil {
		t.Fatal(err)
	}
	items, err := imp.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || !strings.Contains(taskQuery, "assignees%5B%5D=4242") {
		t.Fatalf("items %v, query %q", items, taskQuery)
	}
}

func TestSourceReferencesParse(t *testing.T) {
	for in, want := range map[string]clickUpRef{
		"901234567890": {list: "901234567890"},
		"https://app.clickup.com/9015912090/v/li/901234567890":        {list: "901234567890"},
		"6-1200310000008590-1":                                        {view: "6-1200310000008590-1"},
		"https://app.clickup.com/9015912090/v/b/6-1200310000008590-2": {view: "6-1200310000008590-2"},
	} {
		if got, err := parseClickUpList(in); err != nil || got != want {
			t.Errorf("%s: %+v %v", in, got, err)
		}
	}
	if _, err := parseClickUpList("https://trello.com/b/UFZjbuXF/cooperative"); err == nil {
		t.Error("garbage must be rejected")
	}
	if got := parseTrelloBoard("https://trello.com/b/UFZjbuXF/cooperative"); got != "UFZjbuXF" {
		t.Errorf("trello board: %s", got)
	}
	if got := assignedJQL("project = ABC ORDER BY created DESC"); got != "assignee = currentUser() AND (project = ABC) ORDER BY created DESC" {
		t.Errorf("jql: %s", got)
	}
}

func TestTrelloOnlyMyCards(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/members/me":
			_, _ = w.Write([]byte(`{"id":"me1"}`))
		case strings.HasSuffix(r.URL.Path, "/lists"):
			_, _ = w.Write([]byte(`[{"id":"l1","name":"To Do"}]`))
		case strings.HasSuffix(r.URL.Path, "/cards/open"):
			_, _ = w.Write([]byte(`[{"id":"c1","name":"Mine","idList":"l1","idMembers":["me1"]},{"id":"c2","name":"Theirs","idList":"l1","idMembers":["x"]}]`))
		}
	}))
	defer srv.Close()
	old := TrelloAPI
	TrelloAPI = srv.URL
	defer func() { TrelloAPI = old }()
	imp, err := New("trello", map[string]any{"boardId": "https://trello.com/b/UFZjbuXF/cooperative", "assignedToMe": true}, Credentials{Secret: "tok", Meta: map[string]any{"apiKey": "k"}})
	if err != nil {
		t.Fatal(err)
	}
	items, err := imp.Fetch(context.Background())
	if err != nil || len(items) != 1 || items[0].Title != "Mine" {
		t.Fatalf("%v %v", items, err)
	}
}

func TestClickUpFolderLinks(t *testing.T) {
	for in, want := range map[string]clickUpRef{
		"https://app.clickup.com/9015912090/v/o/f/901515922889":       {folder: "901515922889"},
		"https://app.clickup.com/9015912090/v/f/901515922889/9015123": {folder: "901515922889"},
		"901515922889": {folder: "901515922889"},
		"https://app.clickup.com/9015912090/v/l/6-1200310000008590-1": {view: "6-1200310000008590-1"},
	} {
		if got, err := parseClickUpFolder(in); err != nil || got != want {
			t.Errorf("%s: %+v %v", in, got, err)
		}
	}
}
