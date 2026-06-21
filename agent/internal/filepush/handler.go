package filepush

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/appx/forge/shared-go/auth"
)

// SandboxResolver resolves a sandbox ID to the code directory path on this node.
type SandboxResolver interface {
	// CodeDir returns the code directory for the given sandbox, or an error
	// if the sandbox is not hosted on this node.
	CodeDir(sandboxID string) (string, error)
}

// SandboxRestarter restarts the container backing a sandbox so its Metro dev
// server re-crawls and picks up newly-pushed files. The agent's CommandExecutor
// (the SandboxResolver passed in production) also implements this, so the handler
// discovers it with a type assertion — no constructor change. When the resolver
// does NOT implement it (test stubs / legacy wiring), a structural push falls
// back to the writer's mtime-nudge alone, which is the pre-fix behavior.
type SandboxRestarter interface {
	// RestartSandbox restarts the sandbox's container synchronously and returns
	// an error if the sandbox is unknown or the restart fails.
	RestartSandbox(sandboxID string) error
}

// Handler is the HTTP handler for the file push endpoint.
// It validates HMAC signed URLs and writes files to sandbox bind-mount directories.
type Handler struct {
	hmacSecret []byte
	resolver   SandboxResolver
	logger     *slog.Logger
}

// NewHandler creates a new file push Handler.
// If logger is nil, a default no-op logger is used.
func NewHandler(hmacSecret []byte, resolver SandboxResolver, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Handler{
		hmacSecret: hmacSecret,
		resolver:   resolver,
		logger:     logger,
	}
}

// filePushRequest is the JSON request body for JSON file pushes.
//
// FullSync + Manifest carry the W4 prune contract: when FullSync is true the
// backend has sent the COMPLETE list of project paths (Manifest), so the agent
// removes any on-disk file not in it after writing (see WriteFilesFull). Both
// fields are optional — an old backend omits them and the push behaves exactly
// as before (no deletions).
type filePushRequest struct {
	Files    []FileEntry `json:"files"`
	FullSync bool        `json:"fullSync,omitempty"`
	Manifest []string    `json:"manifest,omitempty"`
}

// errorResponse writes a JSON error to the response writer.
func errorResponse(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// ServeHTTP handles file push requests.
// It validates the HMAC signature, extracts the sandbox ID from the URL path,
// and writes files using either JSON or tar format based on Content-Type.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Reconstruct the full URL for HMAC verification.
	// httptest.NewServer uses http:// so we reconstruct from the request.
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	fullURL := fmt.Sprintf("%s://%s%s", scheme, r.Host, r.RequestURI)

	// Verify HMAC signature and expiry
	_, err := auth.VerifyURL(fullURL, h.hmacSecret)
	if err != nil {
		h.logger.Warn("HMAC verification failed", "error", err, "url", r.RequestURI)
		errorResponse(w, http.StatusForbidden, err.Error())
		return
	}

	// Extract sandbox ID from URL path: /v1/sandboxes/{id}/files
	sandboxID := extractSandboxID(r.URL.Path)
	if sandboxID == "" {
		errorResponse(w, http.StatusBadRequest, "invalid URL path: cannot extract sandbox ID")
		return
	}

	// Resolve sandbox directory
	codeDir, err := h.resolver.CodeDir(sandboxID)
	if err != nil {
		h.logger.Info("sandbox not found", "sandbox_id", sandboxID, "error", err)
		errorResponse(w, http.StatusNotFound, "sandbox not found on this node")
		return
	}

	// Dispatch by Content-Type
	contentType := r.Header.Get("Content-Type")

	switch {
	case strings.HasPrefix(contentType, "application/json"):
		h.handleJSON(w, r, sandboxID, codeDir)
	case strings.HasPrefix(contentType, "application/x-tar"):
		h.handleTar(w, r, sandboxID, codeDir)
	default:
		errorResponse(w, http.StatusBadRequest, fmt.Sprintf("unsupported content type: %s", contentType))
	}
}

// handleJSON parses a JSON file push request and writes files.
func (h *Handler) handleJSON(w http.ResponseWriter, r *http.Request, sandboxID, codeDir string) {
	if r.Body == nil || r.ContentLength == 0 {
		errorResponse(w, http.StatusBadRequest, "empty request body")
		return
	}

	var req filePushRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.logger.Warn("failed to decode JSON body", "error", err)
		errorResponse(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	var result WriteResult
	if req.FullSync {
		result = WriteFilesFull(codeDir, req.Files, req.Manifest)
	} else {
		result = WriteFiles(codeDir, req.Files)
	}

	// Restart Metro for a fresh crawl when the push was structural. Debounced —
	// coalesces a push burst into one restart (see maybeRestart / RestartSandbox).
	h.maybeRestart(sandboxID, result)

	h.logger.Info("file push complete",
		"written", len(result.Written),
		"failed", len(result.Failed),
		"deleted", len(result.Deleted),
		"full_sync", req.FullSync,
		"structural", result.Structural,
	)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(result)
}

// maybeRestart schedules a Metro restart when the push changed the file SET (new
// path or delete) so Metro re-crawls and sees it — the running dev server
// (Watchman off) does not. A content-only push is left to the writer's mtime-nudge
// (fast HMR). The restart is DEBOUNCED inside RestartSandbox: a burst of structural
// pushes (a gen's clustered syncs) coalesces into one restart, so the container is
// not thrash-cycled. No-op when the resolver does not implement SandboxRestarter
// (test stubs / legacy wiring) — the nudge then carries the pre-fix behavior.
// RestartSandbox returns immediately (scheduling never fails); the eventual
// restart is best-effort and logged fail-open.
func (h *Handler) maybeRestart(sandboxID string, result WriteResult) {
	if !result.Structural {
		return
	}
	restarter, ok := h.resolver.(SandboxRestarter)
	if !ok {
		return
	}
	if err := restarter.RestartSandbox(sandboxID); err != nil {
		h.logger.Warn("metro restart after structural push failed (preview may need a refresh)",
			"sandbox_id", sandboxID, "error", err)
		return
	}
	h.logger.Info("metro restarted after structural push", "sandbox_id", sandboxID)
}

// handleTar extracts a tar.gz archive and writes files.
func (h *Handler) handleTar(w http.ResponseWriter, r *http.Request, sandboxID, codeDir string) {
	if r.Body == nil || r.ContentLength == 0 {
		errorResponse(w, http.StatusBadRequest, "empty request body")
		return
	}

	result, err := WriteTar(codeDir, r.Body)
	if err != nil {
		h.logger.Error("tar extraction failed", "error", err)
		errorResponse(w, http.StatusInternalServerError, "tar extraction failed")
		return
	}

	// Fresh-crawl restart on a structural tar push (see maybeRestart).
	h.maybeRestart(sandboxID, result)

	h.logger.Info("tar push complete",
		"written", len(result.Written),
		"failed", len(result.Failed),
		"structural", result.Structural,
	)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(result)
}

// extractSandboxID extracts the sandbox ID from a URL path of the form
// /v1/sandboxes/{id}/files
func extractSandboxID(path string) string {
	// Trim leading slash and split
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	// Expected: ["v1", "sandboxes", "{id}", "files"]
	if len(parts) < 4 {
		return ""
	}
	if parts[0] == "v1" && parts[1] == "sandboxes" && parts[3] == "files" {
		return parts[2]
	}
	return ""
}
