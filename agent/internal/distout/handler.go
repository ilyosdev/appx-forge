// Package distout serves the agent-side dist-out endpoint: it streams a
// sandbox's built `dist/` directory (the `expo export` output) out as a
// gzip-compressed tar so the backend can publish it to R2 for static preview
// (ADR-0003). It is the read-out twin of the filepush write-in handler and
// shares the same HMAC-signed-URL auth scheme.
//
// SECURITY: this endpoint is deliberately scoped to `{codeDir}/dist` ONLY. It
// resolves symlinks and refuses to stream anything that escapes the sandbox
// code tree, skips symlinks during the walk, and caps the total bytes. It is a
// publish primitive — NOT a general file-read/exfiltration endpoint.
package distout

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/appx/forge/shared-go/auth"
)

// maxDistBytes caps the cumulative uncompressed bytes streamed out of dist/ — a
// safety bound so the read-out primitive cannot be coaxed into exfiltrating an
// unbounded amount of data. A var (not const) so a test can shrink it. A real
// expo-export web/native dist is ~2.6-3.1MB; 64MB is generous headroom.
var maxDistBytes int64 = 64 * 1024 * 1024

// codeDirResolver resolves a sandbox ID to its on-disk code directory. The
// agent's *CommandExecutor satisfies this (the same method filepush uses);
// accepted as an interface so a test can stub it.
type codeDirResolver interface {
	CodeDir(sandboxID string) (string, error)
}

// Handler serves GET /v1/sandboxes/{id}/dist.
type Handler struct {
	hmacSecret []byte
	resolver   codeDirResolver
	logger     *slog.Logger
}

// NewHandler creates a dist-out Handler. If logger is nil a no-op logger is used.
func NewHandler(hmacSecret []byte, resolver codeDirResolver, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Handler{hmacSecret: hmacSecret, resolver: resolver, logger: logger}
}

// ServeHTTP validates the HMAC-signed URL, resolves the sandbox's dist/ dir,
// guards against escaping the code tree, and streams dist/ as gzip-tar.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Reconstruct the full URL for HMAC verification (mirrors filepush).
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	fullURL := fmt.Sprintf("%s://%s%s", scheme, r.Host, r.RequestURI)
	if _, err := auth.VerifyURL(fullURL, h.hmacSecret); err != nil {
		h.logger.Warn("dist-out HMAC verification failed", "error", err, "url", r.RequestURI)
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}

	sandboxID := extractSandboxID(r.URL.Path)
	if sandboxID == "" {
		http.Error(w, "invalid URL path: cannot extract sandbox ID", http.StatusBadRequest)
		return
	}

	// Build-scoped fetch: ?build=<id> resolves the build's SNAPSHOT code dir
	// (CodeDir(buildID) -> .../builds/<id>/code) instead of the live sandbox's,
	// so the isolated cold export's dist/ is streamed and the live sandbox's
	// dist/ is never produced or touched. The build param is part of the
	// HMAC-signed string (it is in r.RequestURI), so VerifyURL above already
	// covered it. Falls back to the sandbox ID when absent.
	resolveID := sandboxID
	if buildID := r.URL.Query().Get("build"); buildID != "" {
		resolveID = buildID
	}

	codeDir, err := h.resolver.CodeDir(resolveID)
	if err != nil {
		h.logger.Info("dist-out target not found", "resolve_id", resolveID, "error", err)
		http.Error(w, "sandbox not found on this node", http.StatusNotFound)
		return
	}

	distDir := filepath.Join(codeDir, "dist")

	// Scope-guard: resolve symlinks on BOTH the code dir and the dist dir and
	// require dist to be physically inside code. Defends a symlinked dist/ (or a
	// relative `..`) from escaping the sandbox tree — the security crux.
	realCode, err := filepath.EvalSymlinks(codeDir)
	if err != nil {
		http.Error(w, "sandbox code directory unavailable", http.StatusNotFound)
		return
	}
	realDist, err := filepath.EvalSymlinks(distDir)
	if err != nil {
		// No dist/ yet (never exported, or the export failed) → 404, not 500.
		h.logger.Info("dist-out: no dist directory", "sandbox_id", sandboxID, "dist", distDir)
		http.Error(w, "no dist build found for sandbox", http.StatusNotFound)
		return
	}
	if !isWithin(realCode, realDist) {
		h.logger.Warn("dist-out: dist escapes code dir — refusing", "sandbox_id", sandboxID, "code", realCode, "dist", realDist)
		http.Error(w, "dist directory outside sandbox", http.StatusForbidden)
		return
	}

	// Status + headers BEFORE the walk (mirrors logs.go): a mid-stream error can
	// no longer flip the status; the client detects truncation via a gunzip/untar
	// failure and treats it as a failed build.
	w.Header().Set("Content-Type", "application/gzip")
	w.WriteHeader(http.StatusOK)

	if err := streamDistTarGz(w, realDist, maxDistBytes); err != nil {
		h.logger.Error("dist-out streaming failed", "sandbox_id", sandboxID, "error", err)
	}
}

// streamDistTarGz writes `root` as a gzip-compressed tar to w. Only regular
// files are included (symlinks are skipped — filepath.Walk uses Lstat); entry
// names are kept relative to root; cumulative uncompressed size is capped.
func streamDistTarGz(w io.Writer, root string, limit int64) error {
	gz := gzip.NewWriter(w)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()

	var total int64
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		// Walk uses Lstat → a symlink reports as a symlink here; never follow it.
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		if info.IsDir() || !info.Mode().IsRegular() {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		if rel == ".." || strings.HasPrefix(rel, "../") {
			return nil
		}
		total += info.Size()
		if total > limit {
			return fmt.Errorf("dist exceeds %d byte cap", limit)
		}
		if err := tw.WriteHeader(&tar.Header{
			Name:    rel,
			Mode:    0o644,
			Size:    info.Size(),
			ModTime: info.ModTime(),
		}); err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.CopyN(tw, f, info.Size())
		f.Close()
		return copyErr
	})
}

// isWithin reports whether child equals parent or is nested under it. Both must
// be absolute, symlink-resolved paths.
func isWithin(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// extractSandboxID extracts the sandbox ID from /v1/sandboxes/{id}/dist.
func extractSandboxID(path string) string {
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(parts) < 4 {
		return ""
	}
	if parts[0] == "v1" && parts[1] == "sandboxes" && parts[3] == "dist" {
		return parts[2]
	}
	return ""
}
