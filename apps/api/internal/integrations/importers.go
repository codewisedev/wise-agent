// Package integrations imports tasks from Jira, ClickUp and Trello into a
// project's board. Import only: Wise never writes to these services, so read
// scopes are enough.
package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// ExternalTask is one issue/card from a source, normalized.
type ExternalTask struct {
	ID          string // stable id used for upserts
	Key         string // human key shown on the card (PROJ-12, #abc)
	Title       string
	Description string
	Status      string // the source's status / list name
	Category    string // jira: new|indeterminate|done; clickup: open|custom|done|closed
	URL         string
	Labels      []string
	Priority    string // none|low|medium|high|urgent
}

// Credentials are the account values an importer needs.
type Credentials struct {
	BaseURL string
	Secret  string
	Meta    map[string]any
}

// Importer lists every task a source currently exposes (bounded by MaxItems).
type Importer interface {
	Fetch(ctx context.Context) ([]ExternalTask, error)
}

// MaxItems caps one sync; larger sources should narrow their filter.
const MaxItems = 1000

// maxBody limits each response we read.
const maxBody = 16 << 20

// Endpoints for the hosted services (overridden in tests).
var (
	ClickUpAPI = "https://api.clickup.com/api/v2"
	TrelloAPI  = "https://api.trello.com/1"
)

var httpClient = &http.Client{Timeout: 30 * time.Second}

// ErrAuth means the source rejected the account's credentials.
var ErrAuth = errors.New("the source rejected the credentials")

