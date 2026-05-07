package agent

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/appx/forge/agent/internal/docker"
)

// snapshotProvider is the interface containerExistsHandler needs.
// Defined here (consumer side) so tests can fake it without importing
// the docker package's concrete Snapshotter.
type snapshotProvider interface {
	Snapshot(ctx context.Context) ([]docker.ContainerSnapshot, error)
}

// containerExistsResponse is the shape returned by GET /v1/containers/{name}.
//
// Phase 30 — used by the control plane's SandboxFreshnessService when a
// sandbox row's verified_at is older than the freshness threshold. Returns
// 200 + Exists=true + metadata when present, 404 + Exists=false (with the
// echoed AppName) when absent, 500 on docker daemon error, 400 when the
// path lacks {name}.
type containerExistsResponse struct {
	Exists      bool   `json:"exists"`
	AppName     string `json:"app_name"`
	State       string `json:"state,omitempty"`
	HostPort    int    `json:"host_port,omitempty"`
	ContainerID string `json:"container_id,omitempty"`
}

// containerExistsHandler answers "does this container exist?" via a fresh
// Docker snapshot — never via cached state. The control plane consults
// this on stale verified_at reads (Phase 30 §3.1).
type containerExistsHandler struct {
	snap snapshotProvider
}

func newContainerExistsHandler(snap snapshotProvider) *containerExistsHandler {
	return &containerExistsHandler{snap: snap}
}

func (h *containerExistsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		http.Error(w, "missing app name", http.StatusBadRequest)
		return
	}

	containers, err := h.snap.Snapshot(r.Context())
	if err != nil {
		http.Error(w, "snapshot failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	for _, c := range containers {
		if c.AppName == name {
			writeJSON(w, http.StatusOK, containerExistsResponse{
				Exists:      true,
				AppName:     c.AppName,
				State:       c.State,
				HostPort:    c.HostPort,
				ContainerID: c.ContainerID,
			})
			return
		}
	}
	writeJSON(w, http.StatusNotFound, containerExistsResponse{Exists: false, AppName: name})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
