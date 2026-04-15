package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBearerAuth_ValidToken(t *testing.T) {
	token := "test-secret-token"
	handler := BearerAuth(token)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if rec.Body.String() != "ok" {
		t.Errorf("body = %q, want %q", rec.Body.String(), "ok")
	}
}

func TestBearerAuth_WrongToken(t *testing.T) {
	handler := BearerAuth("correct-token")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be called with wrong token")
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Errorf("Content-Type = %q, want %q", ct, "application/problem+json")
	}

	var problem ProblemDetail
	if err := json.NewDecoder(rec.Body).Decode(&problem); err != nil {
		t.Fatalf("decode problem: %v", err)
	}
	if problem.Type != "urn:forge:error:unauthorized" {
		t.Errorf("problem.Type = %q, want %q", problem.Type, "urn:forge:error:unauthorized")
	}
	if problem.Status != http.StatusUnauthorized {
		t.Errorf("problem.Status = %d, want %d", problem.Status, http.StatusUnauthorized)
	}
}

func TestBearerAuth_MissingHeader(t *testing.T) {
	handler := BearerAuth("my-token")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be called with missing header")
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	// No Authorization header
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Errorf("Content-Type = %q, want %q", ct, "application/problem+json")
	}
}

func TestBearerAuth_BasicScheme(t *testing.T) {
	handler := BearerAuth("my-token")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be called with Basic scheme")
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Errorf("Content-Type = %q, want %q", ct, "application/problem+json")
	}
}

func TestProblemDetail_WriteProblem(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteProblem(rec, http.StatusNotFound, "urn:forge:error:not-found", "Not Found", "sandbox xyz not found")

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Errorf("Content-Type = %q, want %q", ct, "application/problem+json")
	}

	var problem ProblemDetail
	if err := json.NewDecoder(rec.Body).Decode(&problem); err != nil {
		t.Fatalf("decode problem: %v", err)
	}
	if problem.Type != "urn:forge:error:not-found" {
		t.Errorf("Type = %q, want %q", problem.Type, "urn:forge:error:not-found")
	}
	if problem.Title != "Not Found" {
		t.Errorf("Title = %q, want %q", problem.Title, "Not Found")
	}
	if problem.Status != http.StatusNotFound {
		t.Errorf("Status = %d, want %d", problem.Status, http.StatusNotFound)
	}
	if problem.Detail != "sandbox xyz not found" {
		t.Errorf("Detail = %q, want %q", problem.Detail, "sandbox xyz not found")
	}
}

func TestProblemDetail_Helpers(t *testing.T) {
	tests := []struct {
		name       string
		fn         func(http.ResponseWriter, string)
		detail     string
		wantStatus int
		wantType   string
	}{
		{
			name:       "NotFound",
			fn:         NotFound,
			detail:     "resource not found",
			wantStatus: http.StatusNotFound,
			wantType:   "urn:forge:error:not-found",
		},
		{
			name:       "Conflict",
			fn:         Conflict,
			detail:     "already exists",
			wantStatus: http.StatusConflict,
			wantType:   "urn:forge:error:conflict",
		},
		{
			name:       "BadRequest",
			fn:         BadRequest,
			detail:     "invalid input",
			wantStatus: http.StatusBadRequest,
			wantType:   "urn:forge:error:bad-request",
		},
		{
			name:       "ServiceUnavailable",
			fn:         ServiceUnavailable,
			detail:     "db unreachable",
			wantStatus: http.StatusServiceUnavailable,
			wantType:   "urn:forge:error:service-unavailable",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			tt.fn(rec, tt.detail)

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}

			var problem ProblemDetail
			if err := json.NewDecoder(rec.Body).Decode(&problem); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if problem.Type != tt.wantType {
				t.Errorf("Type = %q, want %q", problem.Type, tt.wantType)
			}
			if problem.Detail != tt.detail {
				t.Errorf("Detail = %q, want %q", problem.Detail, tt.detail)
			}
		})
	}
}

func TestUnauthorized_Helper(t *testing.T) {
	rec := httptest.NewRecorder()
	Unauthorized(rec)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}

	var problem ProblemDetail
	if err := json.NewDecoder(rec.Body).Decode(&problem); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if problem.Type != "urn:forge:error:unauthorized" {
		t.Errorf("Type = %q, want %q", problem.Type, "urn:forge:error:unauthorized")
	}
}
