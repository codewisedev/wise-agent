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
	"regexp"
	"slices"
	"strconv"
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
	Priority    string  // none|low|medium|high|urgent
	Sprint      *Sprint // the source's sprint, in follow mode
}

// Sprint is a sprint as a source reports it (a ClickUp sprint list, a Jira sprint).
type Sprint struct {
	ID       string
	Name     string
	StartsOn *time.Time
	EndsOn   *time.Time
	Closed   bool
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
		base := strings.TrimSuffix(cred.BaseURL, "/")
		jql := str(cfg, "jql")
		if str(cfg, "mode") == "board" {
			board := parseJiraBoard(str(cfg, "boardId"))
			if board == "" {
				return nil, errors.New("set the Jira board (its id or a link to it) to follow its sprints")
			}
			if mine(cfg) {
				jql = joinJQL("assignee = currentUser()", jql)
			}
			return &jiraBoard{jira: jira{base: base, email: str(cred.Meta, "email"), token: cred.Secret, jql: jql}, board: board}, nil
		}
		if jql == "" {
			return nil, errors.New("set a JQL filter (e.g. project = ABC AND statusCategory != Done)")
		}
		if mine(cfg) {
			jql = assignedJQL(jql)
		}
		return &jira{base: base, email: str(cred.Meta, "email"), token: cred.Secret, jql: jql}, nil
	case "clickup":
		if cred.Secret == "" {
			return nil, errors.New("the ClickUp account needs an API token")
		}
		if str(cfg, "mode") == "folder" {
			ref, err := parseClickUpFolder(str(cfg, "folderId"))
			if err != nil {
				return nil, err
			}
			return &clickup{token: cred.Secret, ref: ref, mine: mine(cfg), folder: true}, nil
		}
		ref, err := parseClickUpList(str(cfg, "listId"))
		if err != nil {
			return nil, err
		}
		return &clickup{token: cred.Secret, ref: ref, mine: mine(cfg)}, nil
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
		return &trello{key: str(cred.Meta, "apiKey"), token: cred.Secret, board: parseTrelloBoard(board), lists: lists, mine: mine(cfg)}, nil
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
			"fields": {jiraFields},
		}
		if next != "" {
			q.Set("nextPageToken", next)
		}
		var page struct {
			Issues        []jiraIssue `json:"issues"`
			NextPageToken string      `json:"nextPageToken"`
			IsLast        bool        `json:"isLast"`
		}
		if err := getJSON(ctx, j.base+"/rest/api/3/search/jql?"+q.Encode(), auth, &page); err != nil {
			return nil, fmt.Errorf("jira: %w", err)
		}
		for _, is := range page.Issues {
			out = append(out, j.task(is))
		}
		if page.IsLast || page.NextPageToken == "" || len(page.Issues) == 0 {
			break
		}
		next = page.NextPageToken
	}
	return out, nil
}

type jiraIssue struct {
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
}

const jiraFields = "summary,description,status,labels,priority"

func (j *jira) task(is jiraIssue) ExternalTask {
	t := ExternalTask{
		ID: is.ID, Key: is.Key, Title: is.Fields.Summary, Description: adfText(is.Fields.Description),
		Status: is.Fields.Status.Name, Category: is.Fields.Status.Category.Key,
		URL: j.base + "/browse/" + url.PathEscape(is.Key), Labels: is.Fields.Labels,
	}
	if is.Fields.Priority != nil {
		t.Priority = normalizePriority(is.Fields.Priority.Name)
	}
	return t
}

// jiraBoard follows a Jira Software board: the issues of its active and future sprints.
type jiraBoard struct {
	jira
	board string
	read  []string
}

// FetchedSprints lists the sprints the last Fetch read.
func (j *jiraBoard) FetchedSprints() ([]string, bool) { return j.read, true }

var jiraBoardURL = regexp.MustCompile(`/boards?/(\d+)`)

// parseJiraBoard accepts a board id or a board URL (…/boards/12).
func parseJiraBoard(v string) string {
	v = strings.TrimSpace(v)
	if m := jiraBoardURL.FindStringSubmatch(v); m != nil {
		return m[1]
	}
	if clickUpListID.MatchString(v) {
		return v
	}
	return ""
}

func jiraDate(s string) *time.Time {
	if s == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		if t, err = time.Parse("2006-01-02T15:04:05.000-0700", s); err != nil {
			return nil
		}
	}
	return &t
}

