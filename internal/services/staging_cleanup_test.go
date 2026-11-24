package services

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestCleanupStaleStagingDirs(t *testing.T) {
	root := t.TempDir()
	old := time.Now().Add(-48 * time.Hour)

	stale := filepath.Join(root, ".mygo-download-stale")
	paused := filepath.Join(root, ".mygo-download-paused")
	fresh := filepath.Join(root, ".mygo-download-fresh")
	unrelated := filepath.Join(root, "downloads")
	for _, dir := range []string{stale, paused, fresh, unrelated} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, dir := range []string{stale, paused} {
		if err := os.Chtimes(dir, old, old); err != nil {
			t.Fatal(err)
		}
	}

	var symlink, target string
	if runtime.GOOS != "windows" {
		target = filepath.Join(t.TempDir(), "target")
		if err := os.Mkdir(target, 0o700); err != nil {
			t.Fatal(err)
		}
		symlink = filepath.Join(root, ".mygo-download-link")
		if err := os.Symlink(target, symlink); err != nil {
			t.Fatalf("symlink: %v", err)
		}
	}

	result := CleanupStaleStagingDirs([]string{root, root}, map[string]bool{paused: true}, time.Hour, time.Now())
	if len(result.Removed) != 1 || result.Removed[0] != stale {
		t.Fatalf("removed = %v, want only %q", result.Removed, stale)
	}
	if len(result.Errors) != 0 {
		t.Fatalf("cleanup errors = %v", result.Errors)
	}
	for _, dir := range []string{paused, fresh, unrelated} {
		if _, err := os.Stat(dir); err != nil {
			t.Fatalf("%s should remain: %v", dir, err)
		}
	}
	if symlink != "" {
		if _, err := os.Stat(symlink); err != nil {
			t.Fatalf("symlink should not be followed or removed: %v", err)
		}
		if _, err := os.Stat(target); err != nil {
			t.Fatalf("symlink target should remain: %v", err)
		}
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale staging directory still exists: %v", err)
	}
}
