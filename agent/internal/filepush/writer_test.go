package filepush

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
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
		"App.tsx":         "app content",
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
		Name: "legit.tsx",
		Size: int64(len(content)),
		Mode: 0644,
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