func getJSON(ctx context.Context, rawURL string, auth func(*http.Request), out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	auth(req)
	res, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("unreachable: %w", err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, maxBody))
	if err != nil {
		return err
	}
	switch {
	case res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden:
		return fmt.Errorf("%w (HTTP %d)", ErrAuth, res.StatusCode)
	case res.StatusCode == http.StatusTooManyRequests:
		return fmt.Errorf("rate limited (HTTP 429); will retry on the next sync")
	case res.StatusCode >= 300:
		return fmt.Errorf("HTTP %d: %s", res.StatusCode, truncate(strings.TrimSpace(string(body)), 300))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("unexpected response: %w", err)
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func str(m map[string]any, k string) string {
	v, _ := m[k].(string)
	return strings.TrimSpace(v)
}

// New returns the importer for an integration.
func New(kind string, cfg map[string]any, cred Credentials) (Importer, error) {
	switch kind {
	case "jira":
		if str(cred.Meta, "email") == "" || cred.Secret == "" || cred.BaseURL == "" {
			return nil, errors.New("the Jira account needs a site URL, email and API token")
		}
		jql := str(cfg, "jql")
		if jql == "" {
			return nil, errors.New("set a JQL filter (e.g. project = ABC AND statusCategory != Done)")
		}
		return &jira{base: strings.TrimSuffix(cred.BaseURL, "/"), email: str(cred.Meta, "email"), token: cred.Secret, jql: jql}, nil
	case "clickup":
		list := str(cfg, "listId")
		if list == "" || cred.Secret == "" {
			return nil, errors.New("set the ClickUp list id")
		}
		return &clickup{token: cred.Secret, list: list}, nil
	case "trello":
		board := str(cfg, "boardId")
		if board == "" || cred.Secret == "" || str(cred.Meta, "apiKey") == "" {
			return nil, errors.New("set the Trello board id (and the account's API key)")
		}
		var lists []string
		if raw, ok := cfg["lists"].([]any); ok {
			for _, l := range raw {
				if s, ok := l.(string); ok && strings.TrimSpace(s) != "" {
					lists = append(lists, strings.ToLower(strings.TrimSpace(s)))
				}
			}
		}
		return &trello{key: str(cred.Meta, "apiKey"), token: cred.Secret, board: board, lists: lists}, nil
	}
	return nil, fmt.Errorf("unknown source %q", kind)
}

// ---------------------------------------------------------------- Jira Cloud

type jira struct{ base, email, token, jql string }

func (j *jira) Fetch(ctx context.Context) ([]ExternalTask, error) {
	auth := func(r *http.Request) { r.SetBasicAuth(j.email, j.token) }
	var out []ExternalTask
	next := ""
	for len(out) < MaxItems {
		q := url.Values{
			"jql": {j.jql}, "maxResults": {"100"},
			"fields": {"summary,description,status,labels,priority"},
		}
		if next != "" {
			q.Set("nextPageToken", next)
		}
		var page struct {
			Issues []struct {
				ID     string `json:"id"`
				Key    string `json:"key"`
				Fields struct {
					Summary     string          `json:"summary"`
					Description json.RawMessage `json:"description"`
					Labels      []string        `json:"labels"`
					Status      struct {
						Name     string `json:"name"`
						Category struct {
							Key string `json:"key"`
						} `json:"statusCategory"`
					} `json:"status"`
					Priority *struct {
						Name string `json:"name"`
					} `json:"priority"`
				} `json:"fields"`
			} `json:"issues"`
			NextPageToken string `json:"nextPageToken"`
			IsLast        bool   `json:"isLast"`
		}
		if err := getJSON(ctx, j.base+"/rest/api/3/search/jql?"+q.Encode(), auth, &page); err != nil {
			return nil, fmt.Errorf("jira: %w", err)
		}
		for _, is := range page.Issues {
			t := ExternalTask{
				ID: is.ID, Key: is.Key, Title: is.Fields.Summary, Description: adfText(is.Fields.Description),
				Status: is.Fields.Status.Name, Category: is.Fields.Status.Category.Key,
				URL: j.base + "/browse/" + url.PathEscape(is.Key), Labels: is.Fields.Labels,
			}
			if is.Fields.Priority != nil {
				t.Priority = normalizePriority(is.Fields.Priority.Name)
			}
			out = append(out, t)
		}
		if page.IsLast || page.NextPageToken == "" || len(page.Issues) == 0 {
			break
		}
		next = page.NextPageToken
	}
	return out, nil
}

// adfText flattens Atlassian Document Format (or a plain string) to text.
func adfText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var node map[string]any
	if json.Unmarshal(raw, &node) != nil {
		return ""
	}
	var b strings.Builder
	var walk func(n map[string]any)
	walk = func(n map[string]any) {
		if t, ok := n["text"].(string); ok {
			b.WriteString(t)
		}
		if kids, ok := n["content"].([]any); ok {
			for _, k := range kids {
				if m, ok := k.(map[string]any); ok {
					walk(m)
				}
			}
		}
		switch n["type"] {
		case "paragraph", "heading", "listItem", "codeBlock", "blockquote":
			b.WriteString("\n")
		case "hardBreak":
			b.WriteString("\n")
		}
	}
	walk(node)
	return strings.TrimSpace(b.String())
}

// ---------------------------------------------------------------- ClickUp

type clickup struct{ token, list string }

func (c *clickup) Fetch(ctx context.Context) ([]ExternalTask, error) {
	auth := func(r *http.Request) { r.Header.Set("Authorization", c.token) }
	var out []ExternalTask
	for page := 0; len(out) < MaxItems && page < 50; page++ {
		var res struct {
			Tasks []struct {
				ID          string `json:"id"`
				CustomID    string `json:"custom_id"`
				Name        string `json:"name"`
				Description string `json:"text_content"`
				URL         string `json:"url"`
				Status      struct {
					Status string `json:"status"`
					Type   string `json:"type"`
				} `json:"status"`
				Tags []struct {
					Name string `json:"name"`
				} `json:"tags"`
				Priority *struct {
					Priority string `json:"priority"`
				} `json:"priority"`
			} `json:"tasks"`
			LastPage bool `json:"last_page"`
		}
		u := fmt.Sprintf("%s/list/%s/task?include_closed=true&subtasks=false&page=%d", ClickUpAPI, url.PathEscape(c.list), page)
		if err := getJSON(ctx, u, auth, &res); err != nil {
			return nil, fmt.Errorf("clickup: %w", err)
		}
		for _, t := range res.Tasks {
			et := ExternalTask{ID: t.ID, Key: "#" + t.ID, Title: t.Name, Description: t.Description,
				Status: t.Status.Status, Category: t.Status.Type, URL: t.URL}
			if t.CustomID != "" {
				et.Key = t.CustomID
			}
			for _, tag := range t.Tags {
				et.Labels = append(et.Labels, tag.Name)
			}
			if t.Priority != nil {
				et.Priority = normalizePriority(t.Priority.Priority)
			}
			out = append(out, et)
		}
		if res.LastPage || len(res.Tasks) == 0 {
			break
		}
	}
	return out, nil
}

