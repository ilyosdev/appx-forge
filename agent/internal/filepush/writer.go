// Package filepush implements the file push endpoint for the agent.
// It handles receiving files from SDK/CLI clients (via control plane redirect)
// and writing them to sandbox bind-mount directories.
package filepush

import (
	"archive/tar"
	"compress/gzip"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// FileEntry represents a single file operation in a file push request.
type FileEntry struct {
	Path    string `json:"path"`
	Content string `json:"content"` // base64-encoded
	Delete  bool   `json:"delete"`
}

// WriteResult summarizes the outcome of a file push operation.
type WriteResult struct {
	Written []string `json:"written"`
	Failed  []string `json:"failed"`
}

// WriteFiles writes, creates, or deletes files in codeDir based on the given entries.
// Each file path is validated to prevent path traversal. Invalid paths are added to
// the Failed list. WriteFiles never returns an error -- partial failures are reported
// in the result.
func WriteFiles(codeDir string, files []FileEntry) WriteResult {
	result := WriteResult{
		Written: []string{},
		Failed:  []string{},
	}

	for _, f := range files {
		if !isValidPath(f.Path) {
			result.Failed = append(result.Failed, f.Path)
			continue
		}

		fullPath := filepath.Join(codeDir, f.Path)

		if f.Delete {
			if err := os.Remove(fullPath); err != nil {
				result.Failed = append(result.Failed, f.Path)
			} else {
				result.Written = append(result.Written, f.Path)
			}
			continue
		}

		data, err := base64.StdEncoding.DecodeString(f.Content)
		if err != nil {
			result.Failed = append(result.Failed, f.Path)
			continue
		}

		dir := filepath.Dir(fullPath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			result.Failed = append(result.Failed, f.Path)
			continue
		}

		if err := os.WriteFile(fullPath, data, 0644); err != nil {
			result.Failed = append(result.Failed, f.Path)
			continue
		}

		result.Written = append(result.Written, f.Path)
	}

	// Nudge Metro to re-bundle even when inotify misses new files in nested
	// app/** subdirs (Watchman is disabled in the baked Metro config).
	if len(result.Written) > 0 {
		triggerMetroRebuild(codeDir)
	}

	return result
}

// WriteTar extracts a tar.gz archive from r into codeDir.
// Symlinks in the archive are ignored for security. Path traversal
// entries (containing ".." or starting with "/") are skipped and added
// to the Failed list.
func WriteTar(codeDir string, r io.Reader) (WriteResult, error) {
	result := WriteResult{
		Written: []string{},
		Failed:  []string{},
	}

	gz, err := gzip.NewReader(r)
	if err != nil {
		return result, fmt.Errorf("opening gzip reader: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return result, fmt.Errorf("reading tar entry: %w", err)
		}

		// Skip non-regular files (symlinks, directories, etc.)
		if hdr.Typeflag != tar.TypeReg {
			continue
		}

		name := filepath.Clean(hdr.Name)

		if !isValidPath(name) {
			result.Failed = append(result.Failed, name)
			continue
		}

		fullPath := filepath.Join(codeDir, name)

		dir := filepath.Dir(fullPath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			result.Failed = append(result.Failed, name)
			continue
		}

		f, err := os.Create(fullPath)
		if err != nil {
			result.Failed = append(result.Failed, name)
			continue
		}

		if _, err := io.Copy(f, tr); err != nil {
			f.Close()
			result.Failed = append(result.Failed, name)
			continue
		}
		f.Close()

		result.Written = append(result.Written, name)
	}

	// Nudge Metro to re-bundle (see WriteFiles).
	if len(result.Written) > 0 {
		triggerMetroRebuild(codeDir)
	}

	return result, nil
}

// watchedRootCandidates are files that Metro reliably keeps in its watched
// module graph for an Expo Router project. Bumping the mtime of any one of
// them makes Metro's file watcher fire a change event and re-crawl the
// project, which is the only dependable way to pick up brand-new files that
// were written into not-yet-watched nested app/** subdirectories.
//
// entry.js is force-refreshed by the sandbox entrypoint on every boot and is
// the literal Metro entry point, so it is always present and always in the
// graph; the app/ router files are next-best fallbacks. Ordered most- to
// least-reliable.
var watchedRootCandidates = []string{
	"entry.js",
	"app/_layout.tsx",
	"app/index.tsx",
	"app.json",
}

// triggerMetroRebuild forces Metro to detect that code changed, independent of
// inotify catching writes in nested directories. It bumps the modification
// time of the first existing always-watched root file (see watchedRootCandidates),
// which Metro's watcher observes and turns into a re-bundle.
//
// Best-effort and silent: a failure here only means the preview may serve a
// stale bundle until the next push, so it must never affect the push result or
// surface an error. Cheap (one stat + one chtimes), idempotent (sets mtime to
// "now"), and applies to all pushed files regardless of their location because
// Metro re-crawls the whole project on the change event.
func triggerMetroRebuild(codeDir string) {
	now := time.Now()
	for _, rel := range watchedRootCandidates {
		full := filepath.Join(codeDir, rel)
		if _, err := os.Stat(full); err != nil {
			continue
		}
		// Update both atime and mtime to now. Ignore errors (read-only file,
		// race with a concurrent push) — this is purely advisory.
		_ = os.Chtimes(full, now, now)
		return
	}
}

// protectedFiles are paths the control plane / SDK may never write.
// Metro config is baked into the sandbox image and enforced at container start;
// allowing a push to overwrite it would silently undo B3 memory tuning
// (see .planning/phases/19-shared-bundler/19-B3-SPEC.md, section B3-3).
var protectedFiles = map[string]struct{}{
	"metro.config.js":  {},
	"metro.config.ts":  {},
	"metro.config.cjs": {},
	"metro.config.mjs": {},
}

// isValidPath returns false for paths that could escape the code directory
// (containing ".." or starting with "/") or that target a protected file.
func isValidPath(p string) bool {
	if p == "" {
		return false
	}
	if filepath.IsAbs(p) {
		return false
	}
	clean := filepath.ToSlash(filepath.Clean(p))
	for _, part := range strings.Split(clean, "/") {
		if part == ".." {
			return false
		}
	}
	if _, blocked := protectedFiles[clean]; blocked {
		return false
	}
	return true
}
