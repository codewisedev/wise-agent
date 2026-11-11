package httpapi

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"wise/apps/api/internal/apperr"
	"wise/apps/api/internal/githost"
)

// Internal routes for the trusted git worker only (WISE_GIT_WORKER_TOKEN).
// They are outside the user API: no session or user token is accepted, and
// without a configured token they do not exist.
const gitInternalPrefix = BasePath + "/internal/git"

func (s *Server) gitWorkerAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if s.GitWorkerToken == "" || len(tok) < 32 || subtle.ConstantTimeCompare([]byte(tok), []byte(s.GitWorkerToken)) != 1 {
			writeProblem(w, apperr.Unauthorized("git worker token required"))
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		next(w, r)
	}
}

func (s *Server) mountGitInternal(r chi.Router) {
	r.Post(gitInternalPrefix+"/push-grants/{approvalId}", s.gitWorkerAuth(func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(chi.URLParam(r, "approvalId"))
		if err != nil {
			writeProblem(w, apperr.BadRequest("invalid approval id"))
			return
		}
		g, err := s.Release.PushGrant(r.Context(), id)
		writeJSONOrProblem(w, g, err)
	}))
	r.Post(gitInternalPrefix+"/pushes/{pushId}/result", s.gitWorkerAuth(func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(chi.URLParam(r, "pushId"))
		var in struct {
			OK      bool   `json:"ok"`
			Existed bool   `json:"existed"`
			Error   string `json:"error"`
		}
		if err != nil || json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in) != nil {
			writeProblem(w, apperr.BadRequest("invalid result"))
			return
		}
		_, err = s.Release.PushResult(r.Context(), id, in.OK, in.Existed, in.Error)
		writeJSONOrProblem(w, map[string]bool{"recorded": err == nil}, err)
	}))
	r.Post(gitInternalPrefix+"/pr-grants/{pushId}", s.gitWorkerAuth(func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(chi.URLParam(r, "pushId"))
		if err != nil {
			writeProblem(w, apperr.BadRequest("invalid push id"))
			return
		}
		var approval *uuid.UUID
		if a := r.URL.Query().Get("approvalId"); a != "" {
			v, err := uuid.Parse(a)
			if err != nil {
				writeProblem(w, apperr.BadRequest("invalid approval id"))
				return
			}
			approval = &v
		}
		g, err := s.Release.PRGrant(r.Context(), id, approval)
		writeJSONOrProblem(w, g, err)
	}))
	r.Post(gitInternalPrefix+"/pull-requests/{prId}/result", s.gitWorkerAuth(func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(chi.URLParam(r, "prId"))
		var in struct {
			OK    bool       `json:"ok"`
			PR    githost.PR `json:"pr"`
			Error string     `json:"error"`
		}
		if err != nil || json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in) != nil {
			writeProblem(w, apperr.BadRequest("invalid result"))
			return
		}
		_, err = s.Release.PRResult(r.Context(), id, in.OK, in.PR, in.Error)
		writeJSONOrProblem(w, map[string]bool{"recorded": err == nil}, err)
	}))
}

func writeJSONOrProblem(w http.ResponseWriter, v any, err error) {
	if err != nil {
		if e, ok := apperr.As(err); ok {
			writeProblem(w, e)
			return
		}
		writeProblem(w, &apperr.Error{Status: http.StatusInternalServerError, Title: "Internal Server Error", Detail: "an unexpected error occurred"})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
