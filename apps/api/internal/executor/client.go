package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Client calls the executor API (used by the API and the orchestrator).
type Client struct {
	base  string
	token string
	http  *http.Client
}

// NewClient returns a client, or nil when the executor is not configured.
func NewClient(base, token string) *Client {
	if base == "" || token == "" {
		return nil
	}
	return &Client{base: base, token: token, http: &http.Client{Timeout: 5 * time.Minute}}
}

// Error is a failed executor call.
type Error struct {
	Status int
	Msg    string
}

func (e *Error) Error() string { return fmt.Sprintf("executor: %s", e.Msg) }

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		switch v := in.(type) {
		case json.RawMessage:
			body = bytes.NewReader(v)
		default:
			data, err := json.Marshal(in)
			if err != nil {
				return err
			}
			body = bytes.NewReader(data)
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("executor unreachable: %w", err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 64<<20))
	if err != nil {
		return err
	}
	if res.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &e)
		if e.Error == "" {
			e.Error = trimSpace(data)
		}
		return &Error{Status: res.StatusCode, Msg: e.Error}
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	if raw, ok := out.(*json.RawMessage); ok {
		*raw = append((*raw)[:0], data...)
		return nil
	}
	return json.Unmarshal(data, out)
}

type jobRef struct {
	JobID string `json:"jobId"`
}

// Prepare starts preparing a workspace; it returns the job id.
func (c *Client) Prepare(ctx context.Context, req PrepareRequest) (string, error) {
	var r jobRef
	return r.JobID, c.do(ctx, http.MethodPost, "/v1/workspaces", req, &r)
}

// Get returns a workspace.
func (c *Client) Get(ctx context.Context, id string) (Info, error) {
	var r Info
	return r, c.do(ctx, http.MethodGet, "/v1/workspaces/"+url.PathEscape(id), nil, &r)
}

// Remove deletes a workspace.
func (c *Client) Remove(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/v1/workspaces/"+url.PathEscape(id), nil, nil)
}

// Stop stops a workspace's sandbox.
func (c *Client) Stop(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/v1/workspaces/"+url.PathEscape(id)+"/stop", nil, nil)
}

// FS runs a file operation and returns its raw JSON result.
func (c *Client) FS(ctx context.Context, id string, req any) (json.RawMessage, error) {
	var r json.RawMessage
	return r, c.do(ctx, http.MethodPost, "/v1/workspaces/"+url.PathEscape(id)+"/fs", req, &r)
}

// Command starts a command; it returns the job id.
func (c *Client) Command(ctx context.Context, id, command string, timeoutS int) (string, error) {
	var r jobRef
	err := c.do(ctx, http.MethodPost, "/v1/workspaces/"+url.PathEscape(id)+"/commands",
		map[string]any{"command": command, "timeoutS": timeoutS}, &r)
	return r.JobID, err
}

// Job polls a job, waiting up to wait seconds for news.
func (c *Client) Job(ctx context.Context, jobID string, offset int64, wait int) (JobStatus, error) {
	var r JobStatus
	q := url.Values{"offset": {strconv.FormatInt(offset, 10)}, "wait": {strconv.Itoa(wait)}}
	return r, c.do(ctx, http.MethodGet, "/v1/jobs/"+url.PathEscape(jobID)+"?"+q.Encode(), nil, &r)
}

// CancelJob stops a job.
func (c *Client) CancelJob(ctx context.Context, jobID string) error {
	return c.do(ctx, http.MethodDelete, "/v1/jobs/"+url.PathEscape(jobID), nil, nil)
}

// Snapshot commits and verifies the workspace's changes.
func (c *Client) Snapshot(ctx context.Context, id string, req SnapshotRequest) (Changeset, error) {
	var r Changeset
	return r, c.do(ctx, http.MethodPost, "/v1/workspaces/"+url.PathEscape(id)+"/snapshot", req, &r)
}

// Reword changes the snapshot commit's message (before final approval).
func (c *Client) Reword(ctx context.Context, id string, req RewordRequest) (Changeset, error) {
	var r Changeset
	return r, c.do(ctx, http.MethodPost, "/v1/workspaces/"+url.PathEscape(id)+"/reword", req, &r)
}

// Deliver writes an approved commit as a branch into the user's repository.
func (c *Client) Deliver(ctx context.Context, id string, req DeliverRequest) (Delivery, error) {
	var r Delivery
	return r, c.do(ctx, http.MethodPost, "/v1/workspaces/"+url.PathEscape(id)+"/deliver", req, &r)
}

// Diff returns the workspace's current (unverified) changes.
func (c *Client) Diff(ctx context.Context, id string, maxBytes int) (WorkingDiff, error) {
	var r WorkingDiff
	return r, c.do(ctx, http.MethodGet, "/v1/workspaces/"+url.PathEscape(id)+"/diff?maxBytes="+strconv.Itoa(maxBytes), nil, &r)
}

// Runner starts a coding CLI runner on a workspace; it returns the job id.
func (c *Client) Runner(ctx context.Context, id string, req RunnerRequest) (string, error) {
	var r jobRef
	return r.JobID, c.do(ctx, http.MethodPost, "/v1/workspaces/"+url.PathEscape(id)+"/runners", req, &r)
}

// SetEgress replaces the egress allowlist.
func (c *Client) SetEgress(ctx context.Context, hosts []string) error {
	return c.do(ctx, http.MethodPut, "/v1/egress", map[string]any{"hosts": hosts}, nil)
}

// Wait polls a job until it finishes, passing new output to onOutput and
// calling beat between polls (for Temporal heartbeats). Cancelling ctx
// cancels the job.
func (c *Client) Wait(ctx context.Context, jobID string, onOutput func(string), beat func()) (JobStatus, error) {
	var offset int64
	var st0 JobStatus
	for {
		wait := 20
		if offset > 0 && st0.More {
			wait = 0
		}
		st, err := c.Job(ctx, jobID, offset, wait)
		st0 = st
		if ctx.Err() != nil {
			cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			_ = c.CancelJob(cctx, jobID)
			cancel()
			return st, ctx.Err()
		}
		if err != nil {
			return st, err
		}
		if st.Output != "" && onOutput != nil {
			onOutput(st.Output)
		}
		offset = st.NextOffset
		if beat != nil {
			beat()
		}
		if st.Status != JobRunning && !st.More {
			return st, nil
		}
	}
}
