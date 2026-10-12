package config

import (
	"encoding/json"
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
	_ = os.MkdirAll(dir, 0o755)
	return filepath.Join(dir, "settings.json")
}

func (r *SettingsRepository) Load() AppSettings {
	r.mu.Lock()
	defer r.mu.Unlock()
	data, err := os.ReadFile(r.path)
	if err != nil {
		return DefaultSettings()
	}
	var s AppSettings
	if err := json.Unmarshal(data, &s); err != nil {
		return DefaultSettings()
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
	return s
}

func (r *SettingsRepository) Save(s AppSettings) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(r.path, data, 0o644)
}
