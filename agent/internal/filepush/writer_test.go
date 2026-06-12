package filepush

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWriteFiles_CreatesFileAtCorrectPath(t *testing.T) {
	dir := t.TempDir()
	files := []FileEntry{
		{Path: "App.tsx", Content: b64("hello world"), Delete: false},
	}

	result := WriteFiles(dir, files)

	if len(result.Written) != 1 || result.Written[0] != "App.tsx" {
		t.Fatalf("expected Written=[App.tsx], got Written=%v", result.Written)
	}
	if len(result.Failed) != 0 {
		t.Fatalf("expected no failures, got Failed=%v", result.Failed)
	}

	got, err := os.ReadFile(filepath.Join(dir, "App.tsx"))
	if err != nil {
		t.Fatalf("reading written file: %v", err)
	}
	if string(got) != "hello world" {
		t.Errorf("file content = %q, want %q", got, "hello world")
	}
}

func TestWriteFiles_CreatesSubdirectories(t *testing.T) {
	dir := t.TempDir()
	files := []FileEntry{
		{Path: "screens/Home.tsx", Content: b64("home screen"), Delete: false},
	}

	result := WriteFiles(dir, files)

	if len(result.Written) != 1 || result.Written[0] != "screens/Home.tsx" {
		t.Fatalf("expected Written=[screens/Home.tsx], got Written=%v", result.Written)
	}

	got, err := os.ReadFile(filepath.Join(dir, "screens", "Home.tsx"))
	if err != nil {
		t.Fatalf("reading written file: %v", err)
	}
	if string(got) != "home screen" {
		t.Errorf("file content = %q, want %q", got, "home screen")
	}
}

func TestWriteFiles_DecodesBase64Content(t *testing.T) {
	dir := t.TempDir()
	original := "import React from 'react';\n\nexport default function App() { return null; }\n"
	files := []FileEntry{
		{Path: "App.tsx", Content: base64.StdEncoding.EncodeToString([]byte(original)), Delete: false},
	}

	result := WriteFiles(dir, files)

	if len(result.Written) != 1 {
		t.Fatalf("expected 1 written, got %d", len(result.Written))
	}

	got, err := os.ReadFile(filepath.Join(dir, "App.tsx"))
	if err != nil {
		t.Fatalf("reading file: %v", err)
	}
	if string(got) != original {
		t.Errorf("file content = %q, want %q", got, original)
	}
}

func TestWriteFiles_DeleteRemovesFile(t *testing.T) {
	dir := t.TempDir()

	// Create a file first
	target := filepath.Join(dir, "old.tsx")
	if err := os.WriteFile(target, []byte("old content"), 0644); err != nil {
		t.Fatalf("creating test file: %v", err)
	}

	files := []FileEntry{
		{Path: "old.tsx", Content: "", Delete: true},
	}

	result := WriteFiles(dir, files)

	if len(result.Written) != 1 || result.Written[0] != "old.tsx" {
		t.Fatalf("expected Written=[old.tsx], got Written=%v", result.Written)
	}

	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Error("file should have been deleted")
	}
}

func TestWriteFiles_RejectsPathTraversal(t *testing.T) {
	dir := t.TempDir()
	files := []FileEntry{
		{Path: "../etc/passwd", Content: b64("evil"), Delete: false},
	}

	result := WriteFiles(dir, files)

	if len(result.Failed) != 1 || result.Failed[0] != "../etc/passwd" {
		t.Fatalf("expected Failed=[../etc/passwd], got Failed=%v", result.Failed)
	}
	if len(result.Written) != 0 {
		t.Fatalf("expected no written files, got Written=%v", result.Written)
	}

	// Verify file was NOT created
	if _, err := os.Stat(filepath.Join(dir, "..", "etc", "passwd")); !os.IsNotExist(err) {
		t.Error("path traversal file should not exist")
	}
}

