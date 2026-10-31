package services

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/fazi-gondal/video-downloader-mygo/internal/models"
)

type HistoryEntry struct {
	ID            string `json:"id"`
	URL           string `json:"url"`
	Title         string `json:"title"`
	Mode          string `json:"mode"`
	Container     string `json:"container"`
	MaxHeight     int    `json:"max_height,omitempty"`
	MaxFPS        int    `json:"max_fps,omitempty"`
	VideoFormatID string `json:"video_format_id,omitempty"`
	AudioFormatID string `json:"audio_format_id,omitempty"`
	AudioFmt      string `json:"audio_format"`
	OutputDir     string `json:"output_dir"`
	OutputPath    string `json:"output_path"`
	State         string `json:"state"`
	Error         string `json:"error"`
	CreatedAt     string `json:"created_at"`
	FinishedAt    string `json:"finished_at"`
}

type HistoryService struct {
	path string
	mu   sync.Mutex
}

func NewHistoryService() (*HistoryService, error) {
	dir := historyDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create history directory: %w", err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(dir, 0o700); err != nil { // #nosec G302 -- history directories intentionally use owner-only permissions.
			return nil, fmt.Errorf("secure history directory: %w", err)
		}
	}
	return &HistoryService{path: filepath.Join(dir, "history.json")}, nil
}

func historyDir() string {
	switch runtime.GOOS {
	case "windows":
		base := os.Getenv("LOCALAPPDATA")
		if base == "" {
			base, _ = os.UserConfigDir()
		}
		if base == "" {
			base, _ = os.UserHomeDir()
		}
		return filepath.Join(base, "VideoDownloader")
	case "darwin":
		home, _ := os.UserHomeDir()
		return filepath.Join(home, "Library", "Application Support", "VideoDownloader")
	default:
		home, _ := os.UserHomeDir()
		return filepath.Join(home, ".config", "VideoDownloader")
	}
}

func (h *HistoryService) load() ([]HistoryEntry, error) {
	data, err := os.ReadFile(h.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read history: %w", err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(h.path, 0o600); err != nil {
			return nil, fmt.Errorf("secure history permissions: %w", err)
		}
	}
	var entries []HistoryEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("decode history: %w", err)
	}
	return entries, nil
}

func (h *HistoryService) save(entries []HistoryEntry) error {
	if entries == nil {
		entries = []HistoryEntry{}
	}
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return fmt.Errorf("encode history: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(h.path), 0o700); err != nil {
		return fmt.Errorf("create history directory: %w", err)
	}
	if runtime.GOOS != "windows" {
		_ = os.Chmod(filepath.Dir(h.path), 0o700) // #nosec G302 -- history directories intentionally use owner-only permissions.
	}
	return atomicWriteHistoryFile(h.path, data)
}

func (h *HistoryService) Record(task *models.DownloadTask) error {
	snapshot := task.Snapshot()
	if !snapshot.State.IsTerminal() {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	entries, err := h.load()
	if err != nil {
		return err
	}
	req := snapshot.Request
	finished := ""
	if snapshot.FinishedAt != nil {
		finished = snapshot.FinishedAt.Format(time.RFC3339)
	}
	e := HistoryEntry{
		ID: snapshot.ID, URL: req.URL, Title: req.Title, Mode: string(req.Mode),
		Container: req.Container, MaxHeight: req.MaxHeight, MaxFPS: req.MaxFPS,
		VideoFormatID: req.VideoFormatID, AudioFormatID: req.AudioFormatID,
		AudioFmt: req.AudioFormat, OutputDir: req.OutputDir,
		OutputPath: snapshot.OutputPath, State: string(snapshot.State), Error: snapshot.Error,
		CreatedAt: snapshot.CreatedAt.Format(time.RFC3339), FinishedAt: finished,
	}
	found := false
	for i := range entries {
		if entries[i].ID == e.ID {
			entries[i] = e
			found = true
			break
		}
	}
	if !found {
		entries = append([]HistoryEntry{e}, entries...)
	}
	if len(entries) > 500 {
		entries = entries[:500]
	}
	return h.save(entries)
}

func (h *HistoryService) List(limit int) ([]HistoryEntry, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	entries, err := h.load()
	if err != nil {
		return nil, err
	}
	if limit > 0 && len(entries) > limit {
		entries = entries[:limit]
	}
	return entries, nil
}

func (h *HistoryService) Clear() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, err := h.load(); err != nil {
		return err
	}
	return h.save(nil)
}

// Delete removes one history record without changing the rest of the file.
func (h *HistoryService) Delete(id string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	entries, err := h.load()
	if err != nil {
		return err
	}
	filtered := entries[:0]
	for _, entry := range entries {
		if entry.ID != id {
			filtered = append(filtered, entry)
		}
	}
	return h.save(filtered)
}

func (h *HistoryService) Close() error { return nil }

func atomicWriteHistoryFile(path string, data []byte) error {
	return atomicWriteFile(path, data, "history")
}

func atomicWriteFile(path string, data []byte, label string) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create %s temporary file: %w", label, err)
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
			return fmt.Errorf("set %s temporary file permissions: %w", label, err)
		}
	}
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("write %s temporary file: %w", label, err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync %s temporary file: %w", label, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s temporary file: %w", label, err)
	}
	closed = true
	if runtime.GOOS == "windows" {
		return replaceWindowsFile(tmpPath, path, label)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace %s file: %w", label, err)
	}
	return nil
}

func replaceWindowsFile(tmpPath, path, label string) error {
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		return fmt.Errorf("replace %s file: destination is a directory", label)
	}
	backup := path + ".bak"
	_ = os.Remove(backup)
	if err := os.Rename(path, backup); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("stage existing %s file: %w", label, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		if restoreErr := os.Rename(backup, path); restoreErr != nil && !errors.Is(restoreErr, fs.ErrNotExist) {
			return fmt.Errorf("replace %s file: %w (restore failed: %v)", label, err, restoreErr)
		}
		return fmt.Errorf("replace %s file: %w", label, err)
	}
	_ = os.Remove(backup)
	return nil
}
