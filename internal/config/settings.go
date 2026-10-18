package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

// AppSettings mirrors the original Python settings model.
type AppSettings struct {
	DownloadDir         string            `json:"download_dir"`
	MaxConcurrent       int               `json:"max_concurrent"`
	ConcurrentFragments int               `json:"concurrent_fragments"`
	Proxy               string            `json:"proxy"`
	CookiesBrowser      string            `json:"cookies_browser"`
	CustomHeaders       map[string]string `json:"custom_headers"`
	RateLimitKbps       int               `json:"rate_limit_kbps"`
	KeepOriginals       bool              `json:"keep_originals"`
	WriteSubtitles      bool              `json:"write_subtitles"`
	SubtitleLangs       []string          `json:"subtitle_langs"`
	MultiAudio          bool              `json:"multi_audio"`
	EmbedThumbnail      bool              `json:"embed_thumbnail"`
	EmbedMetadata       bool              `json:"embed_metadata"`
	PreferVP9Video      bool              `json:"prefer_vp9_video"`
	ThemeMode           string            `json:"theme_mode"` // system | dark | light
}

func DefaultSettings() AppSettings {
	home, _ := os.UserHomeDir()
	return AppSettings{
		DownloadDir:         filepath.Join(home, "Downloads", "VideoDownloader"),
		MaxConcurrent:       4,
		ConcurrentFragments: 1,
		KeepOriginals:       true,
		WriteSubtitles:      false,
		SubtitleLangs:       []string{"all"},
		MultiAudio:          false,
		EmbedThumbnail:      true,
		EmbedMetadata:       true,
		PreferVP9Video:      true,
		ThemeMode:           "system",
		CustomHeaders:       map[string]string{},
	}
}

type SettingsRepository struct {
	mu      sync.Mutex
	path    string
	initErr error
}

func NewSettingsRepository() *SettingsRepository {
	path, err := settingsPath()
	return &SettingsRepository{path: path, initErr: err}
}

func settingsPath() (string, error) {
	var dir string
	switch runtime.GOOS {
	case "windows":
		base := os.Getenv("LOCALAPPDATA")
		if base == "" {
			base, _ = os.UserConfigDir()
		}
		if base == "" {
			base, _ = os.UserHomeDir()
		}
		dir = filepath.Join(base, "VideoDownloader")
	case "darwin":
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, "Library", "Application Support", "VideoDownloader")
	default:
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config", "VideoDownloader")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return filepath.Join(dir, "settings.json"), fmt.Errorf("create settings directory: %w", err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(dir, 0o700); err != nil { // #nosec G302 -- settings directories intentionally use owner-only permissions.
			return filepath.Join(dir, "settings.json"), fmt.Errorf("secure settings directory: %w", err)
		}
	}
	return filepath.Join(dir, "settings.json"), nil
}

func (r *SettingsRepository) Load() (AppSettings, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.initErr != nil {
		return DefaultSettings(), r.initErr
	}
	data, err := os.ReadFile(r.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return DefaultSettings(), nil
		}
		return DefaultSettings(), fmt.Errorf("read settings: %w", err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(r.path, 0o600); err != nil {
			return DefaultSettings(), fmt.Errorf("secure settings permissions: %w", err)
		}
	}
	var s AppSettings
	if err := json.Unmarshal(data, &s); err != nil {
		return DefaultSettings(), fmt.Errorf("decode settings: %w", err)
	}
	// Fill defaults for missing fields
	def := DefaultSettings()
	if s.DownloadDir == "" {
		s.DownloadDir = def.DownloadDir
	}
	if s.MaxConcurrent < 1 {
		s.MaxConcurrent = def.MaxConcurrent
	}
	if s.ThemeMode == "" {
		s.ThemeMode = def.ThemeMode
	}
	if s.CustomHeaders == nil {
		s.CustomHeaders = map[string]string{}
	}
	return s, nil
}

func (r *SettingsRepository) Save(s AppSettings) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.initErr != nil {
		return r.initErr
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("encode settings: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(r.path), 0o700); err != nil {
		return fmt.Errorf("create settings directory: %w", err)
	}
	if runtime.GOOS != "windows" {
		_ = os.Chmod(filepath.Dir(r.path), 0o700) // #nosec G302 -- settings directories intentionally use owner-only permissions.
	}
	return atomicWriteSettingsFile(r.path, data)
}

func atomicWriteSettingsFile(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".settings.json.tmp-*")
	if err != nil {
		return fmt.Errorf("create settings temporary file: %w", err)
	}
	tmpPath := tmp.Name()
	closed := false
	defer func() {
		if !closed {
			_ = tmp.Close()
		}
		_ = os.Remove(tmpPath)
	}()

	if runtime.GOOS != "windows" {
		if err := tmp.Chmod(0o600); err != nil {
			return fmt.Errorf("set settings temporary file permissions: %w", err)
		}
	}
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("write settings temporary file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync settings temporary file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close settings temporary file: %w", err)
	}
	closed = true
	if runtime.GOOS == "windows" {
		return replaceWindowsSettingsFile(tmpPath, path)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace settings file: %w", err)
	}
	return nil
}

func replaceWindowsSettingsFile(tmpPath, path string) error {
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		return fmt.Errorf("replace settings file: destination is a directory")
	}
	backup := path + ".bak"
	_ = os.Remove(backup)
	if err := os.Rename(path, backup); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("stage existing settings file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		if restoreErr := os.Rename(backup, path); restoreErr != nil && !errors.Is(restoreErr, fs.ErrNotExist) {
			return fmt.Errorf("replace settings file: %w (restore failed: %v)", err, restoreErr)
		}
		return fmt.Errorf("replace settings file: %w", err)
	}
	_ = os.Remove(backup)
	return nil
}
