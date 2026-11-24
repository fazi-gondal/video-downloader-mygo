package tools

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// RecoverManagedTools restores a tool whose target is missing but whose
// install-time backup survived a crash or quit, and removes stale installer
// directories and backups. It runs once at startup, before services resolve
// their paths.
func RecoverManagedTools(now time.Time) {
	dir := ManagedDir()
	if dir == "" {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	for _, name := range []string{"ffmpeg", "ffprobe", "yt-dlp"} {
		target := filepath.Join(dir, exeName(name))
		if fileExists(target) {
			continue
		}
		prefix := exeName(name) + ".backup-"
		var newest string
		var newestMod time.Time
		for _, entry := range entries {
			if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !strings.HasPrefix(entry.Name(), prefix) {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				continue
			}
			if newest == "" || info.ModTime().After(newestMod) {
				newest = filepath.Join(dir, entry.Name())
				newestMod = info.ModTime()
			}
		}
		if newest != "" {
			_ = os.Rename(newest, target)
		}
	}

	staleBefore := now.Add(-time.Hour)
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		info, err := entry.Info()
		if err != nil || !info.ModTime().Before(staleBefore) {
			continue
		}
		if entry.IsDir() && strings.HasPrefix(entry.Name(), ".install-") {
			_ = os.RemoveAll(path)
			continue
		}
		if !entry.IsDir() && strings.Contains(entry.Name(), ".backup-") {
			_ = os.Remove(path)
		}
	}
}
