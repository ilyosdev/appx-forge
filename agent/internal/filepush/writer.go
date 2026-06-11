// Package filepush implements the file push endpoint for the agent.
// It handles receiving files from SDK/CLI clients (via control plane redirect)
// and writing them to sandbox bind-mount directories.
package filepush

import (
	"archive/tar"
	"bytes"
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
//
// Identical-byte writes are skipped: if the on-disk file already holds the exact
// bytes being pushed, the write (and its mtime bump) is elided. The path is still
// reported in Written — the file IS present with the requested content, so callers
// that count Written for accounting see no change — but Metro is NOT nudged unless
// at least one file's bytes (or set of files) actually changed. This is the wake
// reflush fix: a sleep→wake cycle re-pushes the whole tree, and without the compare
// every chtimes/write bumped mtimes and forced Metro to cold-rebundle TreeFS.
func WriteFiles(codeDir string, files []FileEntry) WriteResult {
	result := WriteResult{
		Written: []string{},
		Failed:  []string{},
	}

	// Count of operations that actually mutated the filesystem (a real write of
	// changed bytes, or a delete). Identical-byte no-ops do NOT count, so an
	// all-identical reflush triggers no Metro rebuild.
	changed := 0

	for _, f := range files {
		if !isValidPath(f.Path) {
			result.Failed = append(result.Failed, f.Path)
			continue
		}

		fullPath := filepath.Join(codeDir, f.Path)

		if f.Delete {
			if err := os.Remove(fullPath); err != nil {
				// A delete of an already-absent file is a no-op, not a failure:
				// the desired end-state (file gone) holds. Report it as written
				// but do not count it as a change so it can't trigger a rebuild.
				if os.IsNotExist(err) {
					result.Written = append(result.Written, f.Path)
				} else {
					result.Failed = append(result.Failed, f.Path)
				}
			} else {
				result.Written = append(result.Written, f.Path)
				changed++
			}
			continue
		}

		data, err := base64.StdEncoding.DecodeString(f.Content)
		if err != nil {
			result.Failed = append(result.Failed, f.Path)
			continue
		}

		// Skip the write when the file already holds exactly these bytes. Avoids
		// the mtime bump that makes Metro's mtime-keyed TreeFS re-crawl on every
		// reflush (the dominant cost of sleep→wake). One read per file is cheap
		// next to a write + a Metro cold rebundle.
		if sameBytes(fullPath, data) {
			result.Written = append(result.Written, f.Path)
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
		changed++
	}

	// Nudge Metro to re-bundle even when inotify misses new files in nested
	// app/** subdirs (Watchman is disabled in the baked Metro config). Only when
	// something actually changed on disk — an all-identical reflush (the wake
	// case) leaves mtimes untouched and must not force a cold rebundle.
	if changed > 0 {
		triggerMetroRebuild(codeDir)
	}

	return result
}

// sameBytes reports whether the file at path already holds exactly want. Returns
// false on any read error (missing file, permission) so the caller falls through
// to a normal write — the compare is a fast-path optimization, never a gate.
//
// Size is checked first (one stat) to avoid reading large files that obviously
// differ; only same-size files are read and byte-compared.
func sameBytes(path string, want []byte) bool {
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() || fi.Size() != int64(len(want)) {
		return false
	}
	have, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return bytes.Equal(have, want)
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

	// See WriteFiles: only a real on-disk mutation may trigger a Metro rebuild.
	changed := 0

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

		// Buffer the entry so we can compare against the on-disk bytes before
		// writing. Tar entries are individual source files (small); buffering
		// is cheaper than the mtime-bump cold rebundle a blind rewrite causes.
		data, err := io.ReadAll(tr)
		if err != nil {
			result.Failed = append(result.Failed, name)
			continue
		}

		// Identical-byte skip: file already holds these bytes → no write, no
		// mtime bump (the wake reflush fix; see WriteFiles).
		if sameBytes(fullPath, data) {
			result.Written = append(result.Written, name)
			continue
		}

		dir := filepath.Dir(fullPath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			result.Failed = append(result.Failed, name)
			continue
		}

		if err := os.WriteFile(fullPath, data, 0644); err != nil {
			result.Failed = append(result.Failed, name)
			continue
		}

		result.Written = append(result.Written, name)
		changed++
	}

	// Nudge Metro to re-bundle (see WriteFiles) — only on a real change.
	if changed > 0 {
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
