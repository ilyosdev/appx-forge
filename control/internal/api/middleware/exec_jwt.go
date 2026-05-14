// Package middleware contains chi-compatible HTTP middleware that is shared
// across handler groups. Lives in its own package so the api package can wire
// it on specific route groups without exporting more from api itself.
package middleware

import (
	"crypto/subtle"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
)

// jwtClaimSandboxID is the canonical claim key for the scoped exec token.
// Tokens with `sandbox_id == "<path-id>"` are accepted; mismatches return 403.
const jwtClaimSandboxID = "sandbox_id"

// ExecJWT returns a chi-compatible middleware that validates either a
// scoped exec JWT (via X-Exec-Token header) or, if absent, falls through
// so a chained Bearer middleware can authorize the request.
//
// Behavior:
//   - X-Exec-Token present + valid + sandbox_id claim matches path {id}:
//     allow. The Bearer middleware that wraps this can also be satisfied
//     by the original Bearer header — both go through.
//   - X-Exec-Token present but invalid (bad signature, expired, bad alg)
//     or claim mismatch: 401 / 403. We do NOT fall through to Bearer in
//     this case — an explicit bad token is a hard error, not "try the
//     next auth method".
//   - X-Exec-Token absent: pass-through. Bearer middleware (mounted in
//     the chain ahead of this one in routes.go) handles the request via
//     the global FORGE_API_TOKEN.
//
// secret is the shared HMAC secret used to sign exec JWTs. If secret is
// empty the middleware degrades to pass-through (callers using the
// header path will get 401 because no token can validate against an
// empty secret).
func ExecJWT(secret string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tokenStr := r.Header.Get("X-Exec-Token")
			if tokenStr == "" {
				// No scoped token — let the upstream Bearer middleware do its job.
				next.ServeHTTP(w, r)
				return
			}

			if secret == "" {
				http.Error(w, "exec token not accepted: server has no exec secret configured", http.StatusUnauthorized)
				return
			}

			token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (interface{}, error) {
				if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
					return nil, errors.New("unexpected signing method")
				}
				return []byte(secret), nil
			})
			if err != nil || token == nil || !token.Valid {
				http.Error(w, "invalid exec token", http.StatusUnauthorized)
				return
			}

			claims, ok := token.Claims.(jwt.MapClaims)
			if !ok {
				http.Error(w, "invalid exec token claims", http.StatusUnauthorized)
				return
			}

			claimedSandboxIDRaw, exists := claims[jwtClaimSandboxID]
			if !exists {
				http.Error(w, "missing sandbox_id claim", http.StatusUnauthorized)
				return
			}
			claimedSandboxID, ok := claimedSandboxIDRaw.(string)
			if !ok || claimedSandboxID == "" {
				http.Error(w, "invalid sandbox_id claim", http.StatusUnauthorized)
				return
			}
			pathSandboxID := chi.URLParam(r, "id")
			if pathSandboxID == "" {
				http.Error(w, "sandbox_id claim missing", http.StatusForbidden)
				return
			}
			if subtle.ConstantTimeCompare([]byte(claimedSandboxID), []byte(pathSandboxID)) != 1 {
				http.Error(w, "sandbox_id claim mismatch", http.StatusForbidden)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
