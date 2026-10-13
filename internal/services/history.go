package services

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/fazi-gondal/video-downloader-mygo/internal/models"
)

type HistoryEntry struct {
	ID         string `json:"id"`
	URL        string `json:"url"`
	Title      string `json:"title"`
	Mode       string `json:"mode"`
	Container  string `json:"container"`
	AudioFmt   string `json:"audio_format"`
	OutputDir  string `json:"output_dir"`
	OutputPath string `json:"output_path"`
	State      string `json:"state"`
	Error      string `json:"error"`
	CreatedAt  string `json:"created_at"`
	FinishedAt string `json:"finished_at"`
}

type HistoryService struct {
	path string
	mu   sync.Mutex
}

func NewHistoryService() (*HistoryService, error) {
	dir := historyDir()
	_ = os.MkdirAll(dir, 0o755)
	return &HistoryService{path: filepath.Join(dir, "history.json")}, nil
}

func historyDir() string {
	switch runtime.GOOS {
	case "windows":
		return filepath.Join(os.Getenv("LOCALAPPDATA"), "VideoDownloader")
	case "darwin":
		home, _ := os.UserHomeDir()
		return filepath.Join(home, "Library", "Application Support", "VideoDownloader")
	default:
		home, _ := os.UserHomeDir()
		return filepath.Join(home, ".config", "VideoDownloader")
	}
}

func (h *HistoryService) load() []HistoryEntry {
	data, err := os.ReadFile(h.path)
	if err != nil {
		return nil
	}
	var entries []HistoryEntry
	_ = json.Unmarshal(data, &entries)
	return entries
}

func (h *HistoryService) save(entries []HistoryEntry) error {
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(h.path, data, 0o644)
}

func (h *HistoryService) Record(task *models.DownloadTask) error {
	if !task.State.IsTerminal() {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	entries := h.load()
	req := task.Request
	finished := ""
	if task.FinishedAt != nil {
		finished = task.FinishedAt.Format(time.RFC3339)
	}
	e := HistoryEntry{
		ID: task.ID, URL: req.URL, Title: req.Title, Mode: string(req.Mode),
		Container: req.Container, AudioFmt: req.AudioFormat, OutputDir: req.OutputDir,
		OutputPath: task.OutputPath, State: string(task.State), Error: task.Error,
		CreatedAt: task.CreatedAt.Format(time.RFC3339), FinishedAt: finished,
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
	entries := h.load()
	if limit > 0 && len(entries) > limit {
		entries = entries[:limit]
	}
	return entries, nil
}

func (h *HistoryService) Clear() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.save(nil)
}

// Delete removes one history record without changing the rest of the file.
func (h *HistoryService) Delete(id string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	entries := h.load()
	filtered := entries[:0]
	for _, entry := range entries {
		if entry.ID != id {
			filtered = append(filtered, entry)
		}
	}
	return h.save(filtered)
}

func (h *HistoryService) Close() error { return nil }
