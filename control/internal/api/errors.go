// Package api provides the HTTP server, middleware, and handlers for the control plane.
package api

import (
	"encoding/json"
	"net/http"
)

// ProblemDetail implements RFC 7807 Problem Details for HTTP APIs.
type ProblemDetail struct {
	Type     string `json:"type"`
	Title    string `json:"title"`
	Status   int    `json:"status"`
	Detail   string `json:"detail,omitempty"`
	Instance string `json:"instance,omitempty"`
}

// WriteProblem writes an RFC 7807 problem+json response with the given status code.
func WriteProblem(w http.ResponseWriter, status int, problemType, title, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(ProblemDetail{
		Type:   problemType,
		Title:  title,
		Status: status,
		Detail: detail,
	})
}

// NotFound writes a 404 problem response.
func NotFound(w http.ResponseWriter, detail string) {
	WriteProblem(w, http.StatusNotFound, "urn:forge:error:not-found", "Not Found", detail)
}

// Unauthorized writes a 401 problem response.
func Unauthorized(w http.ResponseWriter) {
	WriteProblem(w, http.StatusUnauthorized, "urn:forge:error:unauthorized", "Unauthorized", "missing or invalid bearer token")
}

// Conflict writes a 409 problem response.
func Conflict(w http.ResponseWriter, detail string) {
	WriteProblem(w, http.StatusConflict, "urn:forge:error:conflict", "Conflict", detail)
}

// BadRequest writes a 400 problem response.
func BadRequest(w http.ResponseWriter, detail string) {
	WriteProblem(w, http.StatusBadRequest, "urn:forge:error:bad-request", "Bad Request", detail)
}

// ServiceUnavailable writes a 503 problem response.
func ServiceUnavailable(w http.ResponseWriter, detail string) {
	WriteProblem(w, http.StatusServiceUnavailable, "urn:forge:error:service-unavailable", "Service Unavailable", detail)
}
