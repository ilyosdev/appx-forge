package agent

import (
	"context"
	"io"
	"net/http"
	"strconv"
)

// logsSandboxResolver resolves a sandbox ID to a container ID on this node.
// Defined consumer-side (like snapshotProvider) so tests can fake it without
// constructing a full CommandExecutor. The concrete implementation is
// CommandExecutor.ResolveContainerID, which goes through resolveSandbox — the
// in-memory map with a lazy docker-label fallback — so a sandbox created
// before the agent's last restart still resolves (it would otherwise 404).
type logsSandboxResolver interface {
	ResolveContainerID(sandboxID string) (string, bool)
}

// logsReader retrieves container logs. Backed by docker.Client.GetLogs, which
// uses the Docker `container logs` API — that works on STOPPED containers too,
// so a slept/idle sandbox returns its captured output rather than "unreachable".
type logsReader interface {
	GetLogs(ctx context.Context, containerID string, tail int, follow bool) (io.ReadCloser, error)
}

// logsHandler answers GET /sandboxes/{id}/logs — the agent endpoint the
// control plane's log proxy forwards to (control/internal/api/logs.go). The
// route was never registered on the agent's HTTP mux, so every Logs-pane
// request 404'd at the agent and surfaced in the UI as "Container unreachable".
//
// Returns:
//   - 400 when the path lacks {id}
//   - 404 when the sandbox is not hosted on this node
//   - 502 when the Docker daemon refuses the log read
//   - 200 + text/plain log body otherwise (incl. for stopped containers)
type logsHandler struct {
	resolver logsSandboxResolver
	reader   logsReader
}

func newLogsHandler(resolver logsSandboxResolver, reader logsReader) *logsHandler {
	return &logsHandler{resolver: resolver, reader: reader}
}

func (h *logsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	sandboxID := r.PathValue("id")
	if sandboxID == "" {
		http.Error(w, "missing sandbox id", http.StatusBadRequest)
		return
	}

	containerID, ok := h.resolver.ResolveContainerID(sandboxID)
	if !ok {
		http.Error(w, "sandbox not found on this node", http.StatusNotFound)
		return
	}

	// tail defaults to "all" (0); follow is forced off — the control proxy is a
	// one-shot request/response, and a streaming reader would hold the proxy
	// open until the 60s control timeout.
	tail := 0
	if t := r.URL.Query().Get("tail"); t != "" {
		if parsed, err := strconv.Atoi(t); err == nil && parsed > 0 {
			tail = parsed
		}
	}

	reader, err := h.reader.GetLogs(r.Context(), containerID, tail, false)
	if err != nil {
		http.Error(w, "failed to read container logs: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer reader.Close()

	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, reader)
}