func TestWriteFiles_RejectsAbsolutePath(t *testing.T) {
	dir := t.TempDir()
	files := []FileEntry{
		{Path: "/tmp/evil.txt", Content: b64("evil"), Delete: false},
	}

	result := WriteFiles(dir, files)

	if len(result.Failed) != 1 || result.Failed[0] != "/tmp/evil.txt" {
		t.Fatalf("expected Failed=[/tmp/evil.txt], got Failed=%v", result.Failed)
	}
	if len(result.Written) != 0 {
		t.Fatalf("expected no written files, got Written=%v", result.Written)
	}
}

func TestWriteFiles_ReportsPartialFailures(t *testing.T) {
	dir := t.TempDir()
	files := []FileEntry{
		{Path: "good.tsx", Content: b64("good"), Delete: false},
		{Path: "../bad.tsx", Content: b64("bad"), Delete: false},
		{Path: "also-good.tsx", Content: b64("also good"), Delete: false},
	}

	result := WriteFiles(dir, files)

	if len(result.Written) != 2 {
		t.Fatalf("expected 2 written, got %d: %v", len(result.Written), result.Written)
	}
	if len(result.Failed) != 1 || result.Failed[0] != "../bad.tsx" {
		t.Fatalf("expected Failed=[../bad.tsx], got Failed=%v", result.Failed)
	}
}

func TestWriteFiles_RejectsMetroConfigOverwrite(t *testing.T) {
	dir := t.TempDir()
	cases := []string{
		"metro.config.js",
		"metro.config.ts",
		"metro.config.cjs",
		"metro.config.mjs",
		"./metro.config.js", // filepath.Clean normalises to metro.config.js
	}

	for _, path := range cases {
		files := []FileEntry{{Path: path, Content: b64("module.exports = {}"), Delete: false}}
		result := WriteFiles(dir, files)

		if len(result.Written) != 0 {
			t.Errorf("path %q: expected no writes, got Written=%v", path, result.Written)
		}
		if len(result.Failed) != 1 {
			t.Errorf("path %q: expected 1 failed, got %v", path, result.Failed)
		}
	}
}

func TestWriteFiles_EmptyFilesArray(t *testing.T) {
	dir := t.TempDir()

	result := WriteFiles(dir, []FileEntry{})

	if len(result.Written) != 0 {
		t.Fatalf("expected empty written, got %v", result.Written)
	}
	if len(result.Failed) != 0 {
		t.Fatalf("expected empty failed, got %v", result.Failed)
	}
}

func TestWriteTar_ExtractsContents(t *testing.T) {
	dir := t.TempDir()

	// Build a tar.gz with two files
	buf := buildTarGz(t, map[string]string{
		"App.tsx":          "app content",
		"screens/Home.tsx": "home content",
	})

	result, err := WriteTar(dir, buf)
	if err != nil {
		t.Fatalf("WriteTar failed: %v", err)
	}

	if len(result.Written) != 2 {
		t.Fatalf("expected 2 written, got %d: %v", len(result.Written), result.Written)
	}
	if len(result.Failed) != 0 {
		t.Fatalf("expected no failures, got %v", result.Failed)
	}

	got, err := os.ReadFile(filepath.Join(dir, "App.tsx"))
	if err != nil {
		t.Fatalf("reading App.tsx: %v", err)
	}
	if string(got) != "app content" {
		t.Errorf("App.tsx content = %q, want %q", got, "app content")
	}

	got, err = os.ReadFile(filepath.Join(dir, "screens", "Home.tsx"))
	if err != nil {
		t.Fatalf("reading screens/Home.tsx: %v", err)
	}
	if string(got) != "home content" {
		t.Errorf("screens/Home.tsx content = %q, want %q", got, "home content")
	}
}

