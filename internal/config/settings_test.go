package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestSettingsRepositoryLoadMissingUsesDefaults(t *testing.T) {
	repo := &SettingsRepository{path: filepath.Join(t.TempDir(), "settings.json")}

	got, err := repo.Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil for missing file", err)
	}
	want := DefaultSettings()
	if got.DownloadDir != want.DownloadDir || got.MaxConcurrent != want.MaxConcurrent || got.ThemeMode != want.ThemeMode {
		t.Fatalf("Load() defaults = %#v, want fields from %#v", got, want)
	}
}

func TestSettingsRepositoryLoadPartialSettingsPreservesDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"download_dir":"downloads","max_concurrent":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	repo := &SettingsRepository{path: path}

	got, err := repo.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := DefaultSettings()
	want.DownloadDir = "downloads"
	want.MaxConcurrent = 2
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Load() = %#v, want %#v", got, want)
	}
}

func TestSettingsRepositoryLoadPreservesExplicitFalseValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	data := []byte(`{"download_dir":"downloads","keep_originals":false,"embed_thumbnail":false,"embed_metadata":false,"prefer_vp9_video":false}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	repo := &SettingsRepository{path: path}

	got, err := repo.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.KeepOriginals || got.EmbedThumbnail || got.EmbedMetadata || got.PreferVP9Video {
		t.Fatalf("Load() changed explicit false values: %#v", got)
	}
}

func TestSettingsRepositoryLoadMalformedReturnsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"download_dir":`), 0o600); err != nil {
		t.Fatal(err)
	}
	repo := &SettingsRepository{path: path}

	got, err := repo.Load()
	if err == nil || !strings.Contains(err.Error(), "decode settings") {
		t.Fatalf("Load() error = %v, want decode error", err)
	}
	if got.DownloadDir == "" || got.MaxConcurrent == 0 {
		t.Fatalf("Load() returned empty state for malformed file: %#v", got)
	}
}

func TestSettingsRepositorySaveUsesPrivatePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix file permission bits are not meaningful on Windows")
	}
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	repo := &SettingsRepository{path: path}
	settings := DefaultSettings()
	settings.DownloadDir = filepath.Join(t.TempDir(), "downloads")
	if err := repo.Save(settings); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("settings permissions = %o, want 600", got)
	}
	var decoded AppSettings
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("saved settings are invalid JSON: %v", err)
	}
	if decoded.DownloadDir != settings.DownloadDir {
		t.Fatalf("saved download directory = %q, want %q", decoded.DownloadDir, settings.DownloadDir)
	}
	if leftovers, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".settings.json.tmp-*")); err != nil {
		t.Fatal(err)
	} else if len(leftovers) != 0 {
		t.Fatalf("temporary settings files remain: %v", leftovers)
	}
}

func TestSettingsRepositorySaveFailurePreservesDestination(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(path, "sentinel")
	if err := os.WriteFile(sentinel, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}

	repo := &SettingsRepository{path: path}
	if err := repo.Save(DefaultSettings()); err == nil {
		t.Fatal("Save() succeeded when destination was a directory")
	}
	data, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "existing" {
		t.Fatalf("destination data = %q, want existing data", data)
	}
	if leftovers, err := filepath.Glob(filepath.Join(dir, ".settings.json.tmp-*")); err != nil {
		t.Fatal(err)
	} else if len(leftovers) != 0 {
		t.Fatalf("temporary settings files remain after failure: %v", leftovers)
	}
}
