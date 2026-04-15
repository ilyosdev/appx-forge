package api

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// BearerAuth returns a chi-compatible middleware that validates Bearer token
// authentication. Uses constant-time comparison to prevent timing attacks (T-03-01).
func BearerAuth(token string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			auth := r.Header.Get("Authorization")
			if auth == "" {
				Unauthorized(w)
				return
			}

			if !strings.HasPrefix(auth, "Bearer ") {
				Unauthorized(w)
				return
			}

			provided := strings.TrimPrefix(auth, "Bearer ")
			if subtle.ConstantTimeCompare([]byte(provided), []byte(token)) != 1 {
				Unauthorized(w)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