func TestWriteTar_IgnoresSymlinks(t *testing.T) {
	dir := t.TempDir()

	// Build a tar.gz with a symlink entry
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	// Add a regular file
	content := []byte("legit content")
	if err := tw.WriteHeader(&tar.Header{
		Name:     "legit.tsx",
		Size:     int64(len(content)),
		Mode:     0644,
		Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}

	// Add a symlink (should be ignored)
	if err := tw.WriteHeader(&tar.Header{
		Name:     "evil-link",
		Linkname: "/etc/passwd",
		Mode:     0777,
		Typeflag: tar.TypeSymlink,
	}); err != nil {
		t.Fatal(err)
	}

	tw.Close()
	gz.Close()

	result, err := WriteTar(dir, &buf)
	if err != nil {
		t.Fatalf("WriteTar failed: %v", err)
	}

	// Only the regular file should be written
	if len(result.Written) != 1 || result.Written[0] != "legit.tsx" {
		t.Fatalf("expected Written=[legit.tsx], got Written=%v", result.Written)
	}

	// Symlink should NOT exist
	if _, err := os.Lstat(filepath.Join(dir, "evil-link")); !os.IsNotExist(err) {
		t.Error("symlink entry should have been ignored")
	}
}

func TestWriteFiles_BumpsWatchedRootMtimeOnWrite(t *testing.T) {
	dir := t.TempDir()

	// Seed a watched root file (Metro entry) with an old mtime.
	entry := filepath.Join(dir, "entry.js")
	if err := os.WriteFile(entry, []byte("import 'expo-router/entry';"), 0644); err != nil {
		t.Fatalf("seeding entry.js: %v", err)
	}
	old := time.Now().Add(-1 * time.Hour)
	if err := os.Chtimes(entry, old, old); err != nil {
		t.Fatalf("backdating entry.js: %v", err)
	}

	// Push a brand-new nested file, the inotify-weak-spot case.
	files := []FileEntry{
		{Path: "app/(tabs)/settings.tsx", Content: b64("export default function S(){return null}"), Delete: false},
	}
	result := WriteFiles(dir, files)
	if len(result.Written) != 1 {
		t.Fatalf("expected 1 written, got %v", result.Written)
	}

	info, err := os.Stat(entry)
	if err != nil {
		t.Fatalf("stat entry.js: %v", err)
	}
	if !info.ModTime().After(old) {
		t.Errorf("expected entry.js mtime to be bumped past %v, got %v", old, info.ModTime())
	}
}

func TestTriggerMetroRebuild_NoWatchedFileIsNoop(t *testing.T) {
	dir := t.TempDir()
	// No entry.js / app router files present — must not panic or create anything.
	triggerMetroRebuild(dir)

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading dir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("triggerMetroRebuild created files: %v", entries)
	}
}

// TestWriteFiles_IdenticalBytesSkipWrite is the core wake-reflush fix: pushing
// the exact bytes a file already holds must NOT rewrite it (no mtime bump) but
// must still report the path as Written so caller accounting is unchanged.
func TestWriteFiles_IdenticalBytesSkipWrite(t *testing.T) {
	dir := t.TempDir()

	target := filepath.Join(dir, "app/index.tsx")
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	content := "export default function Home(){ return null }"
	if err := os.WriteFile(target, []byte(content), 0644); err != nil {
		t.Fatalf("seeding file: %v", err)
	}
	// Backdate the file's mtime far enough that any rewrite is detectable.
	old := time.Now().Add(-1 * time.Hour)
	if err := os.Chtimes(target, old, old); err != nil {
		t.Fatalf("backdating: %v", err)
	}

	result := WriteFiles(dir, []FileEntry{
		{Path: "app/index.tsx", Content: b64(content), Delete: false},
	})

	// Reported as written (file IS present with the requested bytes).
	if len(result.Written) != 1 || result.Written[0] != "app/index.tsx" {
		t.Fatalf("expected Written=[app/index.tsx], got %v", result.Written)
	}
	if len(result.Failed) != 0 {
		t.Fatalf("expected no failures, got %v", result.Failed)
	}

	// But the mtime must be UNCHANGED — no rewrite happened.
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !info.ModTime().Equal(old) {
		t.Errorf("identical-byte push bumped mtime: was %v, now %v", old, info.ModTime())
	}
}

