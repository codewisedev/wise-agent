package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"

	"wise/apps/api/internal/apperr"
	"wise/apps/api/internal/audit"
	"wise/apps/api/internal/auth"
)

const (
	runtimePrefix = BasePath + "/runtime/"
	claimPath     = BasePath + "/runtime/v1/runs/claim"
	runsPrefix    = BasePath + "/runtime/v1/runs/"
	indexClaim    = BasePath + "/runtime/v1/index/claim"
	indexPrefix   = BasePath + "/runtime/v1/index/"
	wsPath        = BasePath + "/events/ws"
)

type runCtxKey struct{}

// runtimeAuth authenticates agent runtimes: the service token for claims and a
// per-run token (bound to the run in the URL) for everything else.
func (s *Server) runtimeAuth(writeErr auth.ErrorWriter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token, ok := bearerToken(r)
			if !ok {
				writeErr(w, r, apperr.Unauthorized("runtime credentials required"))
				return
			}
			ctx := audit.WithActor(r.Context(), audit.System)
			if r.URL.Path == claimPath || r.URL.Path == indexClaim {
				if s.RuntimeToken == "" {
					writeErr(w, r, apperr.Unavailable("agent runtime disabled: WISE_RUNTIME_TOKEN is not configured"))
					return
				}
				if subtle.ConstantTimeCompare([]byte(token), []byte(s.RuntimeToken)) != 1 {
					writeErr(w, r, apperr.Unauthorized("invalid runtime token"))
					return
				}
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
			// Index jobs: a per-job token bound to the job in the URL.
			if strings.HasPrefix(r.URL.Path, indexPrefix) {
				if s.Index == nil {
					writeErr(w, r, apperr.Unavailable("indexing is not configured"))
					return
				}
				job, err := s.Index.Authenticate(ctx, token)
				if err != nil {
					writeErr(w, r, err)
					return
				}
				idStr, _, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, indexPrefix), "/")
				if id, err := uuid.Parse(idStr); err != nil || id != job.ID {
					writeErr(w, r, apperr.Forbidden("index job token does not grant access to this job"))
					return
				}
				next.ServeHTTP(w, r.WithContext(context.WithValue(ctx, indexJobCtxKey{}, job)))
				return
			}
			run, err := s.RunQueue.Authenticate(ctx, token)
			if err != nil {
				writeErr(w, r, err)
				return
			}
			idStr, _, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, runsPrefix), "/")
			if id, err := uuid.Parse(idStr); err != nil || id != run.ID {
				writeErr(w, r, apperr.Forbidden("run token does not grant access to this run"))
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(ctx, runCtxKey{}, run)))
		})
	}
}

func bearerToken(r *http.Request) (string, bool) {
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(token) == "" {
		return "", false
	}
	return strings.TrimSpace(token), true
}

// isLongRunning marks requests exempt from the default request timeout:
// model turns (a provider call can take minutes) and WebSocket streams.
func isLongRunning(r *http.Request) bool {
	return r.URL.Path == wsPath || (strings.HasPrefix(r.URL.Path, runsPrefix) &&
		(strings.HasSuffix(r.URL.Path, "/model-turn") || strings.HasSuffix(r.URL.Path, "/workspace/command")))
}

// extendDeadlines lifts the server's read/write deadlines for long requests.
func extendDeadlines(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isLongRunning(r) {
			rc := http.NewResponseController(w)
			if r.URL.Path == wsPath {
				_ = rc.SetReadDeadline(time.Time{})
				_ = rc.SetWriteDeadline(time.Time{})
			} else if strings.HasSuffix(r.URL.Path, "/workspace/command") {
				_ = rc.SetWriteDeadline(time.Now().Add(6*time.Hour + 5*time.Minute)) // the command timeout's upper bound
			} else {
				_ = rc.SetWriteDeadline(time.Now().Add(20 * time.Minute))
			}
		}
		next.ServeHTTP(w, r)
	})
}

// eventsWebSocket streams events as JSON messages:
//   - ?runId=<id>        agent events of one run (no parameter: every run)
//   - ?workflowId=<id>   workflow events of one workflow run
//   - ?stream=workflows  workflow events of every workflow (approvals inbox)
func (s *Server) eventsWebSocket(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	parseID := func(name string) (*uuid.UUID, bool) {
		v := q.Get(name)
		if v == "" {
			return nil, true
		}
		id, err := uuid.Parse(v)
		if err != nil {
			writeProblem(w, apperr.BadRequest(name+" must be a UUID"))
			return nil, false
		}
		return &id, true
	}
	runID, ok := parseID("runId")
	if !ok {
		return
	}
	workflowID, ok := parseID("workflowId")
	if !ok {
		return
	}
	workflows := workflowID != nil || q.Get("stream") == "workflows"
	if workflows && runID != nil {
		writeProblem(w, apperr.BadRequest("use either runId or workflowId/stream=workflows"))
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: s.WSOriginPatterns})
	if err != nil {
		return
	}
	defer conn.CloseNow()
	ctx := conn.CloseRead(r.Context())
	if workflows {
		ch, cancel := s.Hub.SubscribeWorkflow(workflowID)
		defer cancel()
		pumpEvents(ctx, conn, ch)
		return
	}
	ch, cancel := s.Hub.Subscribe(runID)
	defer cancel()
	pumpEvents(ctx, conn, ch)
}

// pumpEvents writes each event as a JSON text message and pings every 30s.
func pumpEvents[T any](ctx context.Context, conn *websocket.Conn, ch <-chan T) {
	ping := time.NewTicker(30 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			conn.Close(websocket.StatusNormalClosure, "")
			return
		case <-ping.C:
			pctx, c := context.WithTimeout(ctx, 10*time.Second)
			err := conn.Ping(pctx)
			c()
			if err != nil {
				return
			}
		case ev, ok := <-ch:
			if !ok {
				return
			}
			b, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			wctx, c := context.WithTimeout(ctx, 10*time.Second)
			err = conn.Write(wctx, websocket.MessageText, b)
			c()
			if err != nil {
				return
			}
		}
	}
}
