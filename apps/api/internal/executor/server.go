package executor

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Handler serves the executor API. Every route except /healthz requires the
// bearer token; only the API and the orchestrator hold it.
// egressToken, when set, may only read the egress allowlist (the egress
// proxy holds it; it joins every sandbox network).
func Handler(m *Manager, token, egressToken string, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	h := &handler{m: m, log: log}
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("POST /v1/workspaces", h.prepare)
	mux.HandleFunc("GET /v1/workspaces/{id}", h.get)
	mux.HandleFunc("DELETE /v1/workspaces/{id}", h.remove)
	mux.HandleFunc("POST /v1/workspaces/{id}/stop", h.stop)
	mux.HandleFunc("POST /v1/workspaces/{id}/fs", h.fs)
	mux.HandleFunc("POST /v1/workspaces/{id}/commands", h.command)
	mux.HandleFunc("POST /v1/workspaces/{id}/snapshot", h.snapshot)
	mux.HandleFunc("POST /v1/workspaces/{id}/deliver", h.deliver)
	mux.HandleFunc("GET /v1/workspaces/{id}/diff", h.diff)
	mux.HandleFunc("POST /v1/workspaces/{id}/runners", h.runner)
	mux.HandleFunc("GET /v1/egress/grants/{token}", h.grant)
	mux.HandleFunc("GET /v1/jobs/{id}", h.job)
	mux.HandleFunc("DELETE /v1/jobs/{id}", h.cancel)
	mux.HandleFunc("GET /v1/egress", h.getEgress)
	mux.HandleFunc("PUT /v1/egress", h.putEgress)
	want := []byte("Bearer " + token)
	wantEgress := []byte("Bearer " + egressToken)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := []byte(r.Header.Get("Authorization"))
		full := subtle.ConstantTimeCompare(got, want) == 1
		readEgress := egressToken != "" && r.Method == http.MethodGet &&
			(r.URL.Path == "/v1/egress" || strings.HasPrefix(r.URL.Path, "/v1/egress/grants/")) &&
			subtle.ConstantTimeCompare(got, wantEgress) == 1
		if r.URL.Path != "/healthz" && !full && !readEgress {
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 64<<20)
		mux.ServeHTTP(w, r)
	})
}

type handler struct {
	m   *Manager
	log *slog.Logger
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func (h *handler) fail(w http.ResponseWriter, err error) {
	var br BadRequest
	switch {
	case errors.As(err, &br):
		writeErr(w, http.StatusBadRequest, br.Msg)
	case errors.Is(err, ErrNotFound):
		writeErr(w, http.StatusNotFound, "not found")
	case errors.Is(err, context.Canceled):
		writeErr(w, 499, "cancelled")
	default:
		h.log.Warn("executor request failed", "err", err)
		writeErr(w, http.StatusInternalServerError, err.Error())
	}
}

func decode(r *http.Request, v any) error {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		return bad("invalid request body: %v", err)
	}
	return nil
}

func (h *handler) prepare(w http.ResponseWriter, r *http.Request) {
	var req PrepareRequest
	if err := decode(r, &req); err != nil {
		h.fail(w, err)
		return
	}
	id, err := h.m.Prepare(r.Context(), req)
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"jobId": id})
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) {
	info, err := h.m.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, info)
}

func (h *handler) remove(w http.ResponseWriter, r *http.Request) {
	if err := h.m.Remove(r.Context(), r.PathValue("id")); err != nil {
		h.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) stop(w http.ResponseWriter, r *http.Request) {
	if err := h.m.Stop(r.Context(), r.PathValue("id")); err != nil {
		h.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) fs(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil || !json.Valid(body) {
		h.fail(w, bad("invalid request body"))
		return
	}
	res, err := h.m.FS(r.Context(), r.PathValue("id"), body)
	if err != nil {
		h.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(res)
}

func (h *handler) command(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Command  string `json:"command"`
		TimeoutS int    `json:"timeoutS"`
	}
	if err := decode(r, &req); err != nil {
		h.fail(w, err)
		return
	}
	id, err := h.m.Command(r.Context(), r.PathValue("id"), req.Command, req.TimeoutS)
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"jobId": id})
}

func (h *handler) snapshot(w http.ResponseWriter, r *http.Request) {
	var req SnapshotRequest
	if err := decode(r, &req); err != nil {
		h.fail(w, err)
		return
	}
	cs, err := h.m.Snapshot(r.Context(), r.PathValue("id"), req)
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, cs)
}

func (h *handler) deliver(w http.ResponseWriter, r *http.Request) {
	var req DeliverRequest
	if err := decode(r, &req); err != nil {
		h.fail(w, err)
		return
	}
	d, err := h.m.Deliver(r.Context(), r.PathValue("id"), req)
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (h *handler) diff(w http.ResponseWriter, r *http.Request) {
	maxBytes, _ := strconv.Atoi(r.URL.Query().Get("maxBytes"))
	d, err := h.m.Diff(r.Context(), r.PathValue("id"), maxBytes)
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (h *handler) runner(w http.ResponseWriter, r *http.Request) {
	var req RunnerRequest
	if err := decode(r, &req); err != nil {
		h.fail(w, err)
		return
	}
	id, err := h.m.Runner(r.Context(), r.PathValue("id"), req)
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"jobId": id})
}

func (h *handler) grant(w http.ResponseWriter, r *http.Request) {
	g, ok := h.m.Grant(r.PathValue("token"))
	if !ok {
		writeErr(w, http.StatusNotFound, "no grant")
		return
	}
	writeJSON(w, http.StatusOK, g)
}

func (h *handler) job(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	offset, _ := strconv.ParseInt(q.Get("offset"), 10, 64)
	wait, _ := strconv.Atoi(q.Get("wait"))
	st, err := h.m.Job(r.Context(), r.PathValue("id"), offset, time.Duration(min(max(wait, 0), 30))*time.Second)
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (h *handler) cancel(w http.ResponseWriter, r *http.Request) {
	if err := h.m.CancelJob(r.PathValue("id")); err != nil {
		h.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) getEgress(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"hosts": h.m.Egress()})
}

func (h *handler) putEgress(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Hosts []string `json:"hosts"`
	}
	if err := decode(r, &req); err != nil {
		h.fail(w, err)
		return
	}
	if err := h.m.SetEgress(req.Hosts); err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"hosts": h.m.Egress()})
}

// trimSpace is used by the client for error bodies.
func trimSpace(b []byte) string { return strings.TrimSpace(string(b)) }