// TestWriteFiles_AllIdenticalNoMetroNudge proves the rebuild trigger: an
// all-identical reflush (the wake case) leaves the watched-root mtime untouched
// so Metro is never forced to cold-rebundle.
func TestWriteFiles_AllIdenticalNoMetroNudge(t *testing.T) {
	dir := t.TempDir()

	// Watched root (entry.js) — triggerMetroRebuild would bump this.
	entry := filepath.Join(dir, "entry.js")
	if err := os.WriteFile(entry, []byte("import 'expo-router/entry';"), 0644); err != nil {
		t.Fatalf("seeding entry.js: %v", err)
	}
	// A user file with known content.
	app := filepath.Join(dir, "App.tsx")
	appContent := "export default function App(){ return null }"
	if err := os.WriteFile(app, []byte(appContent), 0644); err != nil {
		t.Fatalf("seeding App.tsx: %v", err)
	}

	old := time.Now().Add(-1 * time.Hour)
	for _, p := range []string{entry, app} {
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatalf("backdating %s: %v", p, err)
		}
	}

	// Reflush the exact same App.tsx bytes (sleep→wake re-push).
	result := WriteFiles(dir, []FileEntry{
		{Path: "App.tsx", Content: b64(appContent), Delete: false},
	})
	if len(result.Written) != 1 {
		t.Fatalf("expected 1 written, got %v", result.Written)
	}

	info, err := os.Stat(entry)
	if err != nil {
		t.Fatalf("stat entry.js: %v", err)
	}
	if !info.ModTime().Equal(old) {
		t.Errorf("all-identical reflush nudged Metro (entry.js mtime bumped): was %v, now %v", old, info.ModTime())
	}
}

// TestWriteFiles_ChangedBytesStillNudge guards the inverse: when even one file's
// bytes differ, the write happens AND Metro is nudged.
func TestWriteFiles_ChangedBytesStillNudge(t *testing.T) {
	dir := t.TempDir()

	entry := filepath.Join(dir, "entry.js")
	if err := os.WriteFile(entry, []byte("import 'expo-router/entry';"), 0644); err != nil {
		t.Fatalf("seeding entry.js: %v", err)
	}
	app := filepath.Join(dir, "App.tsx")
	if err := os.WriteFile(app, []byte("old"), 0644); err != nil {
		t.Fatalf("seeding App.tsx: %v", err)
	}
	old := time.Now().Add(-1 * time.Hour)
	for _, p := range []string{entry, app} {
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatalf("backdating %s: %v", p, err)
		}
	}

	result := WriteFiles(dir, []FileEntry{
		{Path: "App.tsx", Content: b64("new content"), Delete: false},
	})
	if len(result.Written) != 1 {
		t.Fatalf("expected 1 written, got %v", result.Written)
	}

	// File actually rewritten.
	got, err := os.ReadFile(app)
	if err != nil {
		t.Fatalf("reading App.tsx: %v", err)
	}
	if string(got) != "new content" {
		t.Errorf("App.tsx = %q, want %q", got, "new content")
	}

	// Metro nudged (entry.js mtime bumped).
	info, err := os.Stat(entry)
	if err != nil {
		t.Fatalf("stat entry.js: %v", err)
	}
	if !info.ModTime().After(old) {
		t.Errorf("changed-byte push did not nudge Metro: entry.js mtime still %v", info.ModTime())
	}
}

// TestWriteFiles_DeleteMissingIsNoFailure: deleting an already-absent file is a
// no-op success (desired end-state holds), not a Failed entry.
func TestWriteFiles_DeleteMissingIsNoFailure(t *testing.T) {
	dir := t.TempDir()

	result := WriteFiles(dir, []FileEntry{
		{Path: "never-existed.tsx", Content: "", Delete: true},
	})

	if len(result.Failed) != 0 {
		t.Fatalf("expected no failures for absent-file delete, got %v", result.Failed)
	}
	if len(result.Written) != 1 || result.Written[0] != "never-existed.tsx" {
		t.Fatalf("expected Written=[never-existed.tsx], got %v", result.Written)
	}
}

