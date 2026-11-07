package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/go-chi/chi/v5/middleware"

	"wise/apps/api/internal/apperr"
	"wise/apps/api/internal/httpapi/apigen"
)

// problemWriter renders errors as RFC 7807 documents, logging unexpected ones.
type problemWriter struct{ log *slog.Logger }

func (p problemWriter) write(w http.ResponseWriter, r *http.Request, err error) {
	ae, ok := apperr.As(err)
	if !ok {
		p.log.ErrorContext(r.Context(), "unhandled error",
			"err", err, "method", r.Method, "path", r.URL.Path, "request_id", middleware.GetReqID(r.Context()))
		ae = &apperr.Error{Status: http.StatusInternalServerError, Title: http.StatusText(http.StatusInternalServerError),
			Detail: "an unexpected error occurred"}
	} else if ae.Unwrap() != nil {
		p.log.WarnContext(r.Context(), "client error", "status", ae.Status, "err", ae.Unwrap())
	}
	writeProblem(w, ae)
}

func writeProblem(w http.ResponseWriter, ae *apperr.Error) {
	prob := apigen.Problem{Type: "about:blank", Title: ae.Title, Status: ae.Status}
	if ae.Detail != "" {
		prob.Detail = &ae.Detail
	}
	if len(ae.Fields) > 0 {
		fe := make([]apigen.FieldError, len(ae.Fields))
		for i, f := range ae.Fields {
			fe[i] = apigen.FieldError{Field: f.Field, Message: f.Message}
		}
		prob.Errors = &fe
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(ae.Status)
	_ = json.NewEncoder(w).Encode(prob)
}

// validationError converts an OpenAPI request validation failure to an apperr.
func validationError(err error, status int) *apperr.Error {
	if errors.Is(err, routers.ErrPathNotFound) {
		return apperr.NotFound("no such endpoint")
	}
	if errors.Is(err, routers.ErrMethodNotAllowed) {
		return &apperr.Error{Status: http.StatusMethodNotAllowed, Title: http.StatusText(http.StatusMethodNotAllowed)}
	}
	var fields []apperr.FieldError
	collect := func(e error) {
		var re *openapi3filter.RequestError
		if errors.As(e, &re) {
			field := "body"
			if re.Parameter != nil {
				field = re.Parameter.Name
			}
			var se *openapi3.SchemaError
			if errors.As(re.Err, &se) {
				if ptr := se.JSONPointer(); len(ptr) > 0 {
					field = joinPointer(ptr)
				}
				fields = append(fields, apperr.FieldError{Field: field, Message: se.Reason})
				return
			}
			fields = append(fields, apperr.FieldError{Field: field, Message: re.Error()})
			return
		}
		fields = append(fields, apperr.FieldError{Field: "request", Message: e.Error()})
	}
	var multi openapi3.MultiError
	if errors.As(err, &multi) {
		for _, e := range multi {
			collect(e)
		}
	} else {
		collect(err)
	}
	if status == 0 || status == http.StatusInternalServerError {
		status = http.StatusBadRequest
	}
	e := apperr.Validation(fields...)
	e.Status = status
	e.Title = http.StatusText(status)
	return e
}

func joinPointer(p []string) string {
	out := ""
	for i, s := range p {
		if i > 0 {
			out += "."
		}
		out += s
	}
	return out
}
