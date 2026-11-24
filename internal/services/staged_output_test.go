package services

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSingleStagedMediaFile(t *testing.T) {
	dir := t.TempDir()
	write := func(name string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("cover.jpg")
	write("subs.srt")
	write("video.mp4.part")
	write("video.mp4")

	got, err := singleStagedMediaFile(dir)
	if err != nil {
		t.Fatalf("singleStagedMediaFile: %v", err)
	}
	if want := filepath.Join(dir, "video.mp4"); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}

	if err := os.Remove(filepath.Join(dir, "video.mp4")); err != nil {
		t.Fatal(err)
	}
	if _, err := singleStagedMediaFile(dir); err == nil {
		t.Fatal("expected failure when only sidecars and .part remain")
	}

	write("second.webm")
	write("first.mkv")
	if _, err := singleStagedMediaFile(dir); err == nil {
		t.Fatal("expected failure when multiple media files remain")
	}
}
