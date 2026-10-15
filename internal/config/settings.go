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
	mu   sync.Mutex
	path string
}

func NewSettingsRepository() *SettingsRepository {
	return &SettingsRepository{path: settingsPath()}
}

func settingsPath() string {
	var dir string
	switch runtime.GOOS {
	case "windows":
		dir = filepath.Join(os.Getenv("LOCALAPPDATA"), "VideoDownloader")
	case "darwin":
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, "Library", "Application Support", "VideoDownloader")
	default:
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config", "VideoDownloader")
	}
	_ = os.MkdirAll(dir, 0o700)
	return filepath.Join(dir, "settings.json")
}

func (r *SettingsRepository) Load() (AppSettings, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	data, err := os.ReadFile(r.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return DefaultSettings(), nil
		}
		return DefaultSettings(), fmt.Errorf("read settings: %w", err)
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
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("encode settings: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(r.path), 0o700); err != nil {
		return fmt.Errorf("create settings directory: %w", err)
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
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace settings file: %w", err)
	}
	return nil
}
