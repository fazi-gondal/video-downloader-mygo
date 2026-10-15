package services

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/fazi-gondal/video-downloader-mygo/internal/models"
)

func TestHistoryListMissingFileIsEmpty(t *testing.T) {
	history := testHistoryService(t)

	entries, err := history.List(0)
	if err != nil {
		t.Fatalf("List() error = %v, want nil for missing file", err)
	}
	if len(entries) != 0 {
		t.Fatalf("List() entries = %#v, want empty", entries)
	}
}

func TestHistoryListMalformedFileReturnsError(t *testing.T) {
	history := testHistoryService(t)
	if err := os.WriteFile(history.path, []byte(`[{"id":`), 0o600); err != nil {
		t.Fatal(err)
	}

	entries, err := history.List(0)
	if err == nil || !strings.Contains(err.Error(), "decode history") {
		t.Fatalf("List() error = %v, want decode error", err)
	}
	if entries != nil {
		t.Fatalf("List() entries = %#v, want nil on malformed file", entries)
	}
}

func TestHistorySaveUsesPrivatePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix file permission bits are not meaningful on Windows")
	}
	history := testHistoryService(t)
	if err := os.WriteFile(history.path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := history.save([]HistoryEntry{{ID: "one", Title: "Example"}}); err != nil {
		t.Fatalf("save() error = %v", err)
	}
	info, err := os.Stat(history.path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("history permissions = %o, want 600", got)
	}
	entries, err := history.List(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].ID != "one" {
		t.Fatalf("saved entries = %#v, want one entry", entries)
	}
	if leftovers, err := filepath.Glob(filepath.Join(filepath.Dir(history.path), ".history.json.tmp-*")); err != nil {
		t.Fatal(err)
	} else if len(leftovers) != 0 {
		t.Fatalf("temporary history files remain: %v", leftovers)
	}
}

func TestHistoryRecordDoesNotReplaceMalformedFile(t *testing.T) {
	history := testHistoryService(t)
	original := []byte(`{"broken":`)
	if err := os.WriteFile(history.path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	task := models.NewDownloadTask(models.DownloadRequest{URL: "https://example.com/video", Title: "Example"})
	task.State = models.StateCompleted

	if err := history.Record(task); err == nil {
		t.Fatal("Record() succeeded with malformed history")
	}
	data, err := os.ReadFile(history.path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(original) {
		t.Fatalf("history after failed Record() = %q, want original data", data)
	}
}

func TestHistorySaveFailurePreservesDestination(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "history.json")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(path, "sentinel")
	if err := os.WriteFile(sentinel, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	history := &HistoryService{path: path}

	if err := history.save([]HistoryEntry{{ID: "one"}}); err == nil {
		t.Fatal("save() succeeded when destination was a directory")
	}
	data, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "existing" {
		t.Fatalf("destination data = %q, want existing data", data)
	}
	if leftovers, err := filepath.Glob(filepath.Join(dir, ".history.json.tmp-*")); err != nil {
		t.Fatal(err)
	} else if len(leftovers) != 0 {
		t.Fatalf("temporary history files remain after failure: %v", leftovers)
	}
}

func testHistoryService(t *testing.T) *HistoryService {
	t.Helper()
	return &HistoryService{path: filepath.Join(t.TempDir(), "history.json")}
}
