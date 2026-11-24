package tools

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRecoverManagedToolsRestoresBackup(t *testing.T) {
	dir := t.TempDir()
	old := managedDir
	managedDir = func() string { return dir }
	t.Cleanup(func() { managedDir = old })

	backup := filepath.Join(dir, "ffmpeg.backup-123")
	if err := os.WriteFile(backup, []byte("old ffmpeg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ffprobe"), []byte("current ffprobe"), 0o755); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(backup, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	RecoverManagedTools(time.Now())
	data, err := os.ReadFile(filepath.Join(dir, "ffmpeg"))
	if err != nil || string(data) != "old ffmpeg" {
		t.Fatalf("restored ffmpeg = %q, err=%v", data, err)
	}
}

func TestRecoverManagedToolsCleansStaleInstallArtifacts(t *testing.T) {
	dir := t.TempDir()
	old := managedDir
	managedDir = func() string { return dir }
	t.Cleanup(func() { managedDir = old })

	staleInstall := filepath.Join(dir, ".install-stale")
	freshInstall := filepath.Join(dir, ".install-fresh")
	staleBackup := filepath.Join(dir, "yt-dlp.backup-old")
	for _, path := range []string{staleInstall, freshInstall} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(staleBackup, []byte("backup"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-2 * time.Hour)
	for _, path := range []string{staleInstall, staleBackup} {
		if err := os.Chtimes(path, oldTime, oldTime); err != nil {
			t.Fatal(err)
		}
	}

	RecoverManagedTools(time.Now())
	if _, err := os.Stat(staleInstall); !os.IsNotExist(err) {
		t.Fatalf("stale install directory remains: %v", err)
	}
	if _, err := os.Stat(freshInstall); err != nil {
		t.Fatalf("fresh install directory was removed: %v", err)
	}
	if _, err := os.Stat(staleBackup); !os.IsNotExist(err) {
		t.Fatalf("stale backup remains: %v", err)
	}
}
