package gitworker

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"wise/apps/api/internal/githost"
)

// Handler serves the worker's API to the orchestrator (WISE_GIT_PUSH_TOKEN).
// The orchestrator sends only ids: it never holds a git token.
func (w *Worker) Handler(token string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(rw http.ResponseWriter, _ *http.Request) { _, _ = rw.Write([]byte("ok")) })
	mux.HandleFunc("POST /v1/push", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			ApprovalID uuid.UUID `json:"approvalId"`
		}
		if json.NewDecoder(io.LimitReader(r.Body, 1<<12)).Decode(&in) != nil || in.ApprovalID == uuid.Nil {
			writeErr(rw, http.StatusBadRequest, "approvalId required")
			return
		}
		res, err := w.Push(r.Context(), in.ApprovalID)
		reply(rw, res, err)
	})
	mux.HandleFunc("POST /v1/pull-requests", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			PushID     uuid.UUID  `json:"pushId"`
			ApprovalID *uuid.UUID `json:"approvalId"`
		}
		if json.NewDecoder(io.LimitReader(r.Body, 1<<12)).Decode(&in) != nil || in.PushID == uuid.Nil {
			writeErr(rw, http.StatusBadRequest, "pushId required")
			return
		}
		pr, err := w.OpenPR(r.Context(), in.PushID, in.ApprovalID)
		reply(rw, pr, err)
	})
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if token == "" || subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
				writeErr(rw, http.StatusUnauthorized, "token required")
				return
			}
		}
		mux.ServeHTTP(rw, r)
	})
}

func writeErr(rw http.ResponseWriter, status int, msg string) {
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(status)
	_ = json.NewEncoder(rw).Encode(map[string]string{"error": msg})
}

func reply(rw http.ResponseWriter, v any, err error) {
	if err != nil {
		var re *RefusedError
		if errors.As(err, &re) {
			writeErr(rw, http.StatusConflict, re.Msg)
			return
		}
		var se interface{ Temporary() bool }
		if errors.As(err, &se) || strings.HasPrefix(err.Error(), "API ") || strings.Contains(err.Error(), "could not be recorded") {
			writeErr(rw, http.StatusBadGateway, err.Error()) // transient: the orchestrator retries
			return
		}
		writeErr(rw, http.StatusUnprocessableEntity, err.Error())
		return
	}
	rw.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(rw).Encode(v)
}

// Client calls the worker (from the orchestrator).
type Client struct {
	URL, Token string
	HTTP       *http.Client
}

// NewClient returns a client.
func NewClient(url, token string) *Client {
	return &Client{URL: strings.TrimSuffix(url, "/"), Token: token, HTTP: &http.Client{Timeout: 30 * time.Minute}}
}

// Error is a refusal or failure reported by the worker.
type Error struct {
	Status int
	Msg    string
}

func (e *Error) Error() string { return e.Msg }

// Refused reports whether the operation was refused (not retried as is).
func (e *Error) Refused() bool {
	return e.Status == http.StatusConflict || e.Status == http.StatusBadRequest
}

func (c *Client) post(ctx context.Context, path string, body, out any) error {
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	res, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("git worker unreachable: %w", err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		return &Error{Status: res.StatusCode, Msg: e.Error}
	}
	return json.Unmarshal(raw, out)
}

// Push asks the worker to perform an approved push.
func (c *Client) Push(ctx context.Context, approvalID uuid.UUID) (PushResult, error) {
	var r PushResult
	return r, c.post(ctx, "/v1/push", map[string]any{"approvalId": approvalID}, &r)
}

// OpenPR asks the worker to open the pull request for a push.
func (c *Client) OpenPR(ctx context.Context, pushID uuid.UUID, approvalID *uuid.UUID) (githost.PR, error) {
	var pr githost.PR
	return pr, c.post(ctx, "/v1/pull-requests", map[string]any{"pushId": pushID, "approvalId": approvalID}, &pr)
}
