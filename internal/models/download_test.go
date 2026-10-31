package models

import (
	"testing"
	"time"
)

func TestDownloadTaskPauseAndResumePreserveProgressAndStaging(t *testing.T) {
	task := NewDownloadTask(DownloadRequest{Title: "example"})
	task.SetProgress(ProgressInfo{DownloadedBytes: 50, TotalBytes: 100, Percent: 50})
	task.SetStagingDir("partial-dir")
	task.RequestPause()

	if !task.PauseRequested() || !task.Cancelled() {
		t.Fatal("pause did not request process cancellation")
	}
	task.SetState(StatePaused)
	if task.StateValue() != StatePaused {
		t.Fatalf("state after pause = %q, want paused", task.StateValue())
	}

	task.ResetForResume()
	snapshot := task.Snapshot()
	if snapshot.State != StatePending {
		t.Fatalf("state after resume reset = %q, want pending", snapshot.State)
	}
	if snapshot.Progress.Percent != 50 || task.StagingDir() != "partial-dir" {
		t.Fatalf("resume reset lost progress or staging directory: progress=%+v staging=%q", snapshot.Progress, task.StagingDir())
	}
	if task.PauseRequested() || task.Cancelled() {
		t.Fatal("resume reset retained pause cancellation")
	}
}

func TestRestorePausedDownloadTaskPreservesRequestProgressAndStaging(t *testing.T) {
	want := DownloadTaskSnapshot{
		ID:         "restored-task",
		Request:    DownloadRequest{URL: "https://example.test/video", Title: "Example", MaxHeight: 720},
		Progress:   ProgressInfo{DownloadedBytes: 70, TotalBytes: 100, Percent: 70},
		CreatedAt:  time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		StagingDir: "partial-download",
	}

	got := RestorePausedDownloadTask(want).Snapshot()
	if got.ID != want.ID || got.State != StatePaused {
		t.Fatalf("restored task identity/state = %q/%q, want %q/%q", got.ID, got.State, want.ID, StatePaused)
	}
	if got.Request.URL != want.Request.URL || got.Request.MaxHeight != want.Request.MaxHeight {
		t.Fatalf("restored request = %+v, want URL and quality from %+v", got.Request, want.Request)
	}
	if got.Progress != want.Progress || got.StagingDir != want.StagingDir || !got.CreatedAt.Equal(want.CreatedAt) {
		t.Fatalf("restored progress/staging/time = %+v/%q/%v", got.Progress, got.StagingDir, got.CreatedAt)
	}
}