// TestWriteTar_IdenticalBytesSkipWrite mirrors the WriteFiles identical-skip on
// the tar path.
func TestWriteTar_IdenticalBytesSkipWrite(t *testing.T) {
	dir := t.TempDir()

	target := filepath.Join(dir, "App.tsx")
	content := "app content"
	if err := os.WriteFile(target, []byte(content), 0644); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	old := time.Now().Add(-1 * time.Hour)
	if err := os.Chtimes(target, old, old); err != nil {
		t.Fatalf("backdating: %v", err)
	}

	buf := buildTarGz(t, map[string]string{"App.tsx": content})
	result, err := WriteTar(dir, buf)
	if err != nil {
		t.Fatalf("WriteTar failed: %v", err)
	}
	if len(result.Written) != 1 {
		t.Fatalf("expected 1 written, got %v", result.Written)
	}

	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !info.ModTime().Equal(old) {
		t.Errorf("identical tar entry bumped mtime: was %v, now %v", old, info.ModTime())
	}
}

// --- W4: full-sync manifest prune ---

// withTemplateDir points FORGE_TEMPLATE_DIR at a fresh temp dir seeded with the
// given relative paths, and returns a cleanup func. Used so pruneStale can tell
// template seeds from stale project files in tests.
func withTemplateDir(t *testing.T, seeds map[string]string) func() {
	t.Helper()
	tmpl := t.TempDir()
	for rel, content := range seeds {
		full := filepath.Join(tmpl, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatalf("mkdir template %s: %v", rel, err)
		}
		if err := os.WriteFile(full, []byte(content), 0644); err != nil {
			t.Fatalf("seed template %s: %v", rel, err)
		}
	}
	prev := os.Getenv("FORGE_TEMPLATE_DIR")
	os.Setenv("FORGE_TEMPLATE_DIR", tmpl)
	return func() { os.Setenv("FORGE_TEMPLATE_DIR", prev) }
}

func mustExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected %s to exist: %v", path, err)
	}
}

func mustNotExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected %s to be deleted, stat err=%v", path, err)
	}
}

