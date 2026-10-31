package services

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fazi-gondal/video-downloader-mygo/internal/models"
)

func TestResumeStoreRoundTripsPausedDownload(t *testing.T) {
	store := &ResumeStore{path: filepath.Join(t.TempDir(), "downloads.json")}
	want := []models.DownloadTaskSnapshot{{
		ID:         "task-1",
		Request:    models.DownloadRequest{URL: "https://example.test/video", Title: "Example", MaxHeight: 720},
		State:      models.StatePaused,
		Progress:   models.ProgressInfo{DownloadedBytes: 700, TotalBytes: 1000, Percent: 70},
		StagingDir: filepath.Join(t.TempDir(), ".mygo-download-partial"),
		CreatedAt:  time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	}}

	if err := store.Save(want); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Load() returned %d tasks, want 1", len(got))
	}
	if got[0].ID != want[0].ID || !reflect.DeepEqual(got[0].Request, want[0].Request) {
		t.Fatalf("restored identity/request = %q/%+v, want %q/%+v", got[0].ID, got[0].Request, want[0].ID, want[0].Request)
	}
	if got[0].Progress != want[0].Progress || got[0].StagingDir != want[0].StagingDir || !got[0].CreatedAt.Equal(want[0].CreatedAt) {
		t.Fatalf("restored progress/staging/time = %+v/%q/%v", got[0].Progress, got[0].StagingDir, got[0].CreatedAt)
	}

	if err := store.Save(nil); err != nil {
		t.Fatalf("Save(nil) error = %v", err)
	}
	got, err = store.Load()
	if err != nil || len(got) != 0 {
		t.Fatalf("Load() after clearing = (%v, %v), want empty and nil error", got, err)
	}
}

func TestResumeStoreMalformedDataReturnsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "downloads.json")
	if err := os.WriteFile(path, []byte(`[{"ID":`), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &ResumeStore{path: path}

	if _, err := store.Load(); err == nil || !strings.Contains(err.Error(), "decode download recovery data") {
		t.Fatalf("Load() error = %v, want decode error", err)
	}
}
