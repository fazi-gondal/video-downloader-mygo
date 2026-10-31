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

	"github.com/fazi-gondal/video-downloader-mygo/internal/models"
)

// ResumeStore persists nonterminal download snapshots across app restarts.
type ResumeStore struct {
	path string
	mu   sync.Mutex
}

func NewResumeStore() (*ResumeStore, error) {
	dir := historyDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create download recovery directory: %w", err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(dir, 0o700); err != nil {
			return nil, fmt.Errorf("secure download recovery directory: %w", err)
		}
	}
	return &ResumeStore{path: filepath.Join(dir, "downloads.json")}, nil
}

func (s *ResumeStore) Load() ([]models.DownloadTaskSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read download recovery data: %w", err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(s.path, 0o600); err != nil {
			return nil, fmt.Errorf("secure download recovery data: %w", err)
		}
	}
	var snapshots []models.DownloadTaskSnapshot
	if err := json.Unmarshal(data, &snapshots); err != nil {
		return nil, fmt.Errorf("decode download recovery data: %w", err)
	}
	return snapshots, nil
}

func (s *ResumeStore) Save(snapshots []models.DownloadTaskSnapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if snapshots == nil {
		snapshots = []models.DownloadTaskSnapshot{}
	}
	data, err := json.MarshalIndent(snapshots, "", "  ")
	if err != nil {
		return fmt.Errorf("encode download recovery data: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("create download recovery directory: %w", err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(filepath.Dir(s.path), 0o700); err != nil {
			return fmt.Errorf("secure download recovery directory: %w", err)
		}
	}
	return atomicWriteFile(s.path, data, "download recovery")
}
