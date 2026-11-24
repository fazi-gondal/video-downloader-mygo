//go:build !windows

package services

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/fazi-gondal/video-downloader-mygo/internal/models"
)

func TestDownloadCancelKillsYtDlpChildAndClearsStaging(t *testing.T) {
	staging := runFakeYtDlpDownload(t, false)
	if staging != "" {
		t.Fatalf("cancelled download kept staging directory %q", staging)
	}
}

func TestDownloadPauseKillsYtDlpChildAndKeepsStaging(t *testing.T) {
	staging := runFakeYtDlpDownload(t, true)
	if staging == "" {
		t.Fatal("paused download did not keep its staging directory")
	}
	if _, err := os.Stat(staging); err != nil {
		t.Fatalf("paused staging directory: %v", err)
	}
	_ = os.RemoveAll(staging)
}

func runFakeYtDlpDownload(t *testing.T, pause bool) string {
	t.Helper()
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	script := filepath.Join(dir, "fake-yt-dlp")
	body := fmt.Sprintf("#!/bin/sh\nsleep 30 &\necho $! > %q\nwait\n", pidFile)
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	service := &YtDlpService{ytdlpPath: script}
	task := models.NewDownloadTask(models.DownloadRequest{
		URL:       "https://example.com/video",
		Title:     "cancel test",
		Mode:      models.ModeVideoAudio,
		Container: "mp4",
		OutputDir: filepath.Join(dir, "out"),
	})
	done := make(chan error, 1)
	go func() {
		_, err := service.Download(context.Background(), task, nil)
		done <- err
	}()

	childPID := waitForPIDFile(t, pidFile)
	t.Cleanup(func() {
		_ = syscall.Kill(childPID, syscall.SIGKILL)
	})

	if pause {
		task.RequestPause()
	} else {
		task.Cancel()
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Download error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Download did not return after cancellation")
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(childPID, 0); err == syscall.ESRCH {
			return task.StagingDir()
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("yt-dlp child process %d survived cancellation", childPID)
	return ""
}

func waitForPIDFile(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("child pid was not written to %s", path)
	return 0
}
