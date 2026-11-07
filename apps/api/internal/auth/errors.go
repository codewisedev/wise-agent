package auth

import "wise/apps/api/internal/apperr"

var (
	errUnauthenticated = apperr.Unauthorized("authentication required")
	errCSRF            = apperr.Forbidden("missing or invalid CSRF token")
)
