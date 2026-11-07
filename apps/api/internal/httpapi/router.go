// Package httpapi wires the generated OpenAPI server, middleware and domain services.
package httpapi

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	oapimw "github.com/oapi-codegen/nethttp-middleware"

	"wise/apps/api/internal/apperr"
	"wise/apps/api/internal/auth"
	"wise/apps/api/internal/httpapi/apigen"
	apicontract "wise/packages/contracts/api"
)

const BasePath = "/api/v1"

// publicRoutes skip authentication ("METHOD path").
var publicRoutes = map[string]bool{
	"GET " + BasePath + "/healthz":      true,
	"GET " + BasePath + "/readyz":       true,
	"POST " + BasePath + "/auth/login":  true,
	"GET " + BasePath + "/openapi.yaml": true,
}

type RouterOptions struct {
	Logger            *slog.Logger
	TrustProxyHeaders bool
	CORSOrigins       []string
}

// NewRouter builds the HTTP handler for the API.
func NewRouter(srv *Server, opts RouterOptions) (http.Handler, error) {
	spec, err := openapi3.NewLoader().LoadFromData(apicontract.Spec)
	if err != nil {
		return nil, fmt.Errorf("load openapi spec: %w", err)
	}
	if err := spec.Validate(context.Background()); err != nil {
		return nil, fmt.Errorf("invalid openapi spec: %w", err)
	}
	pw := problemWriter{log: opts.Logger}

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	if opts.TrustProxyHeaders {
		r.Use(middleware.RealIP)
	}
	r.Use(requestLogger(opts.Logger))
	r.Use(recoverer(pw))
	r.Use(clientInfoMiddleware)
	r.Use(securityHeaders)
	if len(opts.CORSOrigins) > 0 {
		r.Use(cors(opts.CORSOrigins))
	}
	r.Use(middleware.Timeout(60 * time.Second))

	r.NotFound(func(w http.ResponseWriter, r *http.Request) { pw.write(w, r, apperr.NotFound("no such endpoint")) })
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		writeProblem(w, &apperr.Error{Status: http.StatusMethodNotAllowed, Title: http.StatusText(http.StatusMethodNotAllowed)})
	})

	r.Get(BasePath+"/openapi.yaml", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		_, _ = w.Write(apicontract.Spec)
	})

	validator := oapimw.OapiRequestValidatorWithOptions(spec, &oapimw.Options{
		Prefix:               BasePath,
		DoNotValidateServers: true,
		Options:              openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc, MultiError: true},
		ErrorHandlerWithOpts: func(_ context.Context, err error, w http.ResponseWriter, r *http.Request, o oapimw.ErrorHandlerOpts) {
			writeProblem(w, validationError(err, o.StatusCode))
		},
	})

	strict := apigen.NewStrictHandlerWithOptions(srv, nil, apigen.StrictHTTPServerOptions{
		RequestErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			pw.write(w, r, apperr.BadRequest(err.Error()))
		},
		ResponseErrorHandlerFunc: pw.write,
	})

	r.Group(func(r chi.Router) {
		r.Use(srv.Auth.Middleware(publicRoutes, pw.write))
		r.Use(srv.renewSessionCookie)
		r.Use(validator)
		apigen.HandlerWithOptions(strict, apigen.ChiServerOptions{
			BaseURL:    BasePath,
			BaseRouter: r,
			ErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
				pw.write(w, r, apperr.BadRequest(err.Error()))
			},
		})
	})
	return r, nil
}

type clientInfoKey struct{}

func clientInfoMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := r.RemoteAddr
		if host, _, err := net.SplitHostPort(ip); err == nil {
			ip = host
		}
		ci := auth.ClientInfo{IP: ip, UserAgent: r.UserAgent()}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), clientInfoKey{}, ci)))
	})
}

func clientInfoFrom(ctx context.Context) auth.ClientInfo {
	ci, _ := ctx.Value(clientInfoKey{}).(auth.ClientInfo)
	return ci
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func cors(origins []string) func(http.Handler) http.Handler {
	allowed := map[string]bool{}
	for _, o := range origins {
		allowed[o] = true
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin != "" && allowed[origin] {
				h := w.Header()
				h.Set("Access-Control-Allow-Origin", origin)
				h.Set("Access-Control-Allow-Credentials", "true")
				h.Set("Vary", "Origin")
				if r.Method == http.MethodOptions {
					h.Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
					h.Set("Access-Control-Allow-Headers", "Content-Type, Authorization, "+auth.CSRFHeader)
					h.Set("Access-Control-Max-Age", "600")
					w.WriteHeader(http.StatusNoContent)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

func requestLogger(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)
			log.InfoContext(r.Context(), "http request",
				"method", r.Method, "path", r.URL.Path, "status", ww.Status(),
				"bytes", ww.BytesWritten(), "duration_ms", time.Since(start).Milliseconds(),
				"request_id", middleware.GetReqID(r.Context()))
		})
	}
}

func recoverer(pw problemWriter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					if rec == http.ErrAbortHandler {
						panic(rec)
					}
					pw.write(w, r, fmt.Errorf("panic: %v", rec))
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}
