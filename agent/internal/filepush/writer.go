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
	Deleted []string `json:"deleted,omitempty"`
}

// templateDir holds the readonly source seed the entrypoint copies into the
// code directory at boot (Dockerfile bakes app/ → /opt/template). Files that
// originate here (entry.js, app.json, app/index.tsx, …) are infrastructure,
// not project source, so a manifest prune must NEVER remove them. Overridable
// via FORGE_TEMPLATE_DIR for tests / non-standard images.
func templateDir() string {
	if d := os.Getenv("FORGE_TEMPLATE_DIR"); d != "" {
		return d
	}
	return "/opt/template"
}

// WriteFilesFull writes the given entries (see WriteFiles) and then prunes any
// stale file in codeDir that is NOT in manifest. It is the full-sync variant:
// the backend's syncFromRevision / syncFromDb paths send the COMPLETE list of
// project paths, so a file present on disk but absent from the manifest is a
// project file that was deleted at the source (e.g. a route renamed away) and
// must be removed — otherwise Metro keeps serving the ghost route after a
// container restart re-pushes the tree (W4: sync never deleted).
//
// Pruning is conservative (see pruneStale): only files inside codeDir, never a
// template seed, never under node_modules/.expo/.git or any hidden dir, never a
// protected metro.config.*. A deletion counts as a change so Metro is nudged.
func WriteFilesFull(codeDir string, files []FileEntry, manifest []string) WriteResult {
	result := WriteFiles(codeDir, files)
	deleted, changed := pruneStale(codeDir, manifest)
	result.Deleted = deleted
	// A prune deletion IS a change — Metro must re-crawl so the ghost route
	// disappears. WriteFiles only nudges on its own writes; an all-identical
	// reflush that nonetheless prunes a stale file would otherwise leave the
	// ghost in Metro's graph until the next genuine write.
	if changed {
		triggerMetroRebuild(codeDir)
	}
	return result
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

// prunedDirSkip names directory components a manifest prune never descends
// into: vendored deps, Metro/Expo caches, and VCS metadata. None of these are
// project source, none are in the manifest, and walking them would be both slow
// and dangerous (deleting node_modules breaks the symlinked shared deps).
var prunedDirSkip = map[string]struct{}{
	"node_modules": {},
	".expo":        {},
	".git":         {},
}

// pruneStale walks codeDir and deletes every regular file that is NOT in the
// manifest, returning the deleted relative paths and whether anything changed.
//
// A file survives the prune when it is any of:
//   - in the manifest (slash-normalised) — it is current project source;
//   - a template seed (present at the same relative path under templateDir) —
//     infrastructure the entrypoint owns (entry.js, app.json, app/index.tsx, …);
//   - a protected metro.config.* — baked, never project source;
//   - under a skipped dir (node_modules/.expo/.git) or any hidden (dot) dir.
//
// SAFETY: deletions only ever happen inside codeDir (filepath.Walk roots there;
// a relative path that escapes is refused). If templateDir can't be read, the
// prune is SKIPPED ENTIRELY (fail-open) — a missing template index could
// mis-classify a seed as stale and delete it. A delete error is logged-by-omission
// (the path simply isn't reported deleted) and never aborts the walk.
func pruneStale(codeDir string, manifest []string) (deleted []string, changed bool) {
	// Build the set of paths to keep. Manifest paths are slash-normalised and
	// leading-slash-stripped to match the on-disk relative form.
	keep := make(map[string]struct{}, len(manifest))
	for _, p := range manifest {
		clean := filepath.ToSlash(filepath.Clean(strings.TrimLeft(p, "/")))
		if clean == "." || clean == "" {
			continue
		}
		keep[clean] = struct{}{}
	}

	// Index the template seed when available. The template dir exists INSIDE
	// the sandbox image (/opt/template), not on the agent host — so in prod
	// this is usually unreadable and protection comes from the manifest
	// instead: the backend unions the template file list into the manifest.
	// With neither a template index NOR a manifest we cannot tell a seed from
	// a stale file — skip pruning rather than risk deleting infrastructure.
	tmpl, ok := templateSeedSet(templateDir())
	if !ok {
		if len(keep) == 0 {
			return nil, false
		}
		tmpl = map[string]struct{}{}
	}

	deleted = []string{}
	_ = filepath.Walk(codeDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			// Unreadable entry — skip it, never abort the whole walk.
			if info != nil && info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		rel, relErr := filepath.Rel(codeDir, path)
		if relErr != nil {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}

		base := info.Name()

		if info.IsDir() {
			// Skip vendored/cache/VCS dirs and any hidden dir wholesale.
			if _, skip := prunedDirSkip[base]; skip {
				return filepath.SkipDir
			}
			if strings.HasPrefix(base, ".") {
				return filepath.SkipDir
			}
			return nil
		}

		// Only regular files are candidates. Symlinks (e.g. node_modules) are
		// never deleted.
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil
		}
		// Hidden files are left alone (dotfiles like .gitignore are not source).
		if strings.HasPrefix(base, ".") {
			return nil
		}
		// Defense-in-depth: refuse any path that escaped codeDir.
		if strings.HasPrefix(rel, "../") || rel == ".." {
			return nil
		}

		// Survivors: manifest, template seed, protected config.
		if _, kept := keep[rel]; kept {
			return nil
		}
		if _, seed := tmpl[rel]; seed {
			return nil
		}
		if _, prot := protectedFiles[rel]; prot {
			return nil
		}

		// Stale project file — remove it.
		if err := os.Remove(path); err != nil {
			return nil
		}
		deleted = append(deleted, rel)
		changed = true
		return nil
	})

	return deleted, changed
}

// templateSeedSet returns the set of relative file paths under dir (the readonly
// template seed). The second return is false when dir can't be read at all, the
// fail-open signal pruneStale uses to skip pruning entirely.
func templateSeedSet(dir string) (map[string]struct{}, bool) {
	if _, err := os.Stat(dir); err != nil {
		return nil, false
	}
	seeds := map[string]struct{}{}
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return nil
		}
		seeds[filepath.ToSlash(rel)] = struct{}{}
		return nil
	})
	if err != nil {
		return nil, false
	}
	return seeds, true
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