// TestWriteFilesFull_PrunesFileNotInManifest is the W4 specimen: the project
// moved app/(tabs)/id-detail.tsx → app/[id].tsx, so a full sync whose manifest
// omits the old path must delete the ghost from disk.
func TestWriteFilesFull_PrunesFileNotInManifest(t *testing.T) {
	defer withTemplateDir(t, nil)()
	dir := t.TempDir()

	// Ghost file left from a prior push (not in the new manifest).
	ghost := filepath.Join(dir, "app/(tabs)/id-detail.tsx")
	if err := os.MkdirAll(filepath.Dir(ghost), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(ghost, []byte("ghost route"), 0644); err != nil {
		t.Fatalf("seed ghost: %v", err)
	}

	files := []FileEntry{
		{Path: "app/[id].tsx", Content: b64("new detail route")},
	}
	manifest := []string{"app/[id].tsx"}

	result := WriteFilesFull(dir, files, manifest)

	mustExist(t, filepath.Join(dir, "app/[id].tsx"))
	mustNotExist(t, ghost)

	if len(result.Deleted) != 1 || result.Deleted[0] != "app/(tabs)/id-detail.tsx" {
		t.Fatalf("expected Deleted=[app/(tabs)/id-detail.tsx], got %v", result.Deleted)
	}
}

// TestWriteFilesFull_TemplateSeedSurvives proves a file that originates from the
// template (and is therefore not in a project manifest) is never pruned.
func TestWriteFilesFull_TemplateSeedSurvives(t *testing.T) {
	defer withTemplateDir(t, map[string]string{
		"entry.js":        "import 'expo-router/entry';",
		"app/index.tsx":   "export default function Index(){return null}",
		"babel.config.js": "module.exports = {};",
	})()
	dir := t.TempDir()

	// Seed the template files into the code dir (entrypoint would do this).
	for rel, content := range map[string]string{
		"entry.js":      "import 'expo-router/entry';",
		"app/index.tsx": "export default function Index(){return null}",
	} {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte(content), 0644); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	// Manifest contains only a project screen — NOT the template seeds.
	files := []FileEntry{{Path: "app/(tabs)/home.tsx", Content: b64("home")}}
	manifest := []string{"app/(tabs)/home.tsx"}

	result := WriteFilesFull(dir, files, manifest)

	mustExist(t, filepath.Join(dir, "entry.js"))
	mustExist(t, filepath.Join(dir, "app/index.tsx"))
	mustExist(t, filepath.Join(dir, "app/(tabs)/home.tsx"))

	if len(result.Deleted) != 0 {
		t.Fatalf("template seeds must not be pruned, got Deleted=%v", result.Deleted)
	}
}

// TestWriteFilesFull_NodeModulesUntouched proves vendored/cache/VCS dirs and
// hidden dirs are never descended into or deleted.
func TestWriteFilesFull_NodeModulesUntouched(t *testing.T) {
	defer withTemplateDir(t, nil)()
	dir := t.TempDir()

	junk := map[string]string{
		"node_modules/react/index.js": "module.exports = {}",
		".expo/cache/blob":            "cache",
		".git/HEAD":                   "ref: refs/heads/main",
		".gitignore":                  "node_modules",
	}
	for rel, content := range junk {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte(content), 0644); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	files := []FileEntry{{Path: "app/index.tsx", Content: b64("home")}}
	manifest := []string{"app/index.tsx"}

	result := WriteFilesFull(dir, files, manifest)

	for rel := range junk {
		mustExist(t, filepath.Join(dir, rel))
	}
	if len(result.Deleted) != 0 {
		t.Fatalf("excluded dirs/files must not be pruned, got Deleted=%v", result.Deleted)
	}
}

// TestWriteFilesFull_RefusesEscapeAndProtectsConfig: the prune never deletes a
// baked metro.config.js (it's protected, not project source).
func TestWriteFilesFull_ProtectsMetroConfig(t *testing.T) {
	defer withTemplateDir(t, nil)()
	dir := t.TempDir()

	cfg := filepath.Join(dir, "metro.config.js")
	if err := os.WriteFile(cfg, []byte("module.exports = {}"), 0644); err != nil {
		t.Fatalf("seed metro config: %v", err)
	}

	files := []FileEntry{{Path: "app/index.tsx", Content: b64("home")}}
	manifest := []string{"app/index.tsx"}

	result := WriteFilesFull(dir, files, manifest)

	mustExist(t, cfg)
	if len(result.Deleted) != 0 {
		t.Fatalf("metro.config.js must not be pruned, got Deleted=%v", result.Deleted)
	}
}

// TestWriteFilesFull_ManifestOnlyPruneWhenTemplateUnreadable: in prod the
// template dir lives INSIDE the sandbox image, not on the agent host — the
// manifest (which the backend unions with the template list) is the sole
// protection. A stale file is pruned; manifest entries survive.
func TestWriteFilesFull_ManifestOnlyPruneWhenTemplateUnreadable(t *testing.T) {
	prev := os.Getenv("FORGE_TEMPLATE_DIR")
	os.Setenv("FORGE_TEMPLATE_DIR", filepath.Join(t.TempDir(), "does-not-exist"))
	defer os.Setenv("FORGE_TEMPLATE_DIR", prev)

	dir := t.TempDir()
	ghost := filepath.Join(dir, "app/(tabs)/id-detail.tsx")
	if err := os.MkdirAll(filepath.Dir(ghost), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(ghost, []byte("ghost"), 0644); err != nil {
		t.Fatalf("seed ghost: %v", err)
	}
	seed := filepath.Join(dir, "app/index.tsx")
	if err := os.WriteFile(seed, []byte("seed"), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	files := []FileEntry{{Path: "app/[id].tsx", Content: b64("new")}}
	// Backend manifest = project files ∪ template list — the seed is covered.
	manifest := []string{"app/[id].tsx", "app/index.tsx"}

	result := WriteFilesFull(dir, files, manifest)

	mustNotExist(t, ghost)
	mustExist(t, seed)
	if len(result.Deleted) != 1 {
		t.Fatalf("expected exactly the ghost pruned, got %v", result.Deleted)
	}
}

// TestWriteFilesFull_SkipsPruneWhenNoTemplateAndNoManifest: with neither a
// template index nor a manifest there is no way to tell seed from stale —
// prune must be skipped entirely.
func TestWriteFilesFull_SkipsPruneWhenNoTemplateAndNoManifest(t *testing.T) {
	prev := os.Getenv("FORGE_TEMPLATE_DIR")
	os.Setenv("FORGE_TEMPLATE_DIR", filepath.Join(t.TempDir(), "does-not-exist"))
	defer os.Setenv("FORGE_TEMPLATE_DIR", prev)

	dir := t.TempDir()
	ghost := filepath.Join(dir, "app/(tabs)/id-detail.tsx")
	if err := os.MkdirAll(filepath.Dir(ghost), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(ghost, []byte("ghost"), 0644); err != nil {
		t.Fatalf("seed ghost: %v", err)
	}

	result := WriteFilesFull(dir, nil, nil)

	mustExist(t, ghost)
	if len(result.Deleted) != 0 {
		t.Fatalf("expected no deletions with no template and no manifest, got %v", result.Deleted)
	}
}

// TestWriteFilesFull_NudgesMetroOnPruneOnly proves a prune-only full sync (no
// new writes, just a stale file removed) still nudges Metro so the ghost route
// leaves its module graph.
func TestWriteFilesFull_NudgesMetroOnPruneOnly(t *testing.T) {
	// entry.js is a template seed — register it so the prune keeps it.
	defer withTemplateDir(t, map[string]string{"entry.js": "import 'expo-router/entry';"})()
	dir := t.TempDir()

	// Watched root for the nudge.
	entry := filepath.Join(dir, "entry.js")
	if err := os.WriteFile(entry, []byte("import 'expo-router/entry';"), 0644); err != nil {
		t.Fatalf("seed entry.js: %v", err)
	}
	// A current project file (identical bytes — no write) + a ghost.
	home := filepath.Join(dir, "app/index.tsx")
	homeContent := "export default function H(){return null}"
	if err := os.MkdirAll(filepath.Dir(home), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(home, []byte(homeContent), 0644); err != nil {
		t.Fatalf("seed home: %v", err)
	}
	ghost := filepath.Join(dir, "app/ghost.tsx")
	if err := os.WriteFile(ghost, []byte("ghost"), 0644); err != nil {
		t.Fatalf("seed ghost: %v", err)
	}

	old := time.Now().Add(-1 * time.Hour)
	for _, p := range []string{entry, home} {
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatalf("backdate %s: %v", p, err)
		}
	}

	// Re-push identical home bytes (no write) + manifest excludes the ghost.
	files := []FileEntry{{Path: "app/index.tsx", Content: b64(homeContent)}}
	manifest := []string{"app/index.tsx"}

	result := WriteFilesFull(dir, files, manifest)

	mustNotExist(t, ghost)
	if len(result.Deleted) != 1 {
		t.Fatalf("expected 1 deletion, got %v", result.Deleted)
	}

	// Metro must have been nudged despite zero byte-writes.
	info, err := os.Stat(entry)
	if err != nil {
		t.Fatalf("stat entry.js: %v", err)
	}
	if !info.ModTime().After(old) {
		t.Errorf("prune-only full sync did not nudge Metro: entry.js mtime still %v", info.ModTime())
	}
}

// --- helpers ---

func b64(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}

func buildTarGz(t *testing.T, files map[string]string) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	for name, content := range files {
		data := []byte(content)
		if err := tw.WriteHeader(&tar.Header{
			Name:     name,
			Size:     int64(len(data)),
			Mode:     0644,
			Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}

	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf
}