func (j *jiraBoard) Fetch(ctx context.Context) ([]ExternalTask, error) {
	auth := func(r *http.Request) { r.SetBasicAuth(j.email, j.token) }
	var sprints struct {
		Values []struct {
			ID        int64  `json:"id"`
			Name      string `json:"name"`
			State     string `json:"state"`
			StartDate string `json:"startDate"`
			EndDate   string `json:"endDate"`
		} `json:"values"`
	}
	u := fmt.Sprintf("%s/rest/agile/1.0/board/%s/sprint?state=active,future&maxResults=50", j.base, url.PathEscape(j.board))
	if err := getJSON(ctx, u, auth, &sprints); err != nil {
		return nil, fmt.Errorf("jira board %s: %w", j.board, err)
	}
	var out []ExternalTask
	j.read = j.read[:0]
	for _, sp := range sprints.Values {
		j.read = append(j.read, strconv.FormatInt(sp.ID, 10))
		s := &Sprint{ID: strconv.FormatInt(sp.ID, 10), Name: sp.Name, StartsOn: jiraDate(sp.StartDate), EndsOn: jiraDate(sp.EndDate), Closed: sp.State == "closed"}
		for start := 0; len(out) < MaxItems; {
			q := url.Values{"fields": {jiraFields}, "maxResults": {"100"}, "startAt": {strconv.Itoa(start)}}
			if j.jql != "" {
				q.Set("jql", j.jql)
			}
			var page struct {
				Issues []jiraIssue `json:"issues"`
				Total  int         `json:"total"`
			}
			u := fmt.Sprintf("%s/rest/agile/1.0/sprint/%d/issue?%s", j.base, sp.ID, q.Encode())
			if err := getJSON(ctx, u, auth, &page); err != nil {
				return nil, fmt.Errorf("jira sprint %s: %w", sp.Name, err)
			}
			for _, is := range page.Issues {
				t := j.task(is)
				t.Sprint = s
				out = append(out, t)
			}
			start += len(page.Issues)
			if len(page.Issues) == 0 || start >= page.Total {
				break
			}
		}
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

type clickup struct {
	token  string
	ref    clickUpRef
	mine   bool // only tasks assigned to the account's user
	folder bool // follow a sprint folder: every current/upcoming list is a sprint
	read   []string
}

// FetchedSprints lists the sprints the last Fetch read and whether the source
// follows sprints, so only those sprints' missing tasks are archived.
func (c *clickup) FetchedSprints() ([]string, bool) { return c.read, c.folder }

// clickUpRef names a list, a folder, or a view to resolve to one of them.
type clickUpRef struct{ list, view, folder string }

var clickUpFolderURL = regexp.MustCompile(`/v/(?:[a-z]+/)*f/(\d+)`) // …/v/f/<id>, …/v/o/f/<id>

// parseClickUpFolder accepts a folder id or link, or a link to any list or
// view inside the folder (the folder is looked up).
func parseClickUpFolder(v string) (clickUpRef, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return clickUpRef{}, errors.New("set the ClickUp sprint folder (a link to it, or to any sprint list in it)")
	}
	if m := clickUpFolderURL.FindStringSubmatch(v); m != nil {
		return clickUpRef{folder: m[1]}, nil
	}
	if clickUpListID.MatchString(v) {
		return clickUpRef{folder: v}, nil
	}
	return parseClickUpList(v)
}

var (
	clickUpListURL = regexp.MustCompile(`/li/(\d+)`)
	clickUpViewURL = regexp.MustCompile(`/v/[a-z]+/([0-9a-z]+-[0-9a-z]+-\d+)`)
	clickUpViewID  = regexp.MustCompile(`^[0-9a-z]+-[0-9a-z]+-\d+$`)
	clickUpListID  = regexp.MustCompile(`^[A-Za-z0-9_]+$`)
)

// parseClickUpList accepts a list id, a list URL (…/li/<id>), a view URL
// (…/v/l/6-<id>-1) or a bare view id.
func parseClickUpList(v string) (clickUpRef, error) {
	v = strings.TrimSpace(v)
	switch {
	case v == "":
		return clickUpRef{}, errors.New("set the ClickUp list (its id or a link to it)")
	case clickUpListID.MatchString(v):
		return clickUpRef{list: v}, nil
	case clickUpViewID.MatchString(v):
		return clickUpRef{view: v}, nil
	}
	if m := clickUpListURL.FindStringSubmatch(v); m != nil {
		return clickUpRef{list: m[1]}, nil
	}
	if m := clickUpViewURL.FindStringSubmatch(v); m != nil {
		return clickUpRef{view: m[1]}, nil
	}
	return clickUpRef{}, fmt.Errorf("%q is not a ClickUp list id or link (open the list and copy the address)", v)
}

// listID resolves a view to the list it shows.
func (c *clickup) listID(ctx context.Context, auth func(*http.Request)) (string, error) {
	if c.ref.list != "" {
		return c.ref.list, nil
	}
	var res struct {
		View struct {
			Parent struct {
				ID   string `json:"id"`
				Type int    `json:"type"`
			} `json:"parent"`
		} `json:"view"`
	}
	if err := getJSON(ctx, ClickUpAPI+"/view/"+url.PathEscape(c.ref.view), auth, &res); err != nil {
		return "", fmt.Errorf("clickup: resolving view %s: %w", c.ref.view, err)
	}
	switch {
	case res.View.Parent.Type == 6 && res.View.Parent.ID != "":
		return res.View.Parent.ID, nil
	case res.View.Parent.Type == 5 && c.folder:
		return "folder:" + res.View.Parent.ID, nil
	}
	return "", fmt.Errorf("clickup: view %s belongs to a space or folder, not a list; open the list itself and copy its link", c.ref.view)
}

// folderID finds the sprint folder: given directly, or the folder of a list or view.
func (c *clickup) folderID(ctx context.Context, auth func(*http.Request)) (string, error) {
	if c.ref.folder != "" {
		return c.ref.folder, nil
	}
	list, err := c.listID(ctx, auth)
	if err != nil {
		return "", err
	}
	if f, ok := strings.CutPrefix(list, "folder:"); ok {
		return f, nil
	}
	var res struct {
		Folder struct {
			ID     string `json:"id"`
			Hidden bool   `json:"hidden"`
		} `json:"folder"`
	}
	if err := getJSON(ctx, ClickUpAPI+"/list/"+url.PathEscape(list), auth, &res); err != nil {
		return "", fmt.Errorf("clickup: list %s: %w", list, err)
	}
	if res.Folder.ID == "" || res.Folder.Hidden {
		return "", errors.New("clickup: that list is not in a folder; sprint lists live in a sprint folder")
	}
	return res.Folder.ID, nil
}

func clickUpDate(ms string) *time.Time {
	n, err := strconv.ParseInt(ms, 10, 64)
	if err != nil || n <= 0 {
		return nil
	}
	t := time.UnixMilli(n).UTC()
	return &t
}

// sprintLists returns the folder's lists that are current or upcoming (or
// ended within the last week, so tasks moved out of them are picked up).
func (c *clickup) sprintLists(ctx context.Context, auth func(*http.Request), now time.Time) ([]Sprint, error) {
	folder, err := c.folderID(ctx, auth)
	if err != nil {
		return nil, err
	}
	var res struct {
		Lists []struct {
			ID        string  `json:"id"`
			Name      string  `json:"name"`
			StartDate *string `json:"start_date"`
			DueDate   *string `json:"due_date"`
		} `json:"lists"`
	}
	if err := getJSON(ctx, ClickUpAPI+"/folder/"+url.PathEscape(folder)+"/list?archived=false", auth, &res); err != nil {
		return nil, fmt.Errorf("clickup: folder %s: %w", folder, err)
	}
	cutoff := now.AddDate(0, 0, -7)
	var out []Sprint
	for _, l := range res.Lists {
		var start, end *time.Time
		if l.StartDate != nil {
			start = clickUpDate(*l.StartDate)
		}
		if l.DueDate != nil {
			end = clickUpDate(*l.DueDate)
		}
		if end != nil && end.Before(cutoff) {
			continue
		}
		out = append(out, Sprint{ID: l.ID, Name: l.Name, StartsOn: start, EndsOn: end})
	}
	if len(out) > 12 { // keep the latest dozen
		out = out[len(out)-12:]
	}
	return out, nil
}

func (c *clickup) Fetch(ctx context.Context) ([]ExternalTask, error) {
	auth := func(r *http.Request) { r.Header.Set("Authorization", c.token) }
	assignee, err := c.assignee(ctx, auth)
	if err != nil {
		return nil, err
	}
	if c.folder {
		sprints, err := c.sprintLists(ctx, auth, time.Now())
		if err != nil {
			return nil, err
		}
		var out []ExternalTask
		c.read = c.read[:0]
		for i := range sprints {
			c.read = append(c.read, sprints[i].ID)
			items, err := c.fetchList(ctx, auth, sprints[i].ID, assignee, MaxItems-len(out))
			if err != nil {
				return nil, err
			}
			for j := range items {
				items[j].Sprint = &sprints[i]
			}
			out = append(out, items...)
		}
		return out, nil
	}
	list, err := c.listID(ctx, auth)
	if err != nil {
		return nil, err
	}
	return c.fetchList(ctx, auth, list, assignee, MaxItems)
}

// assignee is the query filter for "only my tasks" ("" = everyone's).
func (c *clickup) assignee(ctx context.Context, auth func(*http.Request)) (string, error) {
	assignee := ""
	if c.mine {
		var me struct {
			User struct {
				ID int64 `json:"id"`
			} `json:"user"`
		}
		if err := getJSON(ctx, ClickUpAPI+"/user", auth, &me); err != nil {
			return "", fmt.Errorf("clickup: who am I: %w", err)
		}
		assignee = "&assignees%5B%5D=" + strconv.FormatInt(me.User.ID, 10)
	}
	return assignee, nil
}

func (c *clickup) fetchList(ctx context.Context, auth func(*http.Request), list, assignee string, limit int) ([]ExternalTask, error) {
	var out []ExternalTask
	for page := 0; len(out) < limit && page < 50; page++ {
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
		u := fmt.Sprintf("%s/list/%s/task?include_closed=true&subtasks=false&page=%d%s", ClickUpAPI, url.PathEscape(list), page, assignee)
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
	mine              bool     // only cards the account's member is on
}

var trelloBoardURL = regexp.MustCompile(`trello\.com/b/([A-Za-z0-9]+)`)

// parseTrelloBoard accepts a board id/short link or a board URL.
func parseTrelloBoard(v string) string {
	if m := trelloBoardURL.FindStringSubmatch(v); m != nil {
		return m[1]
	}
	return strings.TrimSpace(v)
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
	me := ""
	if t.mine {
		var m struct {
			ID string `json:"id"`
		}
		if err := getJSON(ctx, TrelloAPI+"/members/me?fields=id", auth, &m); err != nil {
			return nil, fmt.Errorf("trello: who am I: %w", err)
		}
		me = m.ID
	}
	var cards []struct {
		ID        string   `json:"id"`
		IDMembers []string `json:"idMembers"`
		ShortLink string   `json:"shortLink"`
		Name      string   `json:"name"`
		Desc      string   `json:"desc"`
		IDList    string   `json:"idList"`
		URL       string   `json:"url"`
		Labels    []struct {
			Name string `json:"name"`
		} `json:"labels"`
	}
	if err := getJSON(ctx, TrelloAPI+"/boards/"+b+"/cards/open?fields=name,desc,idList,idMembers,url,labels,shortLink", auth, &cards); err != nil {
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
		if me != "" && !slices.Contains(c.IDMembers, me) {
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

// mine reports the "only items assigned to me" option.
func mine(cfg map[string]any) bool {
	v, _ := cfg["assignedToMe"].(bool)
	return v
}

var orderBy = regexp.MustCompile(`(?i)\s+order\s+by\s+`)

// assignedJQL limits a JQL filter to the account's user, keeping any ORDER BY.
func assignedJQL(jql string) string { return assignedJQLWith("assignee = currentUser()", jql) }

// joinJQL ANDs two JQL filters (either may be empty).
func joinJQL(a, b string) string {
	switch {
	case strings.TrimSpace(b) == "":
		return a
	case strings.TrimSpace(a) == "":
		return b
	}
	return assignedJQLWith(a, b)
}

func assignedJQLWith(prefix, jql string) string {
	where, order := jql, ""
	if loc := orderBy.FindStringIndex(jql); loc != nil {
		where, order = jql[:loc[0]], " "+strings.TrimSpace(jql[loc[0]:])
	}
	return prefix + " AND (" + strings.TrimSpace(where) + ")" + order
}
