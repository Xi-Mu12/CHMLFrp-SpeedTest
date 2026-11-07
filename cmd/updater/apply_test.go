package main

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func setupDirs(t *testing.T) (target, stage string) {
	t.Helper()
	root := t.TempDir()
	target = filepath.Join(root, "app")
	stage = filepath.Join(root, "stage")
	writeFile(t, filepath.Join(target, "app.exe"), "old-app")
	writeFile(t, filepath.Join(target, "updater.exe"), "old-updater")
	writeFile(t, filepath.Join(target, "keep.ini"), "keep")
	writeFile(t, filepath.Join(stage, "app.exe"), "new-app")
	writeFile(t, filepath.Join(stage, "updater.exe"), "new-updater")
	writeFile(t, filepath.Join(stage, "new.dll"), "new-lib")
	return target, stage
}

func TestApplyReplacesFiles(t *testing.T) {
	target, stage := setupDirs(t)

	done, err := apply(stage, target)
	if err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(target, "app.exe")); got != "new-app" {
		t.Fatalf("app.exe not replaced: %q", got)
	}
	if got := readFile(t, filepath.Join(target, "new.dll")); got != "new-lib" {
		t.Fatalf("new file not copied: %q", got)
	}
	if got := readFile(t, filepath.Join(target, "keep.ini")); got != "keep" {
		t.Fatalf("unrelated file changed: %q", got)
	}
	if _, err := os.Stat(filepath.Join(target, "app.exe.old")); err != nil {
		t.Fatal("backup missing before commit")
	}

	commit(done)
	if _, err := os.Stat(filepath.Join(target, "app.exe.old")); !os.IsNotExist(err) {
		t.Fatal("backup should be removed after commit")
	}
	if _, err := os.Stat(filepath.Join(target, "new.dll.old")); !os.IsNotExist(err) {
		t.Fatal("no backup should exist for new files")
	}
}

func TestRollbackRestoresOldVersion(t *testing.T) {
	target, stage := setupDirs(t)

	done, err := apply(stage, target)
	if err != nil {
		t.Fatal(err)
	}
	rollback(done)

	if got := readFile(t, filepath.Join(target, "app.exe")); got != "old-app" {
		t.Fatalf("app.exe not restored: %q", got)
	}
	if got := readFile(t, filepath.Join(target, "updater.exe")); got != "old-updater" {
		t.Fatalf("updater.exe not restored: %q", got)
	}
	if _, err := os.Stat(filepath.Join(target, "new.dll")); !os.IsNotExist(err) {
		t.Fatal("newly added file should be removed on rollback")
	}
	if _, err := os.Stat(filepath.Join(target, "app.exe.old")); !os.IsNotExist(err) {
		t.Fatal("backup should be consumed by rollback")
	}
}

func TestWithinDir(t *testing.T) {
	root := filepath.Join(t.TempDir(), "app")
	if !withinDir(filepath.Join(root, "a", "b.exe"), root) {
		t.Fatal("nested path should be inside")
	}
	if withinDir(filepath.Join(root, "..", "other.exe"), root) {
		t.Fatal("sibling path must not be inside")
	}
	if !withinDir(root, root) {
		t.Fatal("root should be inside itself")
	}
}