// ---------------------------------------------------------------- Trello

type trello struct {
	key, token, board string
	lists             []string // list names to import (lowercase); empty = all open lists
}

func (t *trello) Fetch(ctx context.Context) ([]ExternalTask, error) {
	auth := func(r *http.Request) {
		r.Header.Set("Authorization", fmt.Sprintf(`OAuth oauth_consumer_key="%s", oauth_token="%s"`, t.key, t.token))
	}
	var lists []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	b := url.PathEscape(t.board)
	if err := getJSON(ctx, TrelloAPI+"/boards/"+b+"/lists?filter=open&fields=name", auth, &lists); err != nil {
		return nil, fmt.Errorf("trello: %w", err)
	}
	names := map[string]string{}
	for _, l := range lists {
		names[l.ID] = l.Name
	}
	var cards []struct {
		ID        string `json:"id"`
		ShortLink string `json:"shortLink"`
		Name      string `json:"name"`
		Desc      string `json:"desc"`
		IDList    string `json:"idList"`
		URL       string `json:"url"`
		Labels    []struct {
			Name string `json:"name"`
		} `json:"labels"`
	}
	if err := getJSON(ctx, TrelloAPI+"/boards/"+b+"/cards/open?fields=name,desc,idList,url,labels,shortLink", auth, &cards); err != nil {
		return nil, fmt.Errorf("trello: %w", err)
	}
	var out []ExternalTask
	for _, c := range cards {
		list, ok := names[c.IDList]
		if !ok {
			continue // archived list
		}
		if len(t.lists) > 0 && !slices.Contains(t.lists, strings.ToLower(list)) {
			continue
		}
		et := ExternalTask{ID: c.ID, Key: c.ShortLink, Title: c.Name, Description: c.Desc, Status: list, URL: c.URL}
		for _, l := range c.Labels {
			if l.Name != "" {
				et.Labels = append(et.Labels, l.Name)
			}
		}
		out = append(out, et)
		if len(out) >= MaxItems {
			break
		}
	}
	return out, nil
}

// normalizePriority maps the sources' names onto Wise's scale.
func normalizePriority(p string) string {
	switch strings.ToLower(strings.TrimSpace(p)) {
	case "highest", "urgent", "blocker", "critical", "1":
		return "urgent"
	case "high", "2":
		return "high"
	case "medium", "normal", "3":
		return "medium"
	case "low", "lowest", "trivial", "minor", "4", "5":
		return "low"
	}
	return "none"
}

// DefaultColumn maps a source status onto a board column when the
// integration's status map has no entry for it.
func DefaultColumn(kind string, t ExternalTask) string {
	cat, name := strings.ToLower(t.Category), strings.ToLower(t.Status)
	switch kind {
	case "jira":
		switch cat {
		case "done":
			return "done"
		case "indeterminate":
			return "in_progress"
		}
		return "todo"
	case "clickup":
		switch cat {
		case "done", "closed":
			return "done"
		case "custom":
			return "in_progress"
		}
		return "todo"
	}
	switch {
	case strings.Contains(name, "done") || strings.Contains(name, "complete") || strings.Contains(name, "closed"):
		return "done"
	case strings.Contains(name, "review") || strings.Contains(name, "qa"):
		return "review"
	case strings.Contains(name, "doing") || strings.Contains(name, "progress") || strings.Contains(name, "wip"):
		return "in_progress"
	case strings.Contains(name, "backlog") || strings.Contains(name, "icebox") || strings.Contains(name, "later"):
		return "backlog"
	}
	return "todo"
}
