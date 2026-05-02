package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/appx/forge/agent/internal/docker"
)

type fakeSnapshotProvider struct {
	containers []docker.ContainerSnapshot
	err        error
}

func (f *fakeSnapshotProvider) Snapshot(ctx context.Context) ([]docker.ContainerSnapshot, error) {
	return f.containers, f.err
}

func TestContainerExists_ReturnsTrueWhenPresent(t *testing.T) {
	snap := &fakeSnapshotProvider{
		containers: []docker.ContainerSnapshot{
			{AppName: "pool-X", State: "running", HostPort: 8081, ContainerID: "abc"},
		},
	}
	handler := newContainerExistsHandler(snap)

	req := httptest.NewRequest("GET", "/v1/containers/pool-X", nil)
	req.SetPathValue("name", "pool-X")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	var body containerExistsResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.Exists {
		t.Errorf("expected exists=true")
	}
	if body.State != "running" {
		t.Errorf("expected state=running, got %s", body.State)
	}
	if body.HostPort != 8081 {
		t.Errorf("expected hostPort=8081, got %d", body.HostPort)
	}
}

func TestContainerExists_Returns404WhenAbsent(t *testing.T) {
	snap := &fakeSnapshotProvider{containers: []docker.ContainerSnapshot{}}
	handler := newContainerExistsHandler(snap)

	req := httptest.NewRequest("GET", "/v1/containers/pool-Z", nil)
	req.SetPathValue("name", "pool-Z")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
	var body containerExistsResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Exists {
		t.Errorf("expected exists=false")
	}
	if body.AppName != "pool-Z" {
		t.Errorf("expected appName echoed in 404, got %s", body.AppName)
	}
}

func TestContainerExists_Returns500WhenSnapshotFails(t *testing.T) {
	snap := &fakeSnapshotProvider{err: errors.New("docker daemon down")}
	handler := newContainerExistsHandler(snap)

	req := httptest.NewRequest("GET", "/v1/containers/x", nil)
	req.SetPathValue("name", "x")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
}

func TestContainerExists_Returns400WhenNameMissing(t *testing.T) {
	snap := &fakeSnapshotProvider{}
	handler := newContainerExistsHandler(snap)

	req := httptest.NewRequest("GET", "/v1/containers/", nil)
	// PathValue("name") returns "" when not set
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}
