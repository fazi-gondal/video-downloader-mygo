package services

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DefaultStaleStagingAge keeps recent staging directories even when no task
// references them, so a crash during creation does not race with startup.
const DefaultStaleStagingAge = 24 * time.Hour

// StagingCleanupResult reports the directories removed by a startup sweep and
// the per-directory failures. Callers log both; cleanup never blocks startup.
type StagingCleanupResult struct {
	Removed []string
	Errors  []error
}

// CleanupStaleStagingDirs removes direct children of the known output
// directories that are named .mygo-download-* and are older than minAge.
// Paths in keep are never removed. The sweep does not follow symlinks and does
// not recurse, so it cannot touch anything outside the supplied directories.
func CleanupStaleStagingDirs(outputDirs []string, keep map[string]bool, minAge time.Duration, now time.Time) StagingCleanupResult {
	var result StagingCleanupResult
	if minAge <= 0 {
		minAge = DefaultStaleStagingAge
	}

	kept := make(map[string]bool, len(keep))
	for path := range keep {
		if path != "" {
			kept[canonicalStagingPath(path)] = true
		}
	}

	seenDirs := make(map[string]bool, len(outputDirs))
	for _, outputDir := range outputDirs {
		if strings.TrimSpace(outputDir) == "" {
			continue
		}
		dir := canonicalStagingPath(outputDir)
		if seenDirs[dir] {
			continue
		}
		seenDirs[dir] = true

		info, err := os.Lstat(dir)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			result.Errors = append(result.Errors, fmt.Errorf("inspect staging parent %q: %w", dir, err))
			continue
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			continue
		}

		entries, err := os.ReadDir(dir)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Errorf("read staging parent %q: %w", dir, err))
			continue
		}
		for _, entry := range entries {
			name := entry.Name()
			if !strings.HasPrefix(name, ".mygo-download-") {
				continue
			}
			if entry.Type()&os.ModeSymlink != 0 || !entry.IsDir() {
				continue
			}
			path := filepath.Join(dir, name)
			if kept[path] {
				continue
			}
			entryInfo, err := entry.Info()
			if err != nil {
				result.Errors = append(result.Errors, fmt.Errorf("inspect staging directory %q: %w", path, err))
				continue
			}
			if now.Sub(entryInfo.ModTime()) < minAge {
				continue
			}
			if err := os.RemoveAll(path); err != nil {
				result.Errors = append(result.Errors, fmt.Errorf("remove stale staging directory %q: %w", path, err))
				continue
			}
			result.Removed = append(result.Removed, path)
		}
	}
	return result
}

func canonicalStagingPath(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		return filepath.Clean(abs)
	}
	return filepath.Clean(path)
}
