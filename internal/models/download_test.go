package models

import "testing"

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
