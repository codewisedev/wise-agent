package auth

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

const (
	SessionCookieName = "wise_session"
	CSRFHeader        = "X-Wise-CSRF"
)

// ErrorWriter renders an error as an HTTP response.
type ErrorWriter func(w http.ResponseWriter, r *http.Request, err error)

// Middleware authenticates requests. Paths in public skip authentication.
// Cookie-authenticated unsafe requests must carry a matching CSRF header.
func (s *Service) Middleware(public map[string]bool, writeErr ErrorWriter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if public[r.Method+" "+r.URL.Path] {
				next.ServeHTTP(w, r)
				return
			}
			p, err := s.authenticate(r)
			if err != nil {
				writeErr(w, r, err)
				return
			}
			if p.Method == MethodSession && !isSafeMethod(r.Method) {
				got := r.Header.Get(CSRFHeader)
				if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(p.CSRFToken)) != 1 {
					writeErr(w, r, errCSRF)
					return
				}
			}
			next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), p)))
		})
	}
}

func (s *Service) authenticate(r *http.Request) (Principal, error) {
	if h := r.Header.Get("Authorization"); h != "" {
		scheme, token, ok := strings.Cut(h, " ")
		if !ok || !strings.EqualFold(scheme, "Bearer") || token == "" {
			return Principal{}, errUnauthenticated
		}
		return s.AuthenticateAPIToken(r.Context(), strings.TrimSpace(token))
	}
	if c, err := r.Cookie(SessionCookieName); err == nil && c.Value != "" {
		return s.AuthenticateSession(r.Context(), c.Value)
	}
	return Principal{}, errUnauthenticated
}

func isSafeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}
